//go:build cluster

package clustere2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

// captureSink is a reporter.Reporter that records every emitted event so
// the cluster-e2e tests can assert on the JSON event shape (task-retry
// events, Attempt counts, ...) coming from real Tekton.
type captureSink struct {
	mu     sync.Mutex
	events []reporter.Event
}

func (s *captureSink) Emit(e reporter.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}
func (s *captureSink) Close() error { return nil }
func (s *captureSink) snapshot() []reporter.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]reporter.Event, len(s.events))
	copy(out, s.events)
	return out
}

// One k3d cluster + Tekton install is shared across the whole fixture
// table — the per-fixture cost should be one PipelineRun, not a fresh
// cluster bring-up. Each subtest creates an ephemeral namespace inside
// that shared cluster.
//
// The `TKN_ACT_TEKTON_VERSION` env var, when set, overrides the
// in-binary `DefaultTektonVersion`. The cluster-integration CI workflow
// sets it per matrix leg so each LTS leg runs the full fixture table
// against the requested Tekton release.
func TestClusterE2E(t *testing.T) {
	dir := t.TempDir()
	kubecfg := filepath.Join(dir, "kubeconfig")
	cmStore := volumes.NewStore("")
	secStore := volumes.NewStore("")
	cb := clusterbe.New(clusterbe.Options{
		CacheDir:      dir,
		Driver:        k3d.New(k3d.Options{ClusterName: "tkn-act-e2e", KubeconfigPath: kubecfg}),
		ConfigMaps:    cmStore,
		Secrets:       secStore,
		TektonVersion: os.Getenv("TKN_ACT_TEKTON_VERSION"),
	})
	t.Cleanup(func() { _ = cb.Cleanup(context.Background()) })

	for _, f := range fixtures.All() {
		f := f
		if f.DockerOnly {
			continue
		}
		t.Run(f.TestName(), func(t *testing.T) {
			runFixtureCluster(t, cb, cmStore, secStore, kubecfg, f)
		})
	}

	_ = backend.Backend(cb) // compile-time check
}

func runFixtureCluster(t *testing.T, cb *clusterbe.Backend, cmStore, secStore *volumes.Store, kubecfgPath string, f fixtures.Fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "e2e", f.Dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files in %s", f.Dir)
	}
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "cluster-e2e")
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	// Per-fixture isolation: clear any inline / bundle entries left
	// over from a prior subtest. The Backend holds a fixed pointer to
	// these stores (one Backend amortizes the k3d bring-up cost across
	// the table), so the cheapest way to keep fixtures from polluting
	// each other is to reset the in-memory layers in place. Without
	// this, the `volumes` fixture's inline `app-config/greeting`
	// shadows the next fixture's bundle-loaded `app-config/greeting`
	// (Inline > Bundle in volumes.Store), and cluster CI fails the
	// `configmap-from-yaml` step's `test "$..." = "hello-from-yaml"`
	// with the prior fixture's `hello-from-cm` value.
	cmStore.Reset()
	secStore.Reset()

	// Bundle-loaded CM/Secret resources (kind: ConfigMap / kind: Secret
	// embedded in the fixture's -f stream) sit at the lowest precedence
	// layer; inline f.ConfigMaps / f.Secrets entries below shadow them.
	for name, bytesByKey := range b.ConfigMaps {
		cmStore.LoadBytes(name, bytesByKey)
	}
	for name, bytesByKey := range b.Secrets {
		secStore.LoadBytes(name, bytesByKey)
	}
	for name, kv := range f.ConfigMaps {
		for k, v := range kv {
			cmStore.Add(name, k, v)
		}
	}
	for name, kv := range f.Secrets {
		for k, v := range kv {
			secStore.Add(name, k, v)
		}
	}

	pmap := map[string]tektontypes.ParamValue{}
	for k, v := range f.Params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	// resolver-git: build a per-test bare repo and inject its file://
	// URL as the repoURL pipeline param. The cluster backend's lazy-
	// resolve path (Track 1 #9 Phase 1) inlines the resolved TaskSpec
	// before submitting the PipelineRun, so the bare repo only needs
	// to be reachable from the host running tkn-act — it doesn't need
	// to be visible inside the k3d cluster.
	if f.Dir == "resolver-git" {
		seed := filepath.Join("..", "..", "testdata", "e2e", "resolver-git", "seed")
		url, err := fixtures.BuildBareRepoFromSeed(seed, t.TempDir())
		if err != nil {
			t.Fatalf("build bare repo: %v", err)
		}
		pmap["repoURL"] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: url}
	}

	// Resolver fixtures (Phase 3 of Track 1 #9): the cluster backend
	// inlines resolver-backed taskRefs client-side before submitting
	// the PipelineRun, so spinning up the same httptest.Server +
	// Registry combo works on cluster mode the same way it does on
	// docker. Resolver=="" returns a nil harness and the run is
	// unaffected.
	rh, rerr := fixtures.NewResolverHarness(filepath.Join("..", "..", "testdata", "e2e", f.Dir), f.Resolver)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rh != nil {
		defer rh.Close()
		if rh.ExtraParamName != "" {
			pmap[rh.ExtraParamName] = tektontypes.ParamValue{
				Type:      tektontypes.ParamTypeString,
				StringVal: rh.ExtraParamValue,
			}
		}
	}

	// resolver-remote (Track 1 #9 Phase 5, Mode B): pre-load a Task
	// into a fresh namespace via the cluster's dynamic client, then
	// build a RemoteResolver pointed at the same k3d. The Pipeline's
	// taskRef.resolver: cluster gets dispatched through the remote
	// driver; Tekton's built-in cluster resolver controller (shipped
	// in release.yaml) reads the pre-loaded Task and writes it back
	// on status.data. tkn-act decodes, validates, and inlines.
	var remoteResolver *refresolver.RemoteResolver
	if f.Dir == "resolver-remote" {
		ns, dyn := preloadResolverRemoteTask(t, ctx, kubecfgPath)
		pmap["targetNamespace"] = tektontypes.ParamValue{
			Type:      tektontypes.ParamTypeString,
			StringVal: ns,
		}
		// Build the remote resolver from the kubeconfig path the
		// cluster harness already provisioned for k3d. The resolver's
		// dynamic client is overridden directly to skip a second
		// kubeconfig load — same wire effect as the CLI's
		// --remote-resolver-context plumbing.
		remoteResolver = refresolver.NewRemoteResolverFromOptions(refresolver.RemoteResolverOptions{
			Dynamic:   dyn,
			Namespace: "default", // ResolutionRequests live in any ns; default is fine.
			Timeout:   2 * time.Minute,
		})
	}

	jsonRep := reporter.NewJSON(io.Discard)
	cap := &captureSink{}
	rep := reporter.NewTee(jsonRep, cap)
	// Wire the default refresolver registry so resolver-backed
	// fixtures (Track 1 #9) dispatch on the cluster path the same
	// way they do under docker. The on-disk cache dir is a per-test
	// tmpdir so subtests don't share resolved bytes. For Phase-3
	// (hub/http) fixtures the harness builds its own Registry pointed
	// at an httptest server, which takes precedence.
	engOpts := engine.Options{}
	if rh != nil {
		engOpts.Refresolver = rh.Registry
	} else {
		engOpts.Refresolver = refresolver.NewDefaultRegistry(refresolver.Options{
			Allow:    []string{"git", "hub", "http", "bundles"},
			CacheDir: t.TempDir(),
		})
	}
	if remoteResolver != nil {
		// Mode B routing: every dispatch goes through the remote
		// driver, regardless of the resolver name in the Pipeline
		// (cluster, in this fixture's case). The validator's
		// RemoteResolverEnabled Option short-circuits the direct
		// allow-list check; here we just install the driver.
		engOpts.Refresolver.SetRemote(remoteResolver)
	}
	res, err := engine.New(cb, rep, engOpts).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: f.Pipeline, Params: pmap,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != f.WantStatus {
		// Include the Tekton reason + message and per-task outcomes so a
		// flake (or a real classification bug) is debuggable from CI
		// logs alone. Without this, every cluster-CI mismatch surfaces
		// as `status = X, want Y ()` with zero attribution.
		t.Errorf("status = %s, want %s (%s) reason=%q message=%q tasks=%s",
			res.Status, f.WantStatus, f.Description,
			res.Reason, res.Message, taskOutcomesString(res.Tasks))
	}
	// Cross-backend Pipeline.spec.results fidelity: the cluster path
	// reads pr.status.results from the Tekton verdict; the docker path
	// resolves locally. WantResults asserts both produce the same map.
	if f.WantResults != nil && !fixtures.ResultsEqual(res.Results, f.WantResults) {
		t.Errorf("results = %v, want %v (%s)", res.Results, f.WantResults, f.Description)
	}
	assertEventShape(t, f, cap.snapshot())
}

// taskOutcomesString renders the per-task map as a stable
// "name=status,name=status" string for one-line failure attribution.
// Empty input returns "{}" so callers always see something.
func taskOutcomesString(tasks map[string]engine.TaskOutcome) string {
	if len(tasks) == 0 {
		return "{}"
	}
	names := make([]string, 0, len(tasks))
	for n := range tasks {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		oc := tasks[n]
		if oc.Message != "" {
			parts = append(parts, n+"="+oc.Status+":"+oc.Message)
			continue
		}
		parts = append(parts, n+"="+oc.Status)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// assertEventShape checks the per-fixture invariants that fall out of
// the Track 2 #4 work — the JSON event stream from the cluster backend
// must match the docker stream's *shape* for these specific fixtures.
// Cross-backend fidelity is a checkable invariant, not just an
// aspiration.
func assertEventShape(t *testing.T, f fixtures.Fixture, events []reporter.Event) {
	t.Helper()
	switch f.Dir {
	case "retries":
		// retries/ has retries: 3 and the task succeeds on the third
		// attempt → cluster backend must report 2 task-retry events
		// and a task-end with Attempt: 3, matching docker.
		retries, end := 0, reporter.Event{}
		for _, e := range events {
			switch e.Kind {
			case reporter.EvtTaskRetry:
				retries++
			case reporter.EvtTaskEnd:
				if e.Task != "" {
					end = e
				}
			}
		}
		if retries != 2 {
			t.Errorf("retries fixture: task-retry events = %d, want 2", retries)
		}
		if end.Attempt != 3 {
			t.Errorf("retries fixture: task-end attempt = %d, want 3", end.Attempt)
		}
		if end.Status != "succeeded" {
			t.Errorf("retries fixture: task-end status = %q, want succeeded", end.Status)
		}
	case "timeout":
		// timeout/ must end with task-end status=timeout (Track 2 #2
		// invariant).
		var saw bool
		for _, e := range events {
			if e.Kind == reporter.EvtTaskEnd && e.Status == "timeout" {
				saw = true
			}
		}
		if !saw {
			t.Errorf("timeout fixture: no task-end with status=timeout in event stream")
		}
	}

	if len(f.WantEventFields) > 0 {
		// First event by kind.
		first := map[reporter.EventKind]reporter.Event{}
		for _, e := range events {
			if _, ok := first[e.Kind]; !ok {
				first[e.Kind] = e
			}
		}
		for kindStr, want := range f.WantEventFields {
			ev, ok := first[reporter.EventKind(kindStr)]
			if !ok {
				t.Errorf("WantEventFields: no %q event in captured stream", kindStr)
				continue
			}
			raw, _ := json.Marshal(ev)
			var got map[string]any
			_ = json.Unmarshal(raw, &got)
			for key, expected := range want {
				if fmt.Sprint(got[key]) != expected {
					t.Errorf("WantEventFields[%s][%s] = %v, want %q", kindStr, key, got[key], expected)
				}
			}
		}
	}
}

// preloadResolverRemoteTask creates an ephemeral namespace, applies
// testdata/e2e/resolver-remote/seed/greet.yaml into it, and returns the
// namespace name plus a dynamic.Interface pointed at the same kube the
// cluster harness uses. The returned namespace is registered for
// cleanup at end-of-test.
//
// Sequence (Track 1 #9 Phase 5 e2e):
//
//  1. Build a kube client + dynamic client from the harness's kubeconfig.
//  2. Create namespace `tkn-act-rr-<random>`.
//  3. Apply the seed Task into it via the dynamic Tasks GVR.
//  4. Return (ns, dyn). Cleanup deletes the namespace at t.Cleanup time.
//
// The Pipeline's `taskRef.resolver: cluster` is then dispatched through
// the Mode B remote driver: tkn-act submits a ResolutionRequest CRD to
// the same k3d, and Tekton's built-in cluster resolver controller (in
// release.yaml) reads the pre-loaded Task and writes it back on
// status.data.
func preloadResolverRemoteTask(t *testing.T, ctx context.Context, kubecfgPath string) (string, dynamic.Interface) {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubecfgPath)
	if err != nil {
		t.Fatalf("build kubeconfig: %v", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}

	// Namespace name unique per test invocation. Use the timestamp so
	// reruns within the same k3d cluster (uncommon but possible for
	// debug loops) don't collide.
	nsName := fmt.Sprintf("tkn-act-rr-%d", time.Now().UnixNano())
	if _, err := kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort namespace cleanup; the k3d itself is also
		// torn down at end-of-suite via the t.Cleanup on `cb`.
		_ = kube.CoreV1().Namespaces().Delete(context.Background(), nsName, metav1.DeleteOptions{})
	})

	// Read the seed Task and apply it via the dynamic client.
	seedPath := filepath.Join("..", "..", "testdata", "e2e", "resolver-remote", "seed", "greet.yaml")
	seedBytes, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	var taskObj map[string]interface{}
	if err := yaml.Unmarshal(seedBytes, &taskObj); err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	taskUnstr := &unstructured.Unstructured{Object: taskObj}
	taskUnstr.SetNamespace(nsName)
	gvrTasks := schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}
	if _, err := dyn.Resource(gvrTasks).Namespace(nsName).Create(ctx, taskUnstr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("apply seed Task into ns %s: %v", nsName, err)
	}
	return nsName, dyn
}
