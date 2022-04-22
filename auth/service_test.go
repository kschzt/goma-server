// Copyright 2017 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authpb "go.chromium.org/goma/server/proto/auth"
)

func TestServiceExpire(t *testing.T) {
	ctx := context.Background()
	token := &oauth2.Token{
		AccessToken: "token-value",
		TokenType:   "Bearer",
	}
	ch := make(chan chan bool)
	now := time.Now()
	expiresAt := func() time.Time {
		return now.Add(1 * time.Hour)
	}
	var deadline time.Time
	var fetchCount int
	s := &Service{
		CheckToken: func(ctx context.Context, token *oauth2.Token, tokenInfo *TokenInfo) (string, *oauth2.Token, error) {
			return "", token, nil
		},
		fetchInfo: func(ctx context.Context, t *oauth2.Token) (*TokenInfo, error) {
			fetchCount++
			if !reflect.DeepEqual(t, token) {
				return nil, errors.New("wrong token")
			}
			return &TokenInfo{
				Email:     "foo@example.com",
				ExpiresAt: expiresAt(),
			}, nil
		},
		runAt: func(t time.Time, f func()) {
			deadline = t
			rch := <-ch
			f()
			close(rch)
		},
	}

	t.Logf("initial fetch")
	resp, err := s.Auth(ctx, &authpb.AuthReq{
		Authorization: "Bearer token-value",
	})
	if err != nil {
		t.Fatalf("Auth failed: %v", err)
	}
	expires := expiresAt()
	expiresProto := timestamppb.New(expires)
	want := &authpb.AuthResp{
		Email:     "foo@example.com",
		ExpiresAt: expiresProto,
		Quota:     -1,
		Token: &authpb.Token{
			AccessToken: "token-value",
			TokenType:   "Bearer",
		},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("s.Auth(ctx, req)=%v; want=%v", resp, want)
	}
	key := tokenKey(token)
	s.mu.Lock()
	if _, ok := s.tokenCache[key]; !ok {
		t.Errorf("%q must exist in tokenCache", key)
	}
	s.mu.Unlock()

	if fetchCount != 1 {
		t.Errorf("fetch count=%d; want=1", fetchCount)
	}

	t.Logf("30 minutes later")
	now = now.Add(30 * time.Minute)
	resp, err = s.Auth(ctx, &authpb.AuthReq{
		Authorization: "Bearer token-value",
	})
	if err != nil {
		t.Fatalf("Auth failed: %v", err)
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("s.Auth(ctx, req)=%v; want=%v", resp, want)
	}
	if fetchCount != 1 {
		t.Errorf("fetch count=%d; want=1", fetchCount)
	}

	t.Logf("1 hours later")
	now = now.Add(30 * time.Minute)
	rch := make(chan bool)
	// fire runAt
	ch <- rch
	if !deadline.Equal(expiryTime(expires)) {
		t.Errorf("deadline %s != %s (expiresAt %s)", deadline, expiryTime(expires), expires)
	}
	// wait runAt finish.
	<-rch

	s.mu.Lock()
	if _, ok := s.tokenCache[key]; ok {
		t.Errorf("%q must be removed from tokenCache", key)
	}
	s.mu.Unlock()

	expires2 := expiresAt()
	if otime, ntime := expires, expires2; otime.Equal(ntime) {
		t.Fatalf("expiresAt: %s == %s", otime, ntime)
	}
	want.ExpiresAt = timestamppb.New(expires2)
	resp, err = s.Auth(ctx, &authpb.AuthReq{
		Authorization: "Bearer token-value",
	})
	if err != nil {
		t.Fatalf("Auth failed: %v", err)
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("s.Auth(ctx, req)=%v; want=%v", resp, want)
	}
	s.mu.Lock()
	if _, ok := s.tokenCache[key]; !ok {
		t.Errorf("%q must exist in tokenCache", key)
	}
	s.mu.Unlock()

	if fetchCount != 2 {
		t.Errorf("fetch count=%d; want=2", fetchCount)
	}
}

// TODO: revise this when we implement ACL
func TestAuth(t *testing.T) {
	t.Logf("0. access succeeds by cache")
	email := "example@google.com"
	expiresAt := time.Now().Add(10 * time.Second)
	ptypeExpiresAt := timestamppb.New(expiresAt)
	ti := &TokenInfo{
		Email:     email,
		Audience:  "test-audience.apps.googleusercontent.com",
		ExpiresAt: expiresAt,
	}
	checkToken := func(ctx context.Context, token *oauth2.Token, tokenInfo *TokenInfo) (string, *oauth2.Token, error) {
		if !reflect.DeepEqual(tokenInfo, ti) {
			return "", nil, fmt.Errorf("wrong token info: got=%v; want=%v", tokenInfo, ti)
		}
		return "", token, nil
	}

	want := &authpb.AuthResp{
		Email:     email,
		ExpiresAt: ptypeExpiresAt,
		Quota:     -1,
		Token: &authpb.Token{
			AccessToken: "test",
			TokenType:   "Bearer",
		},
	}
	token := &oauth2.Token{
		AccessToken: "test",
		TokenType:   "Bearer",
	}
	key := tokenKey(token)
	s := &Service{
		CheckToken: checkToken,
		tokenCache: map[string]*tokenCacheEntry{
			key: {
				TokenInfo: ti,
				Token:     token,
			},
		},
	}

	req := &authpb.AuthReq{
		Authorization: "Bearer test",
	}
	resp, err := s.Auth(context.Background(), req)
	if err != nil {
		t.Errorf("Auth(%q) error %v; want nil error", req, err)
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("Auth(%q)=%q; want %q", req, resp, want)
	}

	t.Logf("1. access succeeds by fetching token info.")
	s1 := &Service{
		CheckToken: checkToken,
		fetchInfo: func(ctx context.Context, token *oauth2.Token) (*TokenInfo, error) {
			return ti, nil
		},
	}
	resp, err = s1.Auth(context.Background(), req)
	if err != nil {
		t.Errorf("Auth(%q) error %v; want nil error", req, err)
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("Auth(%q)=%q; want %q", req, resp, want)
	}
	// and confirm it is stored in cache.
	if entry, ok := s.tokenCache[key]; !ok || !reflect.DeepEqual(entry.TokenInfo, ti) {
		t.Errorf(`tokenCache[%q].TokenInfo=%q; want %q`, key, entry.TokenInfo, ti)
	}

	t.Logf("2. failed to fetch token info.")
	s2 := &Service{
		CheckToken: checkToken,
		fetchInfo: func(ctx context.Context, token *oauth2.Token) (*TokenInfo, error) {
			return nil, errors.New("fetch failed")
		},
	}
	resp, err = s2.Auth(context.Background(), req)
	if err != nil {
		t.Errorf("Auth(%q) error %v; want nil error", req, err)
	}
	if resp.Quota != 0 {
		t.Errorf("Auth(%q).Quota=%d; want 0", req, resp.Quota)
	}
	if resp.ErrorDescription == "" {
		t.Errorf("Auth(%q).ErrorDescription=%q; want non empty", req, resp.ErrorDescription)
	}

	t.Logf("3. non-allowed user access")
	s3 := &Service{
		CheckToken: func(ctx context.Context, token *oauth2.Token, tokenInfo *TokenInfo) (string, *oauth2.Token, error) {
			if !strings.HasSuffix(tokenInfo.Email, "@google.com") {
				return "", token, status.Errorf(codes.PermissionDenied, "not allowed")
			}
			return "", token, nil

		},
		fetchInfo: func(ctx context.Context, token *oauth2.Token) (*TokenInfo, error) {
			return &TokenInfo{
				Email:     "not_allowed@example.com",
				ExpiresAt: expiresAt,
			}, nil
		},
	}
	resp, err = s3.Auth(context.Background(), req)
	if err != nil {
		t.Errorf("Auth(%q) error %v; want nil error", req, err)
	}
	if resp == nil {
		t.Errorf("Auth(%q)=nil, want non-nil resp", req)
	} else {
		if resp.Quota != 0 {
			t.Errorf("Auth(%q).Quota=%d; want 0", req, resp.Quota)
		}
		if resp.ErrorDescription == "" {
			t.Errorf("Auth(%q).ErrorDescription=%q; want non empty", req, resp.ErrorDescription)
		}
	}
	t.Logf("4. failed membership check")
	s4 := &Service{
		CheckToken: func(ctx context.Context, token *oauth2.Token, tokenInfo *TokenInfo) (string, *oauth2.Token, error) {
			if tokenInfo.Email == "misfortune@example.com" {
				return "", token, status.Errorf(codes.DeadlineExceeded, "deadline exceeded")
			}
			return "", token, nil

		},
		fetchInfo: func(ctx context.Context, token *oauth2.Token) (*TokenInfo, error) {
			return &TokenInfo{
				Email:     "misfortune@example.com",
				ExpiresAt: expiresAt,
			}, nil
		},
	}
	resp, err = s4.Auth(context.Background(), req)
	if err == nil {
		t.Errorf("Auth(%q)=%v, %v; want error", req, resp, err)
	}
}
