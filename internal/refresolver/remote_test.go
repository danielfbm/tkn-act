package refresolver_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// resolutionRequestGVRs lists both apiVersions the remote driver knows
// how to talk to. v1beta1 is preferred; v1alpha1 is the documented
// fallback for older Tekton Resolution installs.
var (
	gvrV1Beta1 = schema.GroupVersionResource{
		Group: "resolution.tekton.dev", Version: "v1beta1", Resource: "resolutionrequests",
	}
	gvrV1Alpha1 = schema.GroupVersionResource{
		Group: "resolution.tekton.dev", Version: "v1alpha1", Resource: "resolutionrequests",
	}
)

// newFakeRemoteDynamic builds a fake dynamic client that knows how to
// list ResolutionRequests on both v1beta1 and v1alpha1 GVRs, so the
// remote driver's NoKindMatchError fallback is exercisable in unit
// tests.
func newFakeRemoteDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		gvrV1Beta1:  "ResolutionRequestList",
		gvrV1Alpha1: "ResolutionRequestList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, objs...)
}

// arrangeSucceededOnCreate makes the fake client populate the created
// ResolutionRequest with a Succeeded condition + base64(payload) on
// status.data, so a subsequent Watch event surfaces the success path.
//
// It returns the GVR the reactor matched, so tests can assert the
// remote driver picked the v1beta1 vs v1alpha1 path.
func arrangeSucceededOnCreate(t *testing.T, dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, payload []byte) *atomic.Int32 {
	t.Helper()
	var matched atomic.Int32
	dyn.PrependReactor("create", gvr.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		if action.GetResource() != gvr {
			return false, nil, nil
		}
		matched.Add(1)
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("create object not unstructured: %T", ca.GetObject())
		}
		// Pick a stable name (generateName is server-side; tests don't
		// have a server; the fake client requires a name).
		obj.SetName("rr-succeeded")
		// Populate Succeeded condition + data.
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{
				"type":   "Succeeded",
				"status": "True",
			},
		}, "status", "conditions")
		_ = unstructured.SetNestedField(obj.Object, base64.StdEncoding.EncodeToString(payload), "status", "data")
		return false, obj, nil // false: let the tracker accept the modified obj
	})
	return &matched
}

// arrangeFailedOnCreate makes the fake client populate the created
// ResolutionRequest with a Succeeded=False condition + reason/message,
// so subsequent watches surface the failure path.
func arrangeFailedOnCreate(t *testing.T, dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, reason, message string) {
	t.Helper()
	dyn.PrependReactor("create", gvr.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		if action.GetResource() != gvr {
			return false, nil, nil
		}
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("create object not unstructured: %T", ca.GetObject())
		}
		obj.SetName("rr-failed")
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{
				"type":    "Succeeded",
				"status":  "False",
				"reason":  reason,
				"message": message,
			},
		}, "status", "conditions")
		return false, obj, nil
	})
}

// arrangeNoKindMatch makes the fake client return apimeta.NoKindMatchError
// for the given GVR. Used to exercise the v1beta1 → v1alpha1 fallback.
func arrangeNoKindMatch(dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource) {
	dyn.PrependReactor("create", gvr.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != gvr {
			return false, nil, nil
		}
		return true, nil, &apimeta.NoKindMatchError{
			GroupKind:        schema.GroupKind{Group: gvr.Group, Kind: "ResolutionRequest"},
			SearchedVersions: []string{gvr.Version},
		}
	})
}

// trackDeletes records every Delete action targeting the given GVR. The
// remote driver MUST delete the ResolutionRequest in every code path
// (success, failure, ctx-cancel), and these counters pin that
// behavior.
func trackDeletes(dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource) *atomic.Int32 {
	var n atomic.Int32
	dyn.PrependReactor("delete", gvr.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != gvr {
			return false, nil, nil
		}
		n.Add(1)
		return false, nil, nil // let the tracker process the delete
	})
	return &n
}

// newRemoteResolverWithDynamic builds a RemoteResolver wired to the
// supplied dynamic client. Tests use this to bypass kubeconfig loading.
func newRemoteResolverWithDynamic(dyn dynamic.Interface, opts refresolver.RemoteResolverOptions) *refresolver.RemoteResolver {
	opts.Dynamic = dyn
	return refresolver.NewRemoteResolverFromOptions(opts)
}

// TestRemoteResolverHappyPath: status.conditions=Succeeded + status.data
// causes the resolver to return the decoded bytes.
func TestRemoteResolverHappyPath(t *testing.T) {
	payload := []byte("apiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: greet}\nspec:\n  steps:\n    - name: s\n      image: alpine:3\n")
	dyn := newFakeRemoteDynamic()
	matched := arrangeSucceededOnCreate(t, dyn, gvrV1Beta1, payload)
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{
		Namespace: "default",
		Timeout:   5 * time.Second,
		// PollInterval=0 falls back to the package default (small).
	})
	out, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "https://example", "revision": "main"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(out.Bytes) != string(payload) {
		t.Errorf("bytes mismatch: got %q, want %q", out.Bytes, payload)
	}
	if matched.Load() != 1 {
		t.Errorf("v1beta1 create reactor fired %d times, want 1", matched.Load())
	}
	if !strings.Contains(out.Source, "remote") {
		t.Errorf("Source = %q, want it to mention 'remote'", out.Source)
	}
}

// TestRemoteResolverFailedCondition: status.conditions[Succeeded=False]
// surfaces as a typed error containing reason+message.
func TestRemoteResolverFailedCondition(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeFailedOnCreate(t, dyn, gvrV1Beta1, "ResolutionFailed", "git: revision not found")
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{
		Namespace: "default",
		Timeout:   5 * time.Second,
	})
	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "x"},
	})
	if err == nil {
		t.Fatal("expected error from failed condition")
	}
	if !strings.Contains(err.Error(), "ResolutionFailed") || !strings.Contains(err.Error(), "git: revision not found") {
		t.Errorf("error %q does not contain reason+message", err)
	}
}

// TestRemoteResolverTimeout: when the fake never updates status, the
// resolver times out after Timeout.
func TestRemoteResolverTimeout(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	// Reactor that creates the RR but never adds Succeeded condition.
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		obj.SetName("rr-pending")
		// Empty status: no condition. Tracker accepts.
		return false, obj, nil
	})
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{
		Namespace:    "default",
		Timeout:      150 * time.Millisecond,
		PollInterval: 25 * time.Millisecond,
	})
	start := time.Now()
	_, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "b"}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") &&
		!strings.Contains(strings.ToLower(err.Error()), "deadline") {
		t.Errorf("error %q does not look like a timeout", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned in %s — must have actually waited the timeout window", elapsed)
	}
}

// TestRemoteResolverDeletesAfterSuccess: cleanup discipline pins that
// the ResolutionRequest is Deleted on the success path.
func TestRemoteResolverDeletesAfterSuccess(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeSucceededOnCreate(t, dyn, gvrV1Beta1, []byte("kind: Task\nmetadata: {name: x}\nspec:\n  steps: [{name: s, image: alpine:3}]\n"))
	deletes := trackDeletes(dyn, gvrV1Beta1)
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 5 * time.Second})
	if _, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := deletes.Load(); got < 1 {
		t.Errorf("delete reactor fired %d times, want >=1", got)
	}
}

// TestRemoteResolverDeletesAfterFailure: even on failure (Succeeded=False),
// the resolver MUST delete the ResolutionRequest.
func TestRemoteResolverDeletesAfterFailure(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeFailedOnCreate(t, dyn, gvrV1Beta1, "ResolutionFailed", "boom")
	deletes := trackDeletes(dyn, gvrV1Beta1)
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 5 * time.Second})
	_, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := deletes.Load(); got < 1 {
		t.Errorf("delete reactor fired %d times after failure, want >=1", got)
	}
}

// TestRemoteResolverDeletesOnContextCancel: SIGINT-like cancellation
// mid-resolution still triggers the deferred Delete via
// context.Background(). Pins that the cleanup race is closed (Critical 5).
func TestRemoteResolverDeletesOnContextCancel(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	// Create succeeds but never populates Succeeded — keeps the watch
	// loop spinning so we can cancel mid-flight.
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		obj.SetName("rr-pending-cancel")
		return false, obj, nil
	})
	deletes := trackDeletes(dyn, gvrV1Beta1)
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{
		Namespace:    "default",
		Timeout:      10 * time.Second,
		PollInterval: 25 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after Create has run.
	go func() {
		time.Sleep(75 * time.Millisecond)
		cancel()
	}()
	_, err := r.Resolve(ctx, refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// Give the deferred Delete a beat to run, since it uses
	// context.Background() and races the test goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if deletes.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := deletes.Load(); got < 1 {
		t.Errorf("delete reactor fired %d times after ctx-cancel; the deferred Delete via context.Background() must still run, want >=1", got)
	}
}

// TestRemoteResolverFallsBackToV1Alpha1OnNoKindMatch: NoKindMatchError on
// v1beta1 triggers a retry on v1alpha1. Both wire-shapes are identical
// for the fields we read (spec.params, status.conditions, status.data).
func TestRemoteResolverFallsBackToV1Alpha1OnNoKindMatch(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeNoKindMatch(dyn, gvrV1Beta1)
	matched := arrangeSucceededOnCreate(t, dyn, gvrV1Alpha1, []byte("kind: Task\nmetadata: {name: a}\nspec:\n  steps: [{name: s, image: alpine:3}]\n"))
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 5 * time.Second})
	if _, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if matched.Load() != 1 {
		t.Errorf("v1alpha1 create reactor fired %d times, want 1 (fallback path)", matched.Load())
	}
}

// TestRemoteResolverV1Alpha1OnlyCluster: same as above but the v1alpha1
// path returns Succeeded; the resolver returns the bytes without error.
// (Same scenario as the fallback test; this entry pins the success
// shape independently from the matched-counter assertion.)
func TestRemoteResolverV1Alpha1OnlyCluster(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeNoKindMatch(dyn, gvrV1Beta1)
	payload := []byte("kind: Task\nmetadata: {name: only-alpha}\nspec:\n  steps: [{name: s, image: alpine:3}]\n")
	arrangeSucceededOnCreate(t, dyn, gvrV1Alpha1, payload)
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 5 * time.Second})
	out, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(out.Bytes) != string(payload) {
		t.Errorf("bytes mismatch: got %q, want %q", out.Bytes, payload)
	}
}

// TestRemoteResolverParamsForwarded: the spec.params on the created
// ResolutionRequest must mirror the Request.Params (already substituted).
func TestRemoteResolverParamsForwarded(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	var seenParams atomic.Value // []map[string]interface{}
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		obj.SetName("rr-params")
		params, _, _ := unstructured.NestedSlice(obj.Object, "spec", "params")
		seenParams.Store(params)
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{
				"type":   "Succeeded",
				"status": "True",
			},
		}, "status", "conditions")
		_ = unstructured.SetNestedField(obj.Object, base64.StdEncoding.EncodeToString([]byte("kind: Task\nmetadata: {name: x}\nspec: {steps: [{name: s, image: alpine:3}]}\n")), "status", "data")
		return false, obj, nil
	})
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 2 * time.Second})
	if _, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "https://example.com", "revision": "main"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := seenParams.Load()
	if got == nil {
		t.Fatal("never observed spec.params on the created RR")
	}
	list := got.([]interface{})
	want := map[string]string{"url": "https://example.com", "revision": "main"}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		v, _ := m["value"].(string)
		if expected, ok := want[name]; ok {
			if v != expected {
				t.Errorf("param %q forwarded as %q, want %q", name, v, expected)
			}
			delete(want, name)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing params on submitted RR: %v", want)
	}
}

// TestRemoteResolverLabelsResolverType: the submitted RR carries
// metadata.labels[resolution.tekton.dev/type] = req.Resolver, mirroring
// the upstream Tekton Resolution controller's type-routing scheme.
func TestRemoteResolverLabelsResolverType(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	var seenType atomic.Value
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		obj.SetName("rr-labelled")
		labels := obj.GetLabels()
		if t, ok := labels["resolution.tekton.dev/type"]; ok {
			seenType.Store(t)
		}
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{"type": "Succeeded", "status": "True"},
		}, "status", "conditions")
		_ = unstructured.SetNestedField(obj.Object, base64.StdEncoding.EncodeToString([]byte("kind: Task\nmetadata: {name: x}\nspec: {steps: [{name: s, image: alpine:3}]}\n")), "status", "data")
		return false, obj, nil
	})
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 2 * time.Second})
	if _, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "my-private-hub", Params: map[string]string{"k": "v"}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if seenType.Load() != "my-private-hub" {
		t.Errorf("resolution.tekton.dev/type label = %v, want %q", seenType.Load(), "my-private-hub")
	}
}

// TestRemoteResolverInvalidBase64: status.data is not valid base64; the
// resolver surfaces a clear error instead of silently returning garbage.
func TestRemoteResolverInvalidBase64(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj := ca.GetObject().(*unstructured.Unstructured)
		obj.SetName("rr-bad-data")
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{"type": "Succeeded", "status": "True"},
		}, "status", "conditions")
		_ = unstructured.SetNestedField(obj.Object, "!!! not base64 !!!", "status", "data")
		return false, obj, nil
	})
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 2 * time.Second})
	_, err := r.Resolve(context.Background(), refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}})
	if err == nil {
		t.Fatal("expected base64 decode error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "base64") &&
		!strings.Contains(strings.ToLower(err.Error()), "decode") {
		t.Errorf("error %q does not mention base64/decode", err)
	}
}

// TestRemoteResolverName: sanity check on the registered name.
func TestRemoteResolverName(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	r := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: time.Second})
	if got := r.Name(); got != "remote" {
		t.Errorf("Name() = %q, want %q", got, "remote")
	}
}

// TestRegistryRoutesToRemoteWhenSet: when Registry has a Remote set,
// every dispatch (regardless of resolver name) goes through the remote
// driver instead of the direct allow-list. Pins the §6 design point that
// "Remote takes precedence over direct resolvers when non-nil."
func TestRegistryRoutesToRemoteWhenSet(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeSucceededOnCreate(t, dyn, gvrV1Beta1, []byte("kind: Task\nmetadata: {name: x}\nspec:\n  steps: [{name: s, image: alpine:3}]\n"))
	remote := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{
		Namespace: "default",
		Timeout:   2 * time.Second,
	})
	reg := refresolver.NewRegistry()
	reg.SetRemote(remote)
	// Even an arbitrary custom name dispatches successfully through remote.
	out, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "my-private-resolver",
		Params:   map[string]string{"a": "1"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(out.Bytes) == "" {
		t.Errorf("empty bytes from remote-routed resolve")
	}
}

// TestRegistryRemoteSkipsAllowList: when Remote is set, the direct-mode
// allow-list does NOT short-circuit dispatch (custom resolver names are
// the whole point of Mode B).
func TestRegistryRemoteSkipsAllowList(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	arrangeSucceededOnCreate(t, dyn, gvrV1Beta1, []byte("kind: Task\nmetadata: {name: x}\nspec:\n  steps: [{name: s, image: alpine:3}]\n"))
	remote := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 2 * time.Second})
	reg := refresolver.NewRegistry()
	reg.SetAllow([]string{"git", "hub"}) // a restrictive allow-list…
	reg.SetRemote(remote)                // …that Remote bypasses.
	if _, err := reg.Resolve(context.Background(), refresolver.Request{Resolver: "ghpr-private", Params: map[string]string{}}); err != nil {
		if errors.Is(err, refresolver.ErrResolverNotAllowed) {
			t.Fatalf("Remote is set but allow-list still gated dispatch: %v", err)
		}
		t.Fatalf("resolve: %v", err)
	}
}

// TestRegistryRemoteCachesPerRun: identical Request still hits the
// per-run cache when Remote is the dispatch path. Pins that Remote
// participates in the same per-run cache layer as direct resolvers.
func TestRegistryRemoteCachesPerRun(t *testing.T) {
	dyn := newFakeRemoteDynamic()
	var creates atomic.Int32
	dyn.PrependReactor("create", gvrV1Beta1.Resource, func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		creates.Add(1)
		obj, ok := ca.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		obj.SetName("rr-cached")
		_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
			map[string]interface{}{"type": "Succeeded", "status": "True"},
		}, "status", "conditions")
		_ = unstructured.SetNestedField(obj.Object, base64.StdEncoding.EncodeToString([]byte("kind: Task\nmetadata: {name: x}\nspec: {steps: [{name: s, image: alpine:3}]}\n")), "status", "data")
		return false, obj, nil
	})
	remote := newRemoteResolverWithDynamic(dyn, refresolver.RemoteResolverOptions{Namespace: "default", Timeout: 2 * time.Second})
	reg := refresolver.NewRegistry()
	reg.SetRemote(remote)
	req := refresolver.Request{Resolver: "remote-x", Params: map[string]string{"k": "v"}}
	for i := 0; i < 3; i++ {
		if _, err := reg.Resolve(context.Background(), req); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if got := creates.Load(); got != 1 {
		t.Errorf("create fired %d times for identical Request, want 1 (per-run cache)", got)
	}
}

// suppress unused warnings on imports when tests are pruned.
var (
	_ = clienttesting.NewRootDeleteAction
	_ = watch.Added
	_ = metav1.GetOptions{}
)
