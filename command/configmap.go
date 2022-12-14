// Copyright 2018 The Goma Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"path"
	"runtime"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
	"cloud.google.com/go/storage"
	"github.com/googleapis/google-cloud-go-testing/storage/stiface"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.chromium.org/goma/server/log"
	cmdpb "go.chromium.org/goma/server/proto/command"
	"go.chromium.org/goma/server/rpc"
)

var (
	ErrWatcherClosed = errors.New("watcher closed")

	pubsubErrors = stats.Int64(
		"go.chromium.org/goma/command/configmap.pubsub-error",
		"configmap pubsub error",
		stats.UnitDimensionless)

	// DefaultViews are the default views provided by this package.
	// You need to register the view for data to actually be collected.
	DefaultViews = []*view.View{
		{
			Description: "configmap pubsub error",
			Measure:     pubsubErrors,
			Aggregation: view.Count(),
		},
	}
)

// ConfigMapLoader loads toolchain_config config map.
//
// ConfigMap provides Watcher, Seqs, Bucket and RuntimeConfigs.
//
// if seq is updated from last load, it will load CmdDescriptor
// from <bucket>/<runtime>/<prebuilt_item>/descriptors/<descriptorHash>.
type ConfigMapLoader struct {
	ConfigMap    ConfigMap
	ConfigLoader ConfigLoader
	ConfigStore  ConfigStore
}

// ConfigMap is an interface to access toolchain config map.
type ConfigMap interface {
	// Watcher returns config map watcher.
	Watcher(ctx context.Context) ConfigMapWatcher

	// Seqs returns a map of config name to sequence.
	Seqs(ctx context.Context) (map[string]string, error)

	// Bucket returns toolchain-config bucket.
	Bucket(ctx context.Context) (string, error)

	// RuntimeConfigs returns a map of RuntimeConfigs.
	RuntimeConfigs(ctx context.Context) (map[string]*cmdpb.RuntimeConfig, error)
}

// ConfigMapWatcher is an interface to watch config map.
type ConfigMapWatcher interface {
	// Next waits for some updates in config map.
	Next(ctx context.Context) error

	// Close closes the watcher.
	Close() error
}

// ConfigMapBucket access config on cloud storage bucket.
//
// <bucket> is <project>-toolchain-config.
// in the <bucket>
//
//	<runtime>/
//	         seq: text, sequence number.
//	         <prebuilt-item>/descriptors/<descriptorHash>: proto CmdDescriptor
//
// Watcher watches */seq files via default notification topic on the bucket.
// Seqs and RuntimeConfigs will read ConfigMapFile everytime.
type ConfigMapBucket struct {
	// URI of config data.
	// gs://<bucket>/
	// e.g. gs://$project-toolchain-config/
	URI string

	ConfigMap     *cmdpb.ConfigMap
	ConfigMapFile string

	PubsubClient *pubsub.Client

	// StorageClient is an interface for accessing Cloud Storage. It can
	// be a Cloud Storage client or a fake for testing.
	StorageClient stiface.Client

	// SubscriberID should be unique per each server instance
	// to get notification in every server instance.
	SubscriberID string

	// Remoteexec API address, if RBE API is used.
	// Otherwise, use service_addr in RuntimeConfig proto.
	RemoteexecAddr string
}

type configMapBucketWatcher struct {
	s      *pubsub.Subscription
	cancel func()
	ch     chan *pubsub.Message
}

func (w configMapBucketWatcher) run(ctx context.Context) {
	logger := log.FromContext(ctx)
	logger.Infof("watch start")
	err := rpc.Retry{}.Do(ctx, func() error {
		err := w.s.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
			logger.Debugf("receive message: %s", msg.ID)
			w.ch <- msg
		})
		if err != nil {
			logger.Errorf("configMapBucketWatcher.run: %v", err)
		}
		if status.Code(err) == codes.Unknown {
			// crbug.com/1226107
			return rpc.RetriableError{
				Err: err,
			}
		}
		return err
	})
	logger.Errorf("configMapBucketWatcher.run retry finished: %v", err)
	close(w.ch)
	logger.Infof("watch finished")
}

func (w configMapBucketWatcher) Next(ctx context.Context) error {
	logger := log.FromContext(ctx)
	for {
		var msg *pubsub.Message
		var ok bool
		select {
		case msg, ok = <-w.ch:
			if !ok {
				return ErrWatcherClosed
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		// https://cloud.google.com/storage/docs/pubsub-notifications#attributes
		eventType := msg.Attributes["eventType"]
		objectId := msg.Attributes["objectId"]
		objectGeneration := msg.Attributes["objectGeneration"]
		eventTime := msg.Attributes["eventTime"]
		publishTime := msg.PublishTime
		logger.Debugf("handle message: %s eventType:%s objectId:%s", msg.ID, eventType, objectId)

		msg.Ack()

		if eventType != storage.ObjectFinalizeEvent {
			continue
		}
		if path.Base(objectId) != "seq" {
			continue
		}
		logger.Infof("%s was updated gen:%s at %s (published:%s)", objectId, objectGeneration, eventTime, publishTime)
		// drain pending messages. these messages were generated
		// before we call Seqs or Data, so we won't need to handle
		// them later.
		for {
			select {
			case msg := <-w.ch:
				logger.Debugf("drain message: %s", msg.ID)
				msg.Ack()
			default:
				return nil
			}
		}
	}
}

func (w configMapBucketWatcher) Close() error {
	ctx := context.Background()
	logger := log.FromContext(ctx)
	logger.Infof("watcher close")
	w.cancel() // finish w.s.Receive in run.
	// drain ch
	go func() {
		for msg := range w.ch {
			// ok to ack because we use notification as trigger only.
			logger.Debugf("drain message: %s", msg.ID)
			msg.Ack()
		}
	}()
	logger.Infof("delete subscription: %s", w.s)
	return w.s.Delete(ctx)
}

type configMapBucketPoller struct {
	baseDelay time.Duration
	done      chan bool
}

func (w configMapBucketPoller) Next(ctx context.Context) error {
	logger := log.FromContext(ctx)
	dur := time.Duration(float64(w.baseDelay) * (1 + 0.2*(rand.Float64()*2-1)))
	logger.Infof("poll wait %s", dur)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return errors.New("poller closed")
	case <-time.After(dur):
		// trigger to load seqs, but loader might detect no seq updates.
		return nil
	}
}

func (w configMapBucketPoller) Close() error {
	ctx := context.Background()
	logger := log.FromContext(ctx)
	logger.Infof("poller close")
	close(w.done)
	return nil
}

func (c ConfigMapBucket) configMap(ctx context.Context) (*cmdpb.ConfigMap, error) {
	if c.ConfigMapFile == "" {
		return proto.Clone(c.ConfigMap).(*cmdpb.ConfigMap), nil
	}
	buf, err := ioutil.ReadFile(c.ConfigMapFile)
	if err != nil {
		return nil, err
	}
	err = prototext.Unmarshal(buf, c.ConfigMap)
	if err != nil {
		return nil, err
	}
	return proto.Clone(c.ConfigMap).(*cmdpb.ConfigMap), nil
}

func cloudStorageNotification(ctx context.Context, s stiface.Client, bucket string) (*storage.Notification, error) {
	bkt := s.Bucket(bucket)
	nm, err := bkt.Notifications(ctx)
	if err != nil {
		return nil, err
	}
	var notification *storage.Notification
	for _, n := range nm {
		// use default topic, created by
		//  $ gsutil notification create -f json <bucket>
		// json payload will be:
		// https://cloud.google.com/storage/docs/json_api/v1/objects#resource-representations
		// we don't use json payload, so '-f none' is ok too.
		if n.TopicID == bucket {
			notification = n
			break
		}
	}
	if notification == nil {
		return nil, fmt.Errorf("notification:%s not found in %v", bucket, nm)
	}
	return notification, nil
}

var storageNotification = cloudStorageNotification

func (c ConfigMapBucket) Watcher(ctx context.Context) ConfigMapWatcher {
	logger := log.FromContext(ctx)
	w, err := c.pubsubWatcher(ctx)
	if err == nil {
		stats.Record(ctx, pubsubErrors.M(0))
		logger.Infof("use pubsub watcher")
		return w
	}
	stats.Record(ctx, pubsubErrors.M(1))
	logger.Errorf("failed to use pubsub watcher: %v", err)
	return configMapBucketPoller{
		baseDelay: 1 * time.Hour,
		done:      make(chan bool),
	}
}

func (c ConfigMapBucket) pubsubWatcher(ctx context.Context) (ConfigMapWatcher, error) {
	bucket, _, err := splitGCSPath(c.URI)
	if err != nil {
		return nil, err
	}
	logger := log.FromContext(ctx)
	notification, err := storageNotification(ctx, c.StorageClient, bucket)
	if err != nil {
		return nil, err
	}
	logger.Infof("topic: %s in %s", notification.TopicID, notification.TopicProjectID)
	topic := c.PubsubClient.TopicInProject(notification.TopicID, notification.TopicProjectID)
	ok, err := topic.Exists(ctx)
	if !ok || err != nil {
		return nil, fmt.Errorf("notification topic:%s (notification:%#v): not exist: %v", topic, notification, err)
	}
	if c.SubscriberID == "" {
		return nil, errors.New("SubscriberID is not specified")
	}
	subscription := c.PubsubClient.Subscription(c.SubscriberID)
	ok, err = subscription.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("subscription:%s err:%v", c.SubscriberID, err)
	}
	if ok {
		sc, err := subscription.Config(ctx)
		if err != nil {
			return nil, fmt.Errorf("subscription config:%s err:%v", c.SubscriberID, err)
		}
		if sc.Topic.String() != topic.String() {
			return nil, fmt.Errorf("topic mismatch? %s != %s. delete subscription:%s", sc.Topic, topic, c.SubscriberID)
		}
	} else {
		logger.Infof("subscriber:%s not found. creating", c.SubscriberID)
		subscription, err = c.PubsubClient.CreateSubscription(ctx, c.SubscriberID, pubsub.SubscriptionConfig{
			Topic: topic,
			// experimental config.
			// minimum is 1 day
			// +12 hours margin, to cover summar time switch (+1 hour)
			// b/112820308
			ExpirationPolicy: 36 * time.Hour,
		})
		if err != nil {
			return nil, fmt.Errorf("create subscription:%s err:%v", c.SubscriberID, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	// TODO: watch configMapFile.
	w := configMapBucketWatcher{
		s:      subscription,
		cancel: cancel,
		ch:     make(chan *pubsub.Message),
	}
	go w.run(ctx)
	return w, nil
}

func (c ConfigMapBucket) Seqs(ctx context.Context) (map[string]string, error) {
	logger := log.FromContext(ctx)
	bucket, _, err := splitGCSPath(c.URI)
	if err != nil {
		return nil, err
	}
	cm, err := c.configMap(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, r := range cm.Runtimes {
		obj := path.Join(r.Name, "seq")
		buf, err := storageReadAll(ctx, c.StorageClient, bucket, obj)
		if err == storage.ErrObjectNotExist {
			logger.Infof("ignore %s: %v", obj, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		m[r.Name] = string(buf)
	}
	return m, nil
}

func (c ConfigMapBucket) Bucket(ctx context.Context) (string, error) {
	bucket, _, err := splitGCSPath(c.URI)
	return bucket, err
}

func (c ConfigMapBucket) RuntimeConfigs(ctx context.Context) (map[string]*cmdpb.RuntimeConfig, error) {
	cm, err := c.configMap(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*cmdpb.RuntimeConfig)
	for _, rt := range cm.Runtimes {
		if rt.ServiceAddr == "" {
			rt.ServiceAddr = c.RemoteexecAddr
		}
		m[rt.Name] = rt
	}
	return m, nil
}

// ConfigLoader loads toolchain_config from cloud storage.
type ConfigLoader struct {
	StorageClient  stiface.Client
	EnableParallel bool

	// for test
	versionID func() string
}

// ConfigStore holds latest config.
type ConfigStore struct {
	lastConfigs map[string]configs // key: toolchain_runtime_name

	// for test
	versionID func() string
}

type configs struct {
	seq     string
	configs []*cmdpb.Config
}

// ErrNoUpdate indicates no update in configmap, returned by ConfigMapLoader.Load.
var ErrNoUpdate = errors.New("toolchain: configmap no update")

// Load loads toolchain config.
// It will return ErrNoUpdate if there is no seq change when force=false.
func (c *ConfigMapLoader) Load(ctx context.Context, force bool) (*cmdpb.ConfigResp, error) {
	logger := log.FromContext(ctx)
	defer logger.Sync()

	updated := make(map[string]string)
	deleted := make(map[string]bool)
	for _, k := range c.ConfigStore.List() {
		deleted[k] = true
	}
	seqs, err := c.ConfigMap.Seqs(ctx)
	if err != nil {
		return nil, err
	}
	for name, seq := range seqs {
		delete(deleted, name)
		oseq := c.ConfigStore.Seq(name)
		if oseq != seq {
			updated[name] = seq
		}
	}
	if len(updated) == 0 && len(deleted) == 0 {
		if !force {
			return nil, ErrNoUpdate
		}
		logger.Infof("configmap no update, but force to load")
	}
	for name := range deleted {
		logger.Infof("delete config for %s", name)
		c.ConfigStore.Delete(name)
	}
	bucket, err := c.ConfigMap.Bucket(ctx)
	if err != nil {
		return nil, err
	}
	runtimeConfigs, err := c.ConfigMap.RuntimeConfigs(ctx)
	if err != nil {
		return nil, err
	}
	logger.Infof("RuntimeConfigs: %v", runtimeConfigs)

	for name, seq := range updated {
		logger.Infof("update config for %s", name)
		uri := fmt.Sprintf("gs://%s/%s", bucket, name)
		runtime := runtimeConfigs[name]
		if runtime == nil {
			return nil, fmt.Errorf("runtime config %s not found", name)
		}
		addr := runtime.ServiceAddr
		if addr == "" {
			logger.Warnf("no addr for %s. ignoring", name)
			continue
		}
		confs, err := c.ConfigLoader.Load(ctx, uri, runtime)
		if err != nil {
			return nil, err
		}
		c.ConfigStore.Set(name, seq, confs)
	}
	resp := c.ConfigStore.ConfigResp()
	logger.Infof("config version: %s", resp.VersionId)
	return resp, nil
}

// merge platform's properties into rbePlatform's properties,
// unless property exists in rbePlatform,
func mergePlatformProperties(rbePlatform *cmdpb.RemoteexecPlatform, platform *cmdpb.Platform) {
	if platform == nil {
		return
	}
	m := make(map[string]bool)
	for _, p := range rbePlatform.Properties {
		m[p.Name] = true
	}
	for _, p := range platform.Properties {
		if m[p.Name] {
			continue
		}
		rbePlatform.Properties = append(rbePlatform.Properties, &cmdpb.RemoteexecPlatform_Property{
			Name:  p.Name,
			Value: p.Value,
		})
	}
}

// Load loads toolchain config from <uri>.
// It sets rc.ServiceAddr  as target addr.
func (c *ConfigLoader) Load(ctx context.Context, uri string, rc *cmdpb.RuntimeConfig) ([]*cmdpb.Config, error) {
	platform := &cmdpb.RemoteexecPlatform{}
	parallel := c.EnableParallel
	for _, p := range rc.Platform.GetProperties() {
		platform.Properties = append(platform.Properties, &cmdpb.RemoteexecPlatform_Property{
			Name:  p.Name,
			Value: p.Value,
		})
	}
	platform.HasNsjail = rc.GetPlatformRuntimeConfig().GetHasNsjail()

	confs, err := loadConfigs(ctx, c.StorageClient, uri, rc, platform, parallel)
	if err != nil {
		return nil, err
	}

	// If this runtime config can support arbitrary toolchain support,
	// also add a config for that. Just define RemoteexecPlatform here.
	// CmdDescriptor will be dynamically generated by a compile request.
	if rc.PlatformRuntimeConfig != nil {
		confs = append(confs, &cmdpb.Config{
			RemoteexecPlatform: platform,
			Dimensions:         rc.PlatformRuntimeConfig.Dimensions,
			Acl:                rc.Acl,
		})
	}

	return confs, nil
}

// List returns a list of config names.
func (c *ConfigStore) List() []string {
	var names []string
	for k := range c.lastConfigs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Set sets name's confs with seq.
func (c *ConfigStore) Set(name, seq string, confs []*cmdpb.Config) {
	if c.lastConfigs == nil {
		c.lastConfigs = make(map[string]configs)
	}
	c.lastConfigs[name] = configs{
		seq:     seq,
		configs: confs,
	}
}

// Seq returns seq of name's config.
func (c *ConfigStore) Seq(name string) string {
	return c.lastConfigs[name].seq
}

// Delete deletes name's config.
func (c *ConfigStore) Delete(name string) {
	delete(c.lastConfigs, name)
}

func versionID() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ConfigResp returns current ConfigResp.
func (c *ConfigStore) ConfigResp() *cmdpb.ConfigResp {
	if c.versionID == nil {
		c.versionID = versionID
	}

	var names []string
	for name := range c.lastConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	resp := &cmdpb.ConfigResp{
		VersionId: c.versionID(),
	}
	for _, name := range names {
		confs := c.lastConfigs[name]
		// TODO: dedup?
		resp.Configs = append(resp.Configs, confs.configs...)
	}
	return resp
}

func splitGCSPath(uri string) (string, string, error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("not gs: URI: %q", uri)
	}
	p := strings.SplitN(uri[len("gs://"):], "/", 2)
	if len(p) != 2 {
		return p[0], "", nil
	}
	return p[0], p[1], nil
}

func storageReadAll(ctx context.Context, client stiface.Client, bucket, name string) ([]byte, error) {
	bkt := client.Bucket(bucket)
	if bkt == nil {
		return nil, fmt.Errorf("could not find bucket %s", bucket)
	}
	obj := bkt.Object(name)
	if obj == nil {
		return nil, fmt.Errorf("could not find object %s/%s", bucket, name)
	}
	rd, err := obj.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	var buf bytes.Buffer
	buf.Grow(int(rd.Size()))
	_, err = buf.ReadFrom(rd)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func loadDescriptor(ctx context.Context, client stiface.Client, bucket, name string) (*cmdpb.CmdDescriptor, error) {
	buf, err := storageReadAll(ctx, client, bucket, name)
	if err != nil {
		return nil, fmt.Errorf("load %s: %v", name, err)
	}
	d := &cmdpb.CmdDescriptor{}
	err = proto.Unmarshal(buf, d)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", name, err)
	}
	return d, nil
}

func checkPrebuilt(rc *cmdpb.RuntimeConfig, objName string) error {
	// objName will be <runtime>/<prebuilts>/descriptors/<hash>
	i := strings.Index(objName, "/descriptors")
	if i < 0 {
		return fmt.Errorf("no prebuilt dir: %s", objName)
	}
	prebuiltName := path.Base(objName[:i])
	for _, prefix := range rc.DisallowedPrebuilts {
		if strings.HasPrefix(prebuiltName, prefix) {
			return fmt.Errorf("disallowed prebuilt %s: by %s", objName, prefix)
		}
	}
	if len(rc.AllowedPrebuilts) == 0 {
		return nil
	}
	allowed := false
	for _, prefix := range rc.AllowedPrebuilts {
		if strings.HasPrefix(prebuiltName, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("not allowed prebuilt %s", objName)
	}
	return nil
}

func checkSelector(rc *cmdpb.RuntimeConfig, sel *cmdpb.Selector) error {
	if sel == nil {
		return errors.New("no selector specified")
	}
	for _, s := range rc.DisallowedCommands {
		if s.Name != "" && s.Name == sel.Name {
			return fmt.Errorf("%s: disallowed by name: %s", sel, s.Name)
		}
		if s.Version != "" && s.Version == sel.Version {
			return fmt.Errorf("%s: disallowed by version: %s", sel, s.Version)
		}
		if s.Target != "" && s.Target == sel.Target {
			return fmt.Errorf("%s: disallowed by target: %s", sel, s.Target)
		}
		if s.BinaryHash != "" && s.BinaryHash == sel.BinaryHash {
			return fmt.Errorf("%s: disallowed by binary hash: %s", sel, s.BinaryHash)
		}
	}
	return nil
}

func loadConfigs(ctx context.Context, client stiface.Client, uri string, rc *cmdpb.RuntimeConfig, platform *cmdpb.RemoteexecPlatform, parallel bool) ([]*cmdpb.Config, error) {
	logger := log.FromContext(ctx)
	bucket, obj, err := splitGCSPath(uri)
	if err != nil {
		return nil, err
	}

	bkt := client.Bucket(bucket)
	if bkt == nil {
		return nil, fmt.Errorf("could not find storage bucket %s", bucket)
	}
	iter := bkt.Objects(ctx, &storage.Query{
		Prefix: obj,
	})

	// pagination?
	var confs []*cmdpb.Config
	logger.Infof("load from %s prefix:%s", bucket, obj)
	start := time.Now()
	var attrsList []*storage.ObjectAttrs
	for {
		// iter does not have an API to read all, so just iterate everything.
		// iter may not get all objects matched around storage@v1.15.0
		// https://github.com/googleapis/google-cloud-go/issues/4676
		attrs, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		// Some string ops, no need to be paralleled.
		if err := checkPrebuilt(rc, attrs.Name); err != nil {
			logger.Infof("prebuilt %s: %v", attrs.Name, err)
			continue
		}

		if path.Base(path.Dir(attrs.Name)) != "descriptors" {
			logger.Infof("ignore %s", attrs.Name)
			continue
		}
		attrsList = append(attrsList, attrs)
	}
	logger.Infof("iterate over %s took %v", bucket, time.Since(start))
	start = time.Now()
	concurrent := 1
	if parallel {
		// Limit concurrent requests to NumCPU * 4.
		concurrent = runtime.NumCPU() * 4
	}
	// The ordering of the output should be guaranteed
	// as unit tests using proto.Equal.
	var eg errgroup.Group
	confList := make([]*cmdpb.Config, len(attrsList))
	sema := make(chan struct{}, concurrent)
	for i := range attrsList {
		i := i
		sema <- struct{}{}
		eg.Go(func() error {
			// Limit number of goroutines.
			defer func() { <-sema }()
			attrs := attrsList[i]
			d, err := loadDescriptor(ctx, client, bucket, attrs.Name)
			if err != nil {
				return err
			}
			ts := timestamppb.New(attrs.Updated)
			if err = checkSelector(rc, d.Selector); err != nil {
				logger.Errorf("selector in %s/%s: %v", bucket, attrs.Name, err)
				return nil
			}
			if d.Setup == nil {
				logger.Errorf("no setup in %s/%s", bucket, attrs.Name)
				return nil
			}
			if d.Setup.PathType == cmdpb.CmdDescriptor_UNKNOWN_PATH_TYPE {
				logger.Errorf("unknown path type in %s/%s", bucket, attrs.Name)
				return nil
			}
			// TODO: fix config definition.
			// BuildInfo is used for key for cache key.
			//  include cmd_server hash etc?
			// BuildInfo.Timestamp is used for dedup in exec_server.
			confList[i] = &cmdpb.Config{
				Target: &cmdpb.Target{
					Addr: rc.ServiceAddr,
				},
				BuildInfo: &cmdpb.BuildInfo{
					Timestamp: ts,
				},
				CmdDescriptor:      d,
				RemoteexecPlatform: platform,
				Acl:                rc.Acl,
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	for i := range attrsList {
		attrs := attrsList[i]
		conf := confList[i]
		if conf == nil {
			continue
		}
		confs = append(confs, conf)
		logger.Infof("%s/%s: %s", bucket, attrs.Name, conf.CmdDescriptor.GetSelector())
	}
	logger.Infof("loaded from %s prefix:%s: %d configs using %v", bucket, obj, len(confs), time.Since(start))
	return confs, nil
}
