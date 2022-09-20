module go.chromium.org/goma/server

go 1.16

require (
	cloud.google.com/go/compute v1.10.0
	cloud.google.com/go/errorreporting v0.2.0
	cloud.google.com/go/monitoring v1.6.0
	cloud.google.com/go/profiler v0.3.0
	cloud.google.com/go/pubsub v1.25.1
	cloud.google.com/go/storage v1.26.0
	contrib.go.opencensus.io/exporter/stackdriver v0.13.11 // freeze https://github.com/census-ecosystem/opencensus-go-exporter-stackdriver/issues/301. wait v0.13.13 ?
	github.com/aws/aws-sdk-go v1.44.101 // indirect
	github.com/bazelbuild/remote-apis v0.0.0-20210718193713-0ecef08215cf
	github.com/bazelbuild/remote-apis-sdks v0.0.0-20220429154201-6c8489803a6f
	github.com/fsnotify/fsnotify v1.5.4
	github.com/golang/glog v1.0.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da
	github.com/gomodule/redigo v1.8.9
	github.com/google/go-cmp v0.5.9
	github.com/google/uuid v1.3.0
	github.com/googleapis/gax-go/v2 v2.5.1
	github.com/googleapis/google-cloud-go-testing v0.0.0-20190904031503-2d24dde44ba5
	github.com/grpc-ecosystem/go-grpc-middleware v1.3.0
	github.com/pborman/uuid v1.2.1 // indirect
	go.opencensus.io v0.23.0
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/goleak v1.2.0 // indirect
	go.uber.org/zap v1.23.0
	golang.org/x/build v0.0.0-20191031202223-0706ea4fce0c
	golang.org/x/net v0.0.0-20220909164309-bea034e7d591
	golang.org/x/oauth2 v0.0.0-20220909003341-f21342109be1
	golang.org/x/sync v0.0.0-20220601150217-0de741cfad7f
	google.golang.org/api v0.96.0
	google.golang.org/genproto v0.0.0-20220915135415-7fd63a7952de
	google.golang.org/grpc v1.49.0
	google.golang.org/protobuf v1.28.1
	gopkg.in/yaml.v2 v2.4.0 // indirect
)
