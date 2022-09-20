// Copyright 2018 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package acl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/goma/server/auth"
	"go.chromium.org/goma/server/auth/account"
	"go.chromium.org/goma/server/log"
	pb "go.chromium.org/goma/server/proto/auth"
)

// AuthDB provides authentication database; user groups.
type AuthDB interface {
	IsMember(ctx context.Context, email, group string) (bool, error)
}

// Checker checks token.
type Checker struct {
	AuthDB
	account.Pool

	mu     sync.RWMutex
	config *pb.ACL

	accounts map[string]account.Account
}

// Set sets config in the checker.
func (c *Checker) Set(ctx context.Context, config *pb.ACL) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Pool == nil {
		c.Pool = account.Empty{}
	}
	if c.accounts == nil {
		c.accounts = make(map[string]account.Account)
	}

	logger := log.FromContext(ctx)

	seen := make(map[string]bool)

	for _, g := range config.Groups {
		if g.ServiceAccount == "" {
			continue
		}
		if seen[g.ServiceAccount] {
			continue
		}
		sa, err := c.Pool.New(g.ServiceAccount)
		if err != nil {
			return fmt.Errorf("service account %q: %v", g.ServiceAccount, err)
		}
		seen[g.ServiceAccount] = true
		if sa.Equals(c.accounts[g.ServiceAccount]) {
			// no diff
			logger.Infof("service account %s: no change", g.ServiceAccount)
			continue
		}
		logger.Infof("service account %s: update", g.ServiceAccount)
		c.accounts[g.ServiceAccount] = sa
	}
	for sa := range c.accounts {
		if !seen[sa] {
			logger.Infof("service account %s: deleted", sa)
			delete(c.accounts, sa)
		}
	}
	logger.Infof("acl updated")
	c.config = proto.Clone(config).(*pb.ACL)
	return nil
}

var errNoMatchingGroup = errors.New("no matching group")

// FindGroup finds a group for tokenInfo.
func (c *Checker) FindGroup(ctx context.Context, tokenInfo *auth.TokenInfo) (*pb.Group, error) {
	logger := log.FromContext(ctx)

	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, g := range c.config.GetGroups() {
		ok, err := checkGroup(ctx, tokenInfo, g, c.AuthDB)
		if err != nil {
			logger.Errorf("filed to check group %s for %q %q: %v", g.Id, tokenInfo.Email, tokenInfo.Audience, err)
			return nil, err
		}
		if !ok {
			continue
		}
		return g, nil
	}
	return nil, fmt.Errorf("no group for %q %q: %w", tokenInfo.Email, tokenInfo.Audience, errNoMatchingGroup)
}

// CheckToken checks token and returns group id and token used for backend API.
func (c *Checker) CheckToken(ctx context.Context, token *oauth2.Token, tokenInfo *auth.TokenInfo) (string, *oauth2.Token, error) {

	logger := log.FromContext(ctx)

	g, err := c.FindGroup(ctx, tokenInfo)
	if err != nil {
		if ctx.Err() != nil {
			err := status.FromContextError(ctx.Err()).Err()
			logger.Errorf("acl check context error: %v", err)
			return "", nil, err
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			logger.Errorf("acl check deadline exceeded: %v", err)
			return "", nil, status.Errorf(codes.DeadlineExceeded, "find group failed: %v", err)
		case errors.Is(err, context.Canceled):
			logger.Errorf("acl check canceled: %v", err)
			return "", nil, status.Errorf(codes.Canceled, "find group canceled: %v", err)
		case errors.Is(err, errNoMatchingGroup):
			logger.Errorf("no acl match: %v", err)
			return "", nil, status.Errorf(codes.PermissionDenied, "access rejected")
		}
		logger.Errorf("acl check backend err: %v", err)
		return "", nil, err
	}

	logger.Debugf("in group:%s", g.Id)
	if g.Reject {
		logger.Errorf("group:%s rejected", g.Id)
		return g.Id, nil, grpc.Errorf(codes.PermissionDenied, "access rejected")
	}
	if g.ServiceAccount == "" {
		logger.Debugf("group:%s use EUC", g.Id)
		return g.Id, token, nil
	}

	sa := c.accounts[g.ServiceAccount]
	if sa == nil {
		logger.Errorf("group:%s service account not found: %s", g.Id, g.ServiceAccount)
		return g.Id, nil, grpc.Errorf(codes.Internal, "service account not found: %s", g.ServiceAccount)
	}
	saToken, err := sa.Token(ctx)
	if err != nil {
		logger.Errorf("group:%s service account:%s error:%v", g.Id, g.ServiceAccount, err)
		return g.Id, nil, grpc.Errorf(codes.Internal, "service account:%s error:%v", g.ServiceAccount, err)
	}
	logger.Debugf("group:%s use service account:%s", g.Id, g.ServiceAccount)
	return g.Id, saToken, nil
}

func checkGroup(ctx context.Context, tokenInfo *auth.TokenInfo, g *pb.Group, authDB AuthDB) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Debugf("checking group:%s", g.Id)
	if g.Audience != "" {
		if tokenInfo.Audience != g.Audience {
			logger.Debugf("audience mismatch: %s != %s", tokenInfo.Audience, g.Audience)
			return false, nil
		}
	}
	if len(g.Emails) == 0 && len(g.Domains) == 0 && authDB != nil {
		ok, err := authDB.IsMember(ctx, tokenInfo.Email, g.Id)
		if err != nil {
			logger.Warnf("authdb lookup error:%s: %v", g.Id, err)
			return false, err
		}
		if !ok {
			logger.Debugf("not member in authdb group:%s", g.Id)
		}
		return ok, nil
	}
	if !match(tokenInfo.Email, g.Emails, g.Domains) {
		logger.Debugf("emails/domains mismatch: client email not in group %s", g.Id)
		return false, nil
	}
	return true, nil
}

func match(email string, emails, domains []string) bool {
	for _, e := range emails {
		if email == e {
			return true
		}
	}
	for _, d := range domains {
		if strings.HasSuffix(email, "@"+d) {
			return true
		}
	}
	return false
}
