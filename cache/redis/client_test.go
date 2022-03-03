// Copyright 2020 Google LLC. All Rights Reserved.

package redis

import (
	"context"
	"flag"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"

	"go.chromium.org/goma/server/log"
	pb "go.chromium.org/goma/server/proto/cache"
)

var (
	numFilesPerExecReq = flag.Int("num_files_per_exec_req", 500, "number of files per ExecReq for BenchmarkGet")
)

func BenchmarkGet(b *testing.B) {
	log.SetZapLogger(zap.NewNop())
	s := NewFakeServer(b)

	ctx := context.Background()
	c := NewClient(ctx, s.Addr().String(), Opts{
		MaxIdleConns:   DefaultMaxIdleConns,
		MaxActiveConns: DefaultMaxActiveConns,
	})
	defer c.Close()

	b.Logf("b.N=%d", b.N)
	var wg sync.WaitGroup
	var (
		mu    sync.Mutex
		nerrs int
	)
	wg.Add(b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			var rg sync.WaitGroup
			rg.Add(*numFilesPerExecReq)
			for i := 0; i < *numFilesPerExecReq; i++ {
				go func() {
					defer rg.Done()
					_, err := c.Get(ctx, &pb.GetReq{
						Key: "key",
					})
					if err != nil {
						mu.Lock()
						nerrs++
						mu.Unlock()
					}
				}()
			}
			rg.Wait()
		}()
	}
	wg.Wait()
	mu.Lock()
	b.Logf("nerrs=%d", nerrs)
	mu.Unlock()
}

func TestSetNonZeroTTL(t *testing.T) {
	expectedKey := "test_key"
	expectedValue := "test_value"
	expectedTTL := 5 * time.Millisecond

	log.SetZapLogger(zap.NewNop())
	s := NewFakeServer(t)

	ctx := context.Background()
	c := NewClient(ctx, s.Addr().String(), Opts{
		MaxIdleConns:   DefaultMaxIdleConns,
		MaxActiveConns: DefaultMaxActiveConns,
		EntryTTL:       expectedTTL,
	})
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	_, err := c.Put(ctx, &pb.PutReq{
		Kv: &pb.KV{
			Key:   expectedKey,
			Value: []byte(expectedValue),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"SET", expectedKey, expectedValue, "PX", strconv.FormatInt(expectedTTL.Milliseconds(), 10)}
	got := s.lastRequest()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("lastRequest() mismatch (-want +got):\n%s", diff)
	}
}

func TestSetZeroTTL(t *testing.T) {
	expectedKey := "test_key"
	expectedValue := "test_value"
	expectedTTL := 0 * time.Millisecond

	log.SetZapLogger(zap.NewNop())
	s := NewFakeServer(t)

	ctx := context.Background()
	c := NewClient(ctx, s.Addr().String(), Opts{
		MaxIdleConns:   DefaultMaxIdleConns,
		MaxActiveConns: DefaultMaxActiveConns,
		EntryTTL:       expectedTTL,
	})
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	_, err := c.Put(ctx, &pb.PutReq{
		Kv: &pb.KV{
			Key:   expectedKey,
			Value: []byte(expectedValue),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"SET", expectedKey, expectedValue}
	got := s.lastRequest()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("lastRequest() mismatch (-want +got):\n%s", diff)
	}
}
