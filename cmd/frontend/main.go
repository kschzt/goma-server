// Copyright 2017 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

/*
Binary frontend is goma frontend.

	$ frontend --port $port
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"path/filepath"

	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
	"go.opencensus.io/zpages"
	k8sapi "golang.org/x/build/kubernetes/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"

	"go.chromium.org/goma/server/auth"
	"go.chromium.org/goma/server/backend"
	"go.chromium.org/goma/server/frontend"
	"go.chromium.org/goma/server/log"
	"go.chromium.org/goma/server/profiler"
	"go.chromium.org/goma/server/server"
	"go.chromium.org/goma/server/server/healthz"

	authpb "go.chromium.org/goma/server/proto/auth"
	bepb "go.chromium.org/goma/server/proto/backend"
	execpb "go.chromium.org/goma/server/proto/exec"
	execlogpb "go.chromium.org/goma/server/proto/execlog"
	filepb "go.chromium.org/goma/server/proto/file"
)

var (
	port  = flag.Int("port", 80, "listening port (goma api endpoints)")
	gport = flag.Int("gport", 5050, "grpc port")
	mport = flag.Int("mport", 8081, "monitor port")

	authAddr = flag.String("auth-addr", "passthrough:///auth-server:5050",
		"auth server address")

	backendConfig = flag.String("backend-config", "", "backend config. text proto of backend.BackendConfig")

	configDir = flag.String("config-dir", "/etc/goma", "config directory")

	// TODO set these value using kubernetes api
	namespace = flag.String("namespace", "", "cluster namespace for trace prefix and label")

	traceProjectID = flag.String("trace-project-id", "", "project id for cloud tracing")

	serviceAccountFile = flag.String("service-account-file", "", "service account json file")

	memoryMargin = flag.String("memory-margin",
		k8sapi.NewQuantity(maxMsgSize, k8sapi.BinarySI).String(),
		`accepts incoming requests if memory is available more than margin (bytes), if this value is positive.  can be kubernetes quantity string. e.g. "100Mi".  will be used if -memory-threshold is not specified.`)
)

const maxMsgSize = 64 * 1024 * 1024

type memoryCheck struct {
	hardThreshold int64
	softThreshold int64
}

// Admit checks we can accept new request.
// if memory usage is less than mc.softThreshold, it will accept.
// Otherwise, it will try to run GC to release memory.
// if memory usage is [mc.softThreshold, mc.hardThreshold), it returns
// Unavailable error.
// if memory usage is more than mc.hardThreshold, it returns ResourceExausted.
func (mc memoryCheck) Admit(req *http.Request) error {
	if mc.softThreshold <= 0 {
		return nil
	}
	rss := server.ResidentMemorySize()
	if rss <= mc.softThreshold {
		return nil
	}
	ctx := req.Context()
	logger := log.FromContext(ctx)
	logger.Warnf("memory size %d > soft threshold:%d", rss, mc.softThreshold)
	rss = server.GC(ctx)
	if rss <= mc.softThreshold {
		logger.Infof("GC reduced memory size to %d", rss)
		return nil
	}
	m := fmt.Sprintf("memory size %d > soft threshold:%d: over=%d", rss, mc.softThreshold, rss-mc.softThreshold)
	healthz.SetUnhealthy(m)
	logger.Errorf("GC couldn't reduce memory size: %s", m)
	if mc.hardThreshold > 0 && rss > mc.hardThreshold {
		return status.Errorf(codes.ResourceExhausted, "server resource exhausted")
	}
	return status.Errorf(codes.Unavailable, "server unavailable")
}

func newMainServer(mux *http.ServeMux) server.Server {
	hsMain := server.NewHTTP(*port, mux)
	if *port != 443 {
		return hsMain
	}
	certpem := filepath.Join(*configDir, "cert/cert.pem")
	keypem := filepath.Join(*configDir, "cert/key.pem")
	return server.NewHTTPS(hsMain, certpem, keypem)
}

func main() {
	flag.Parse()

	ctx := context.Background()

	profiler.Setup(ctx)

	logger := log.FromContext(ctx)
	defer logger.Sync()

	err := server.Init(ctx, *traceProjectID, "frontend")
	if err != nil {
		logger.Fatal(err)
	}
	err = view.Register(frontend.DefaultViews...)
	if err != nil {
		logger.Fatal(err)
	}
	err = view.Register(auth.DefaultViews...)
	if err != nil {
		logger.Fatal(err)
	}
	trace.ApplyConfig(trace.Config{
		DefaultSampler: server.NewLimitedSampler(server.DefaultTraceFraction, server.DefaultTraceQPS),
	})

	s, err := server.NewGRPC(*gport,
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.MaxRecvMsgSize(maxMsgSize))
	if err != nil {
		logger.Fatal(err)
	}

	authConn, err := server.DialContext(ctx, *authAddr)
	if err != nil {
		logger.Fatalf("dial %s: %v", *authAddr, err)
	}
	defer authConn.Close()

	beCfg := &bepb.BackendConfig{}
	err = prototext.Unmarshal([]byte(*backendConfig), beCfg)
	if err != nil {
		logger.Fatal(err)
	}
	be, done, err := backend.FromProto(ctx, beCfg, backend.Option{
		Auth: &auth.Auth{
			Client: authpb.NewAuthServiceClient(authConn),
		},
		APIKeyDir: filepath.Join(*configDir, "api-keys"),
	})
	if err != nil {
		logger.Fatal(err)
	}
	defer done()

	mux := http.NewServeMux()
	var memoryChecker memoryCheck
	if *memoryMargin != "" {
		q, err := k8sapi.ParseQuantity(*memoryMargin)
		if err != nil {
			logger.Fatal(err)
		}
		limit, err := server.MemoryLimit()
		if err != nil {
			logger.Errorf("unknown memory limit: %v", err)
		} else {
			memoryChecker.hardThreshold = limit - q.Value()
			memoryChecker.softThreshold = limit - 2*q.Value()
			limitq := k8sapi.NewQuantity(limit, k8sapi.BinarySI)
			logger.Infof("memory check threshold: limit:%s - margin:%s = hard:%d, soft:%d", limitq, q, memoryChecker.hardThreshold, memoryChecker.softThreshold)
		}
	}

	fe := frontend.Frontend{
		AC:          memoryChecker,
		Backend:     be,
		TraceLabels: map[string]string{
			// want to use this to compare between clusters,
			// but not availble yet. http://b/77931512
		},
	}
	frontend.Register(mux, fe)

	if be, ok := be.(backend.GRPC); ok {
		logger.Infof("register grpc server")
		execpb.RegisterExecServiceServer(s.Server, be.ExecServer)
		filepb.RegisterFileServiceServer(s.Server, be.FileServer)
		execlogpb.RegisterLogServiceServer(s.Server, be.ExeclogServer)
		// TODO: expose bytestream?
	}

	// This is for healthcheck from cloud load balancer.
	// TODO: Do not allow access from other than load balancer.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	hsMain := newMainServer(mux)
	hsMonitoring := server.NewHTTP(*mport, nil)
	zpages.Handle(http.DefaultServeMux, "/debug")
	server.Run(ctx, s, hsMain, hsMonitoring)
}
