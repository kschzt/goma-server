// Copyright 2018 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package bytestreamio

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.chromium.org/goma/server/rpc/grpctest"
	bpb "google.golang.org/genproto/googleapis/bytestream"
	pb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubByteStreamReadClient struct {
	pb.ByteStreamClient
	resourceName string
	data         []byte
	chunksize    int
}

func (c *stubByteStreamReadClient) Read(ctx context.Context, req *pb.ReadRequest, opts ...grpc.CallOption) (pb.ByteStream_ReadClient, error) {
	if req.ResourceName != c.resourceName {
		return nil, fmt.Errorf("bad resource name: %q; want %q", req.ResourceName, c.resourceName)
	}
	if req.ReadOffset != 0 {
		return nil, fmt.Errorf("bad read offset=%d; want=%d", req.ReadOffset, 0)
	}
	if req.ReadLimit != 0 {
		return nil, fmt.Errorf("bad read limit=%d; want=%d", req.ReadLimit, 0)
	}
	return &stubReadClient{
		c: c,
	}, nil
}

type stubReadClient struct {
	pb.ByteStream_ReadClient
	c      *stubByteStreamReadClient
	offset int
}

func (r *stubReadClient) Recv() (*pb.ReadResponse, error) {
	if r.offset >= len(r.c.data) {
		return nil, io.EOF
	}
	data := r.c.data[r.offset:]
	if len(data) > r.c.chunksize {
		data = data[:r.c.chunksize]
	}
	r.offset += len(data)
	return &pb.ReadResponse{
		Data: data,
	}, nil
}

func TestReader(t *testing.T) {
	const datasize = 1 * 1024 * 1024
	const chunksize = 8192
	const bufsize = 1024
	data := make([]byte, 4*1024*1024)
	_, err := rand.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	const resourceName = "resource-name"
	c := &stubByteStreamReadClient{
		resourceName: resourceName,
		data:         data,
		chunksize:    chunksize,
	}
	ctx := context.Background()

	r, err := Open(ctx, c, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if bytes.Equal(out.Bytes(), data) {
		t.Fatal("data setup failed")
	}

	buf := make([]byte, bufsize)
	_, err = io.CopyBuffer(&out, r, buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Errorf("read doesn't match: (-want +got)\n%s", cmp.Diff(data, out))
	}
}

type stubByteStreamServer struct {
	bpb.ByteStreamServer
	resourceName             string
	buf                      bytes.Buffer
	err                      error
	finished                 bool
	earlyReturnCommittedSize int64
}

func (s *stubByteStreamServer) Write(stream bpb.ByteStream_WriteServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		if req.ResourceName != s.resourceName {
			return fmt.Errorf("bad resource name: %q; want %q", req.ResourceName, s.resourceName)
		}
		if s.finished {
			return errors.New("bad write to finished client")
		}
		if req.WriteOffset != int64(s.buf.Len()) {
			return fmt.Errorf("bad write offset=%d; want=%d", req.WriteOffset, s.buf.Len())
		}
		if len(req.Data) > maxChunkSizeBytes {
			return fmt.Errorf("too large data=%d. chunksize=%d", len(req.Data), maxChunkSizeBytes)
		}
		s.buf.Write(req.Data) // err is always nil.
		if s.err != nil {
			return s.err
		}
		if s.earlyReturnCommittedSize != 0 {
			return stream.SendAndClose(&bpb.WriteResponse{CommittedSize: s.earlyReturnCommittedSize})
		}
		if req.FinishWrite {
			s.finished = true
			break
		}
	}
	return stream.SendAndClose(&bpb.WriteResponse{
		CommittedSize: int64(s.buf.Len()),
	})
}

// to hit WriteTo.
type bytesReader struct {
	r *bytes.Reader
}

func (r bytesReader) Read(buf []byte) (int, error) {
	return r.r.Read(buf)
}

func TestWriter(t *testing.T) {
	const datasize = 10*1024*1024 + 2048
	data := make([]byte, datasize)
	_, err := rand.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	const resourceName = "resource-name"
	srv := grpc.NewServer()
	s := &stubByteStreamServer{resourceName: resourceName}
	bpb.RegisterByteStreamServer(srv, s)
	addr, serverStop, err := grpctest.StartServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer serverStop()
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := bpb.NewByteStreamClient(conn)
	ctx := context.Background()

	w, err := Create(ctx, c, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	err = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !s.finished {
		t.Errorf("write not finished")
	}
	if s.buf.Len() != len(data) {
		t.Errorf("write len=%d; want=%d", s.buf.Len(), len(data))
	}
	if !bytes.Equal(s.buf.Bytes(), data) {
		t.Errorf("write doesn't match: (-want +got)\n%s", cmp.Diff(data, s.buf.Bytes()))
	}

}

func TestWriterAlreadyExists(t *testing.T) {
	const datasize = 1*1024*1024 + 2048
	const bufsize = 1024

	data := make([]byte, datasize)
	_, err := rand.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	const resourceName = "resource-name"
	srv := grpc.NewServer()
	s := &stubByteStreamServer{resourceName: resourceName, err: status.Errorf(codes.AlreadyExists, "already exists")}
	bpb.RegisterByteStreamServer(srv, s)
	addr, serverStop, err := grpctest.StartServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer serverStop()
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := bpb.NewByteStreamClient(conn)
	ctx := context.Background()

	w, err := Create(ctx, c, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, bufsize)
	_, err = io.CopyBuffer(w, bytesReader{bytes.NewReader(data)}, buf)
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	err = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !w.ok {
		t.Errorf("writer.ok=%t; want=true", w.ok)
	}
	if s.buf.Len() == len(data) {
		t.Errorf("write len=%d << %d", s.buf.Len(), len(data))
	}
	if bytes.Equal(s.buf.Bytes(), data) {
		t.Errorf("write match? should not match for already exists resource")
	}
}

func TestWriterAlreadyExistsEarlyReturn(t *testing.T) {
	const datasize = 1*1024*1024 + 2048
	const bufsize = 1024

	data := make([]byte, datasize)
	_, err := rand.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	const resourceName = "resource-name"
	srv := grpc.NewServer()
	s := &stubByteStreamServer{resourceName: resourceName, earlyReturnCommittedSize: datasize}
	bpb.RegisterByteStreamServer(srv, s)
	addr, serverStop, err := grpctest.StartServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer serverStop()
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := bpb.NewByteStreamClient(conn)
	ctx := context.Background()

	w, err := Create(ctx, c, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, bufsize)
	_, err = io.CopyBuffer(w, bytesReader{bytes.NewReader(data)}, buf)
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	err = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !w.ok {
		t.Errorf("writer.ok=%t; want=true", w.ok)
	}
	if s.buf.Len() == len(data) {
		t.Errorf("write len=%d << %d", s.buf.Len(), len(data))
	}
	if bytes.Equal(s.buf.Bytes(), data) {
		t.Errorf("write match? should not match for already exists resource")
	}
}

func TestWriterServerError(t *testing.T) {
	const datasize = 1*1024*1024 + 2048
	const bufsize = 1024

	data := make([]byte, datasize)
	_, err := rand.Read(data)
	if err != nil {
		t.Fatal(err)
	}

	const resourceName = "resource-name"
	srv := grpc.NewServer()
	s := &stubByteStreamServer{resourceName: resourceName, err: status.Errorf(codes.Unavailable, "server unavailable")}
	bpb.RegisterByteStreamServer(srv, s)
	addr, serverStop, err := grpctest.StartServer(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer serverStop()
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := bpb.NewByteStreamClient(conn)
	ctx := context.Background()

	w, err := Create(ctx, c, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, bufsize)
	_, err = io.CopyBuffer(w, bytesReader{bytes.NewReader(data)}, buf)
	if status.Convert(err).Code() != codes.Unavailable {
		t.Errorf("error not propagated to client: %v", err)
	}
}
