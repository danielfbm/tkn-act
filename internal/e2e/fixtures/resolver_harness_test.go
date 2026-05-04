package fixtures

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// TestNewResolverHarnessHTTPServesFiles: the http harness serves files
// from the served/ subdir at their basename path, and Registry-dispatch
// for "http" returns the bytes verbatim.
func TestNewResolverHarnessHTTPServesFiles(t *testing.T) {
	dir := t.TempDir()
	served := filepath.Join(dir, "served")
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("apiVersion: tekton.dev/v1\nkind: Task\n")
	if err := os.WriteFile(filepath.Join(served, "task.yaml"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := NewResolverHarness(dir, "http")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer h.Close()

	if h.Server == nil {
		t.Fatal("expected non-nil Server")
	}
	if h.Registry == nil {
		t.Fatal("expected non-nil Registry")
	}
	if h.ExtraParamName != "fixture-server-url" {
		t.Errorf("ExtraParamName = %q, want fixture-server-url", h.ExtraParamName)
	}
	if !strings.HasPrefix(h.ExtraParamValue, "http://") {
		t.Errorf("ExtraParamValue = %q, expected http:// URL", h.ExtraParamValue)
	}

	resp, err := http.Get(h.Server.URL + "/task.yaml")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestNewResolverHarnessHTTPDispatchesViaRegistry: the registry built by
// the harness routes Resolver=="http" requests to the test server.
func TestNewResolverHarnessHTTPDispatchesViaRegistry(t *testing.T) {
	dir := t.TempDir()
	served := filepath.Join(dir, "served")
	_ = os.MkdirAll(served, 0o755)
	_ = os.WriteFile(filepath.Join(served, "task.yaml"),
		[]byte("apiVersion: tekton.dev/v1\nkind: Task\n"), 0o644)

	h, err := NewResolverHarness(dir, "http")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer h.Close()

	out, err := h.Registry.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": h.Server.URL + "/task.yaml"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") {
		t.Errorf("returned bytes do not include 'kind: Task': %q", string(out.Bytes))
	}
}

// TestNewResolverHarnessHubMimicsTektonHubAPI: the hub harness handles
// the Tekton Hub v1 path shape and serves <name>.yaml from served/.
func TestNewResolverHarnessHubMimicsTektonHubAPI(t *testing.T) {
	dir := t.TempDir()
	served := filepath.Join(dir, "served")
	_ = os.MkdirAll(served, 0o755)
	_ = os.WriteFile(filepath.Join(served, "greet.yaml"),
		[]byte("apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: greet\n"), 0o644)

	h, err := NewResolverHarness(dir, "hub")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer h.Close()

	out, err := h.Registry.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params: map[string]string{
			"name":    "greet",
			"version": "0.1",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") {
		t.Errorf("returned bytes wrong: %q", string(out.Bytes))
	}

	// 404 path: name not in served/.
	_, err = h.Registry.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "nope", "version": "0.1"},
	})
	if err == nil {
		t.Fatal("expected 404 for unknown name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not mention 'not found'", err)
	}
}

// TestNewResolverHarnessBundlesPushesAndDispatches: the bundles harness
// builds a Tekton bundle from served/*.yaml, pushes it to its in-memory
// OCI registry, and the Registry's bundles resolver fetches the named
// resource back out.
func TestNewResolverHarnessBundlesPushesAndDispatches(t *testing.T) {
	dir := t.TempDir()
	served := filepath.Join(dir, "served")
	if err := os.MkdirAll(served, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(served, "bundle-task.yaml"),
		[]byte("apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: bundle-task\nspec:\n  steps:\n    - name: x\n      image: alpine:3\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	h, err := NewResolverHarness(dir, "bundles")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer h.Close()

	if h.Server == nil {
		t.Fatal("expected non-nil Server")
	}
	if h.Registry == nil {
		t.Fatal("expected non-nil Registry")
	}
	if h.ExtraParamName != "bundle-ref" {
		t.Errorf("ExtraParamName = %q, want bundle-ref", h.ExtraParamName)
	}
	if h.ExtraParamValue == "" {
		t.Error("expected non-empty bundle-ref")
	}

	out, err := h.Registry.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": h.ExtraParamValue,
			"name":   "bundle-task",
			"kind":   "task",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") {
		t.Errorf("returned bytes do not include 'kind: Task': %q", string(out.Bytes))
	}
	if !strings.Contains(string(out.Bytes), "bundle-task") {
		t.Errorf("returned bytes missing the resource name: %q", string(out.Bytes))
	}
}

// TestExtractTektonNameKindFromYAML: the helper used by the bundles
// harness to decide which (name, kind) annotations to attach to each
// layer.
func TestExtractTektonNameKindFromYAML(t *testing.T) {
	for _, c := range []struct {
		body                 string
		wantName, wantKind   string
	}{
		{
			body:     "apiVersion: tekton.dev/v1\nkind: Task\nmetadata:\n  name: foo\nspec: {}\n",
			wantName: "foo", wantKind: "task",
		},
		{
			body:     "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: bar\n",
			wantName: "bar", wantKind: "pipeline",
		},
		{
			body:     "kind: ConfigMap\nmetadata:\n  name: baz\n",
			wantName: "baz", wantKind: "", // ConfigMap is not a Tekton resource type
		},
	} {
		gotName, gotKind := extractTektonNameKindFromYAML([]byte(c.body))
		if gotName != c.wantName {
			t.Errorf("body=%q: name = %q, want %q", c.body, gotName, c.wantName)
		}
		if gotKind != c.wantKind {
			t.Errorf("body=%q: kind = %q, want %q", c.body, gotKind, c.wantKind)
		}
	}
}

// TestNewResolverHarnessUnknownReturnsError: an unsupported Resolver
// fails loudly so a typo doesn't silently skip the fixture.
func TestNewResolverHarnessUnknownReturnsError(t *testing.T) {
	if _, err := NewResolverHarness(t.TempDir(), "made-up"); err == nil {
		t.Fatal("expected error for unknown Resolver")
	}
}

// TestNewResolverHarnessEmptyIsInactive: empty Resolver returns nil so
// non-resolver fixtures are unaffected.
func TestNewResolverHarnessEmptyIsInactive(t *testing.T) {
	h, err := NewResolverHarness(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Errorf("expected nil harness for empty Resolver, got %v", h)
	}
}
