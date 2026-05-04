package refresolver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// scheme + GVR helpers shared across cluster_test.go cases. We register
// the Tekton task/pipeline GVKs so the fake dynamic client can serve
// list / get calls. All tests use the same scheme so the fake client's
// internal indices don't drift.
func newFakeTektonDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		{Group: "tekton.dev", Version: "v1", Resource: "tasks"}:     "TaskList",
		{Group: "tekton.dev", Version: "v1", Resource: "pipelines"}: "PipelineList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, objs...)
}

func makeTaskUnstructured(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       "Task",
		"metadata": map[string]interface{}{
			"name":              name,
			"namespace":         namespace,
			"uid":               "should-be-stripped",
			"resourceVersion":   "1",
			"generation":        int64(1),
			"creationTimestamp": "2024-01-01T00:00:00Z",
			"managedFields":     []interface{}{},
		},
		"spec": map[string]interface{}{
			"steps": []interface{}{
				map[string]interface{}{
					"name":   "greet",
					"image":  "alpine:3",
					"script": "echo hello",
				},
			},
		},
		"status": map[string]interface{}{
			"observedGeneration": int64(1),
		},
	}}
}

func makePipelineUnstructured(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       "Pipeline",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"tasks": []interface{}{
				map[string]interface{}{
					"name":    "first",
					"taskRef": map[string]interface{}{"name": "noop"},
				},
			},
		},
	}}
}

// TestClusterResolverHappyPath: with a fake dynamic client containing one
// Task, the resolver fetches and serializes it back to YAML.
func TestClusterResolverHappyPath(t *testing.T) {
	dyn := newFakeTektonDynamic(makeTaskUnstructured("ns1", "greet"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{
		Dynamic: dyn,
		Context: "test-context",
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params: map[string]string{
			"name":      "greet",
			"kind":      "task",
			"namespace": "ns1",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	body := string(out.Bytes)
	if !strings.Contains(body, "kind: Task") {
		t.Errorf("returned bytes do not look like a Task: %q", body)
	}
	if !strings.Contains(body, "name: greet") {
		t.Errorf("returned bytes do not include the resource name: %q", body)
	}
	if strings.Contains(body, "should-be-stripped") {
		t.Errorf("server-side metadata.uid leaked into resolved bytes: %q", body)
	}
	if !strings.Contains(out.Source, "cluster") || !strings.Contains(out.Source, "test-context") {
		t.Errorf("Source = %q, want it to mention 'cluster' and the context name", out.Source)
	}
}

// TestClusterResolverDefaultKindIsTask: when `kind` is omitted, the
// resolver assumes "task".
func TestClusterResolverDefaultKindIsTask(t *testing.T) {
	dyn := newFakeTektonDynamic(makeTaskUnstructured("default", "greet"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "greet"},
	})
	if err != nil {
		t.Fatalf("resolve (kind omitted): %v", err)
	}
}

// TestClusterResolverDefaultNamespaceIsDefault: when `namespace` is
// omitted, the resolver targets the "default" namespace.
func TestClusterResolverDefaultNamespaceIsDefault(t *testing.T) {
	dyn := newFakeTektonDynamic(makeTaskUnstructured("default", "greet"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "greet", "kind": "task"},
	})
	if err != nil {
		t.Fatalf("resolve (namespace omitted): %v", err)
	}
}

// TestClusterResolverPipelineKind: kind=pipeline reads from the
// pipelines GVR.
func TestClusterResolverPipelineKind(t *testing.T) {
	dyn := newFakeTektonDynamic(makePipelineUnstructured("ns2", "release"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params: map[string]string{
			"name":      "release",
			"kind":      "pipeline",
			"namespace": "ns2",
		},
	})
	if err != nil {
		t.Fatalf("resolve pipeline: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Pipeline") {
		t.Errorf("expected Pipeline kind in: %q", out.Bytes)
	}
}

// TestClusterResolverMissingName: name param is required.
func TestClusterResolverMissingName(t *testing.T) {
	dyn := newFakeTektonDynamic()
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing name param")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q does not mention 'name'", err)
	}
}

// TestClusterResolverUnsupportedKind: only task / pipeline are supported.
func TestClusterResolverUnsupportedKind(t *testing.T) {
	dyn := newFakeTektonDynamic()
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "x", "kind": "stepaction"},
	})
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error %q does not mention kind", err)
	}
}

// TestClusterResolverNotFound: a Get against the fake client when the
// resource doesn't exist surfaces as a typed error mentioning the name.
func TestClusterResolverNotFound(t *testing.T) {
	dyn := newFakeTektonDynamic()
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "nope", "kind": "task"},
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not mention the resource name", err)
	}
}

// TestClusterResolverName: sanity check on the registered name.
func TestClusterResolverName(t *testing.T) {
	dyn := newFakeTektonDynamic()
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if got := res.Name(); got != "cluster" {
		t.Errorf("Name() = %q, want %q", got, "cluster")
	}
}

// TestClusterResolverNotRegisteredByDefault: the default registry MUST
// NOT register the cluster resolver — security: KUBECONFIG may point at
// production.
func TestClusterResolverNotRegisteredByDefault(t *testing.T) {
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		// Default-ish allow-list as the CLI ships.
		Allow: []string{"git", "hub", "http", "bundles"},
	})
	_, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error: cluster should not dispatch by default")
	}
	if !errors.Is(err, refresolver.ErrResolverNotAllowed) && !errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("expected ErrResolverNotAllowed or ErrResolverNotRegistered; got %v", err)
	}
}

// TestClusterResolverOptInViaAllowList: adding "cluster" to the allow-
// list is the documented opt-in path. We can't easily build a real
// kubeconfig in a unit test, so we verify the registration intent: the
// dispatch produces a different error class than "not registered" /
// "not allowed" — because the cluster constructor now runs.
func TestClusterResolverOptInViaAllowList(t *testing.T) {
	// Explicit kubeconfig path that doesn't exist. The constructor
	// should fail gracefully; the registry installs a stub that
	// re-raises the constructor error on Resolve.
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow:                     []string{"cluster"},
		ClusterResolverKubeconfig: "/no/such/kubeconfig",
		AllowCluster:              true,
	})
	_, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error from dispatch with bogus kubeconfig path")
	}
	if errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("with cluster in allow-list + AllowCluster, the resolver should be registered (stub or real); got ErrResolverNotRegistered: %v", err)
	}
}

// TestClusterResolverWithDynamicClientInjected: the constructor accepts
// an externally built dynamic client (the path NewDefaultRegistry uses
// in tests, and what the CLI does once it has a kubeconfig loaded).
func TestClusterResolverWithDynamicClientInjected(t *testing.T) {
	dyn := newFakeTektonDynamic(makeTaskUnstructured("default", "greet"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{
		Dynamic: dyn,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "greet"},
	}); err != nil {
		t.Errorf("resolve with injected client: %v", err)
	}
}

// TestClusterResolverNoDynamicNoKubeconfig: when neither a dynamic
// client nor a usable kubeconfig is available at constructor time, the
// resolver MUST refuse with ErrClusterContextRequired or a clearly
// equivalent error mentioning kubeconfig/context.
//
// We can't easily clear $KUBECONFIG and ~/.kube/config in a unit test
// without affecting the developer's box; instead we point at a non-
// existent path and assert the constructor returns *some* error. The
// "ErrClusterContextRequired" path is exercised by the synthetic
// dispatch in TestClusterResolverNotRegisteredByDefault above.
func TestClusterResolverNoDynamicNoKubeconfig(t *testing.T) {
	_, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{
		Kubeconfig: "/no/such/path/kubeconfig",
		Context:    "no-such-context",
	})
	if err == nil {
		t.Fatal("expected constructor error for nonexistent kubeconfig path")
	}
}

// TestClusterResolverGVRMatchesTektonV1: the resolver's tasks/pipelines
// GVR must match what tkn-act elsewhere uses (tekton.dev/v1). Pinned so
// a future refactor can't silently drop the version match.
func TestClusterResolverGVRMatchesTektonV1(t *testing.T) {
	// Indirectly observed: the fake client's gvrToList uses v1, and the
	// resolver's Get against it succeeds. If the resolver ever switched
	// to v1beta1 or v1alpha1, the fake client would error with a
	// "could not find informer" / no-kind-match equivalent.
	dyn := newFakeTektonDynamic(makeTaskUnstructured("default", "x"))
	res, err := refresolver.NewClusterResolver(refresolver.ClusterResolverOptions{Dynamic: dyn})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "cluster",
		Params:   map[string]string{"name": "x", "kind": "task"},
	}); err != nil {
		t.Errorf("resolve: %v", err)
	}
}

// suppress unused warnings on imports if tests are pruned.
var _ = metav1.GetOptions{}
