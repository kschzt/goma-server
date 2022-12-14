// Copyright 2019 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

/*
Binary remoteexec-proxy is a proxy server between Goma client and Remote Execution API.
*/
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	rpb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/prototext"

	"go.chromium.org/goma/server/auth"
	"go.chromium.org/goma/server/auth/account"
	"go.chromium.org/goma/server/auth/acl"
	"go.chromium.org/goma/server/cache"
	"go.chromium.org/goma/server/cache/gcs"
	"go.chromium.org/goma/server/cache/redis"
	"go.chromium.org/goma/server/file"
	"go.chromium.org/goma/server/frontend"
	"go.chromium.org/goma/server/httprpc"
	execrpc "go.chromium.org/goma/server/httprpc/exec"
	execlogrpc "go.chromium.org/goma/server/httprpc/execlog"
	filerpc "go.chromium.org/goma/server/httprpc/file"
	"go.chromium.org/goma/server/log"
	"go.chromium.org/goma/server/profiler"
	gomapb "go.chromium.org/goma/server/proto/api"
	authpb "go.chromium.org/goma/server/proto/auth"
	cachepb "go.chromium.org/goma/server/proto/cache"
	cmdpb "go.chromium.org/goma/server/proto/command"
	execpb "go.chromium.org/goma/server/proto/exec"
	execlogpb "go.chromium.org/goma/server/proto/execlog"
	filepb "go.chromium.org/goma/server/proto/file"
	"go.chromium.org/goma/server/remoteexec"
	"go.chromium.org/goma/server/remoteexec/digest"
	"go.chromium.org/goma/server/rpc"
	"go.chromium.org/goma/server/server"
)

var (
	port = flag.Int("port", 8090, "listening port (goma api endpoints)")

	remoteexecAddr           = flag.String("remoteexec-addr", "", "remoteexec API endpoint")
	remoteInstanceName       = flag.String("remote-instance-name", "", "remote instance name")
	allowedUsers             = flag.String("allowed-users", "", "comma separated list of allowed users. `*@domain` will match any user in domain. if empty, current user is allowed.")
	serviceAccountJSON       = flag.String("service-account-json", "", "service account json, used to talk to RBE and cloud storage (if --file-cache-bucket is used)")
	platformContainerImage   = flag.String("platform-container-image", "", "docker uri of platform container image")
	insecureRemoteexec       = flag.Bool("insecure-remoteexec", false, "insecure grpc for remoteexec API")
	insecureSkipVerify       = flag.Bool("insecure-skip-verify", false, "insecure skip verifying the server certificate")
	additionalTLSCertificate = flag.String("additional-tls-certificate", "", "additional TLS root certificate for verifying the server certificate")
	execMaxRetryCount        = flag.Int("exec-max-retry-count", 5, "max retry count for exec call. 0 is unlimited count, but bound to ctx timtout. Use small number for powerful clients to run local fallback quickly. Use large number for powerless clients to use remote more than local.")
	execMissingInputLimit    = flag.Int("exec-missing-input-limit", 100, "max missing inputs per exec call response. 0 is unlimited, meaning the client will be told about all missing inputs.")

	fileCacheBucket = flag.String("file-cache-bucket", "", "file cache bucking store bucket")

	execConfigFile = flag.String("exec-config-file", "", "exec inventory config file")

	maxDigestCacheEntries = flag.Int("max-digest-cache-entries", 2e6, "maximum entries in in-memory digest cache")

	traceProjectID = flag.String("trace-project-id", "", "project id for cloud tracing")
	traceFraction  = flag.Float64("trace-sampling-fraction", 1.0, "sampling fraction for stackdriver trace")
	traceQPS       = flag.Float64("trace-sampling-qps-limit", 1.0, "sampling qps limit for stackdriver trace")

	redisMaxIdleConns   = flag.Int("redis-max-idle-conns", redis.DefaultMaxIdleConns, "maximum number of idle connections to redis.")
	redisMaxActiveConns = flag.Int("redis-max-active-conns", redis.DefaultMaxActiveConns, "maximum number of active connections to redis.")
)

func myEmail(ctx context.Context) string {
	logger := log.FromContext(ctx)
	username := os.Getenv("USER")
	if username == "" {
		u, err := user.Current()
		if err != nil {
			logger.Fatalf("failed to get username: need --allowed-users: %v", err)
		}
		username = u.Username
	}
	buf, err := ioutil.ReadFile("/etc/mailname")
	if err != nil {
		logger.Fatalf("failed to get email: need --allowed-users: %v", err)
	}
	return fmt.Sprintf("%s@%s", username, strings.TrimSpace(string(buf)))
}

type authClient struct {
	Service *auth.Service
}

func (c authClient) Auth(ctx context.Context, req *authpb.AuthReq, opts ...grpc.CallOption) (*authpb.AuthResp, error) {
	return c.Service.Auth(ctx, req)
}

type fileClient struct {
	Service filepb.FileServiceServer
}

func (c fileClient) StoreFile(ctx context.Context, req *gomapb.StoreFileReq, opts ...grpc.CallOption) (*gomapb.StoreFileResp, error) {
	return c.Service.StoreFile(ctx, req)
}

func (c fileClient) LookupFile(ctx context.Context, req *gomapb.LookupFileReq, opts ...grpc.CallOption) (*gomapb.LookupFileResp, error) {
	return c.Service.LookupFile(ctx, req)
}

type execlogService struct {
	execlogpb.UnimplementedLogServiceServer
}

func (c execlogService) SaveLog(ctx context.Context, req *gomapb.SaveLogReq) (*gomapb.SaveLogResp, error) {
	return &gomapb.SaveLogResp{}, nil
}

type cacheClient struct {
	Service cachepb.CacheServiceServer
}

func (c cacheClient) Get(ctx context.Context, req *cachepb.GetReq, opts ...grpc.CallOption) (*cachepb.GetResp, error) {
	return c.Service.Get(ctx, req)
}

func (c cacheClient) Put(ctx context.Context, req *cachepb.PutReq, opts ...grpc.CallOption) (*cachepb.PutResp, error) {
	return c.Service.Put(ctx, req)
}

const gomaClientClientID = "687418631491-r6m1c3pr0lth5atp4ie07f03ae8omefc.apps.googleusercontent.com"

type defaultACL struct {
	allowedUser    []string
	allowedDomains []string
}

func (a defaultACL) Load(ctx context.Context) (*authpb.ACL, error) {
	serviceAccount := "default"
	if *serviceAccountJSON != "" {
		serviceAccount = strings.TrimSuffix(filepath.Base(*serviceAccountJSON), ".json")
	}

	return &authpb.ACL{
		Groups: []*authpb.Group{
			{
				Id:             "user",
				Audience:       gomaClientClientID,
				Emails:         a.allowedUser,
				Domains:        a.allowedDomains,
				ServiceAccount: serviceAccount,
			},
		},
	}, nil
}

type reExecServer struct {
	execpb.UnimplementedExecServiceServer
	re *remoteexec.Adapter
}

func (r reExecServer) Exec(ctx context.Context, req *gomapb.ExecReq) (*gomapb.ExecResp, error) {
	ctx, id := rpc.TagID(ctx, req.GetRequesterInfo())
	logger := log.FromContext(ctx)
	logger.Infof("call exec %s", id)
	return r.re.Exec(ctx, req)
}

type reFileServer struct {
	filepb.UnimplementedFileServiceServer
	s filepb.FileServiceServer
}

func (r reFileServer) StoreFile(ctx context.Context, req *gomapb.StoreFileReq) (*gomapb.StoreFileResp, error) {
	ctx, id := rpc.TagID(ctx, req.GetRequesterInfo())
	logger := log.FromContext(ctx)
	logger.Infof("call storefile %s", id)
	return r.s.StoreFile(ctx, req)
}

func (r reFileServer) LookupFile(ctx context.Context, req *gomapb.LookupFileReq) (*gomapb.LookupFileResp, error) {
	ctx, id := rpc.TagID(ctx, req.GetRequesterInfo())
	logger := log.FromContext(ctx)
	logger.Infof("call lookupfile %s", id)
	return r.s.LookupFile(ctx, req)
}

type localBackend struct {
	ExecService execpb.ExecServiceServer
	FileService filepb.FileServiceServer
	Auth        httprpc.Auth
}

func (b localBackend) Ping() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger := log.FromContext(ctx)
		ctx, err := b.Auth.Auth(ctx, req)
		if err != nil {
			http.Error(w, fmt.Sprintf("auth failed: %v", err), http.StatusUnauthorized)
			logger.Errorf("ping unauthorized: %v", err)
			return
		}
		w.Header().Set("Accept-Encoding", "gzip, deflate")
		fmt.Fprintln(w, "ok")
	})
}

func (b localBackend) Exec() http.Handler {
	return execrpc.Handler(b.ExecService, httprpc.Timeout(5*time.Minute), httprpc.WithAuth(b.Auth))
}

func (b localBackend) ByteStream() http.Handler {
	return http.HandlerFunc(http.NotFound)
}

func (b localBackend) StoreFile() http.Handler {
	return filerpc.StoreHandler(b.FileService, httprpc.Timeout(1.*time.Minute), httprpc.WithAuth(b.Auth))
}

func (b localBackend) LookupFile() http.Handler {
	return filerpc.LookupHandler(b.FileService, httprpc.Timeout(1*time.Minute), httprpc.WithAuth(b.Auth))
}

func (b localBackend) Execlog() http.Handler {
	return execlogrpc.Handler(execlogService{}, httprpc.Timeout(1*time.Minute), httprpc.WithAuth(b.Auth))
}

func readConfigResp(fname string) (*cmdpb.ConfigResp, error) {
	b, err := ioutil.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	resp := &cmdpb.ConfigResp{}
	err = prototext.Unmarshal(b, resp)
	if err != nil {
		return nil, err
	}
	// fix target address etc.
	for _, c := range resp.Configs {
		if c.Target == nil {
			c.Target = &cmdpb.Target{}
		}
		c.Target.Addr = *remoteexecAddr
		if c.BuildInfo == nil {
			c.BuildInfo = &cmdpb.BuildInfo{}
		}
	}
	return resp, nil
}

func main() {
	spanTimeout := remoteexec.DefaultSpanTimeout
	flag.DurationVar(&spanTimeout.Inventory, "exec-inventory-timeout", spanTimeout.Inventory, "timeout of exec-inventory")
	flag.DurationVar(&spanTimeout.InputTree, "exec-input-tree-timeout", spanTimeout.InputTree, "timeout of exec-iput-tree")
	flag.DurationVar(&spanTimeout.Setup, "exec-setup-timeout", spanTimeout.Setup, "timeout of exec-setup")
	flag.DurationVar(&spanTimeout.CheckCache, "exec-check-cache-timeout", spanTimeout.CheckCache, "timeout of exec-check-cache")
	flag.DurationVar(&spanTimeout.CheckMissing, "exec-check-missing-timeout", spanTimeout.CheckMissing, "timeout of exec-check-missing")
	flag.DurationVar(&spanTimeout.UploadBlobs, "exec-upload-blobs-timeout", spanTimeout.UploadBlobs, "timeout of exec-upload-blobs")
	flag.DurationVar(&spanTimeout.Execute, "exec-execute-timeout", spanTimeout.Execute, "timeout of exec-execute")
	flag.DurationVar(&spanTimeout.Response, "exec-response-timeout", spanTimeout.Response, "timeout of exec-response")

	flag.Parse()
	ctx := context.Background()

	profiler.Setup(ctx)

	logger := log.FromContext(ctx)
	defer logger.Sync()

	if *allowedUsers == "" {
		*allowedUsers = myEmail(ctx)
	}
	var allowed []string
	var allowedDomains []string
	for _, u := range strings.Split(*allowedUsers, ",") {
		u = strings.TrimSpace(u)
		if strings.HasPrefix(u, "*@") {
			allowedDomains = append(allowedDomains, strings.TrimPrefix(u, "*@"))
		} else {
			allowed = append(allowed, u)
		}
	}
	logger.Infof("allow access for %q / domains %q", allowed, allowedDomains)

	err := server.Init(ctx, *traceProjectID, "remoteexec-proxy")
	if err != nil {
		logger.Fatal(err)
	}

	trace.ApplyConfig(trace.Config{
		DefaultSampler: server.NewLimitedSampler(*traceFraction, *traceQPS),
	})

	saDir := "/"
	if *serviceAccountJSON != "" {
		logger.Infof("using service account: %s", *serviceAccountJSON)
		saDir = filepath.Dir(*serviceAccountJSON)
	} else {
		logger.Infof("using default service account")
	}
	aclCheck := acl.ACL{
		Loader: defaultACL{
			allowedUser:    allowed,
			allowedDomains: allowedDomains,
		},
		Checker: acl.Checker{
			Pool: account.JSONDir{
				Dir: saDir,
				Scopes: []string{
					"https://www.googleapis.com/auth/cloud-build-service",
				},
			},
		},
	}
	err = aclCheck.Update(ctx)
	if err != nil {
		logger.Fatal(err)
	}

	authService := &auth.Service{
		CheckToken: aclCheck.CheckToken,
	}

	var cclient cachepb.CacheServiceClient
	if *fileCacheBucket != "" {
		logger.Infof("use cloud storage bucket: %s", *fileCacheBucket)
		var opts []option.ClientOption
		if *serviceAccountJSON != "" {
			opts = append(opts, option.WithServiceAccountFile(*serviceAccountJSON))
		}
		gsclient, err := storage.NewClient(ctx, opts...)
		if err != nil {
			logger.Fatalf("storage client failed: %v", err)
		}
		defer gsclient.Close()
		cclient = cache.LocalClient{
			CacheServiceServer: gcs.New(gsclient.Bucket(*fileCacheBucket)),
		}
	} else {
		cacheService, err := cache.New(cache.Config{
			MaxBytes: 1 * 1024 * 1024 * 1024,
		})
		if err != nil {
			logger.Fatal(err)
		}
		cclient = cacheClient{
			Service: cacheService,
		}
	}

	fileServiceClient := fileClient{
		Service: &file.Service{
			Cache: cclient,
		},
	}

	certPool, err := x509.SystemCertPool()
	if err != nil {
		logger.Fatal(err)
	}
	if certPool == nil {
		logger.Fatal("got nil certPool")
	}
	if *additionalTLSCertificate != "" {
		caCert, err := ioutil.ReadFile(*additionalTLSCertificate)
		if err != nil {
			logger.Fatal(err)
		}
		if ok := certPool.AppendCertsFromPEM(caCert); !ok {
			logger.Fatal("No certificates loaded from %s", *additionalTLSCertificate)
		}
	}
	tlsConfig := &tls.Config{
		InsecureSkipVerify: *insecureSkipVerify,
		RootCAs:            certPool,
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithStatsHandler(&ocgrpc.ClientHandler{}),
	}
	if *insecureRemoteexec {
		opts[0] = grpc.WithInsecure()
		logger.Warnf("use insecrure remoteexec API")
	}

	reConn, err := grpc.DialContext(ctx, *remoteexecAddr, opts...)
	if err != nil {
		logger.Fatal(err)
	}
	defer reConn.Close()

	var digestCache remoteexec.DigestCache
	redisAddr, err := redis.AddrFromEnv()
	if err != nil {
		logger.Warnf("redis disabled for gomafile-digest: %v", err)
		digestCache = digest.NewCache(nil, *maxDigestCacheEntries)
	} else {
		logger.Infof("redis enabled for gomafile-digest: %v idle=%d active=%d", redisAddr, *redisMaxIdleConns, *redisMaxActiveConns)
		digestCache = digest.NewCache(redis.NewClient(ctx, redisAddr, redis.Opts{
			Prefix:         "gomafile-digest:",
			MaxIdleConns:   *redisMaxIdleConns,
			MaxActiveConns: *redisMaxActiveConns,
		}), *maxDigestCacheEntries)
	}

	re := &remoteexec.Adapter{
		InstancePrefix: path.Dir(*remoteInstanceName),
		ExecTimeout:    15 * time.Minute,
		SpanTimeout:    spanTimeout,
		Client: remoteexec.Client{
			ClientConn: reConn,
			Retry: rpc.Retry{
				MaxRetry: *execMaxRetryCount,
			},
		},
		InsecureClient: *insecureRemoteexec,
		GomaFile:       fileServiceClient,
		DigestCache:    digestCache,
		ToolDetails: &rpb.ToolDetails{
			ToolName:    "remoteexec_proxy",
			ToolVersion: "0.0.0-experimental",
		},
		FileLookupSema:    make(chan struct{}, 2),
		CASBlobLookupSema: make(chan struct{}, 20),
		MissingInputLimit: *execMissingInputLimit,
	}

	configResp := &cmdpb.ConfigResp{
		VersionId: time.Now().UTC().Format(time.RFC3339),
		Configs: []*cmdpb.Config{
			{
				Target: &cmdpb.Target{
					Addr: *remoteexecAddr,
				},
				BuildInfo: &cmdpb.BuildInfo{},
				Dimensions: []string{
					"os:linux",
				},
				RemoteexecPlatform: &cmdpb.RemoteexecPlatform{
					RbeInstanceBasename: path.Base(*remoteInstanceName),
					Properties: []*cmdpb.RemoteexecPlatform_Property{
						{
							Name:  "OSFamily",
							Value: "Linux",
						},
					},
				},
			},
		},
	}
	// TODO: document config example?
	if *execConfigFile != "" {
		c, err := readConfigResp(*execConfigFile)
		if err != nil {
			logger.Fatal(err)
		}
		configResp = c
	}
	err = re.Inventory.Configure(ctx, configResp)
	if err != nil {
		logger.Fatal(err)
	}
	mux := http.DefaultServeMux
	frontend.Register(mux, frontend.Frontend{
		Backend: localBackend{
			ExecService: reExecServer{re: re},
			FileService: reFileServer{s: fileServiceClient.Service},
			Auth: &auth.Auth{
				Client: authClient{Service: authService},
			},
		},
	})

	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintln(w, "ok")
	}))
	tmpl := template.Must(template.New("index").Parse(`
<html>
<head>
 <title>Goma remoteexec_proxy at {{.Port}}</title>
</head>
<body>
<h1>Goma remoteexec_proxy</h1>

<p><b>remoteexec-addr:</b> {{.RemoteexecAddr}}</p>
<p><b>remote-instance-name:</b> {{.RemoteInstanceName}}</p>
<p><b>allowed-users:</b> {{.AllowedUsers}}</p>
<p><b>service-account-json:</b> <a href="file://{{.ServiceAccountJSON}}">{{.ServiceAccountJSON}}</a></p>
<p><b>platform-container-image:</b> {{.PlatformContainerImage}}</p>
<p><b>redis:</b> {{.RedisAddr}}</p>
<p><b>file-cache-bucket:</b> {{.FileCacheBucket}}</p>

<p><b>config:</b>
<pre>{{.Config}}</pre>

<hr>
<p>
<a href="/debug/tracez">/debug/tracez</a> |
<a href="/debug/rpcz">/debug/rpcz</a> |
<a href="/healthz">/healthz - for health check</a>
</body>
</html>`))

	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		err := tmpl.Execute(w, struct {
			Port                   int
			RemoteexecAddr         string
			RemoteInstanceName     string
			AllowedUsers           []string
			ServiceAccountJSON     string
			PlatformContainerImage string
			RedisAddr              string
			FileCacheBucket        string
			Config                 *cmdpb.ConfigResp
		}{
			Port:                   *port,
			RemoteexecAddr:         *remoteexecAddr,
			RemoteInstanceName:     *remoteInstanceName,
			AllowedUsers:           allowed,
			ServiceAccountJSON:     *serviceAccountJSON,
			PlatformContainerImage: *platformContainerImage,
			RedisAddr:              redisAddr,
			FileCacheBucket:        *fileCacheBucket,
			Config:                 configResp,
		})
		if err != nil {
			logger := log.FromContext(ctx)
			logger.Errorf("index template: %v", err)
		}
	}))
	hsMain := server.NewHTTP(*port, mux)
	server.Run(ctx, hsMain)
}
