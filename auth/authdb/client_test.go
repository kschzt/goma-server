// Copyright 2018 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package authdb

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/goma/server/httprpc"
	authdbrpc "go.chromium.org/goma/server/httprpc/authdb"
	pb "go.chromium.org/goma/server/proto/auth"
)

type fakeAuthDBServer struct {
	pb.UnimplementedAuthDBServiceServer
	t        *testing.T
	want     *pb.CheckMembershipReq
	resp     *pb.CheckMembershipResp
	respErrs []error
}

func (a *fakeAuthDBServer) CheckMembership(ctx context.Context, req *pb.CheckMembershipReq) (*pb.CheckMembershipResp, error) {
	if len(a.respErrs) > 0 {
		var err error
		err, a.respErrs = a.respErrs[0], a.respErrs[1:]
		return nil, err
	}
	if !proto.Equal(req, a.want) {
		a.t.Errorf("CheckMembership: req=%#v; want=%#v", req, a.want)
		return nil, errors.New("unexpected request")
	}
	return a.resp, nil
}

func TestClient(t *testing.T) {
	ctx := context.Background()
	fakeserver := &fakeAuthDBServer{}
	s := httptest.NewServer(authdbrpc.Handler(fakeserver))
	defer s.Close()

	for _, tc := range []struct {
		desc         string
		email, group string
		resp         bool
		respErrs     []error
		want         bool
		wantErr      bool
	}{
		{
			desc:  "ok",
			email: "someone@google.com",
			group: "goma-googlers",
			resp:  true,
			want:  true,
		},
		{
			desc:  "not member",
			email: "someone@example.com",
			group: "goma-googlers",
			resp:  false,
			want:  false,
		},
		{
			desc:     "temp failure",
			email:    "someone@google.com",
			group:    "goma-googlers",
			resp:     true,
			respErrs: []error{status.Errorf(codes.Unavailable, "unavailable")},
			want:     true,
		},
		{
			desc:     "temp failure false",
			email:    "someone@google.com",
			group:    "goma-googlers",
			resp:     false,
			respErrs: []error{status.Errorf(codes.Unavailable, "unavailable")},
			want:     false,
		},
		{
			desc:     "server error",
			email:    "someone@google.com",
			group:    "goma-googlers",
			resp:     true,
			respErrs: []error{status.Errorf(codes.InvalidArgument, "bad request")},
			wantErr:  true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			// can't start server in each test case,
			// due to duplicate metrics collector registration.
			fakeserver.t = t
			fakeserver.want = &pb.CheckMembershipReq{
				Email: tc.email,
				Group: tc.group,
			}
			fakeserver.resp = &pb.CheckMembershipResp{
				IsMember: tc.resp,
			}
			fakeserver.respErrs = tc.respErrs
			c := Client{
				Client: &httprpc.Client{
					Client: s.Client(),
					URL:    s.URL + "/authdb/checkMembership",
				},
			}
			got, err := c.IsMember(ctx, tc.email, tc.group)
			if tc.wantErr {
				if err == nil {
					t.Errorf("IsMember(ctx, %q, %q)=%v, nil; want=false, err", tc.email, tc.group, got)
					return
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("IsMember(ctx, %q, %q)=%v, %v; want=%v, false", tc.email, tc.group, got, err, tc.want)
			}
		})
	}
}
