package refresolver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

const sampleTaskYAML = `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: golang-build
spec:
  steps:
    - name: build
      image: golang:1.21
      script: go build ./...
`

// TestHubResolverHappyPath: hub resolver hits the documented v1 endpoint
// shape (/v1/resource/<catalog>/<kind>/<name>/<version>/yaml) and returns
// the raw bytes. The Hub's actual response is the YAML itself, content-
// type text/yaml or application/octet-stream — we accept whatever the
// server sends.
func TestHubResolverHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/v1/resource/tekton/task/golang-build/0.4/yaml"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()

	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params: map[string]string{
			"name":    "golang-build",
			"version": "0.4",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") {
		t.Errorf("returned bytes do not look like a Task: %q", string(out.Bytes))
	}
	if out.Source == "" {
		t.Errorf("Source should be populated")
	}
}

// TestHubResolverDefaultsKindAndCatalog: omitting `kind` should default to
// `task`; omitting `catalog` should default to `tekton`; omitting `version`
// should default to `latest`.
func TestHubResolverDefaultsKindAndCatalog(t *testing.T) {
	gotPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "/v1/resource/tekton/task/x/latest/yaml"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

// TestHubResolverHonorsExplicitCatalogAndKind: explicit catalog and kind
// override defaults.
func TestHubResolverHonorsExplicitCatalogAndKind(t *testing.T) {
	gotPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata: {name: p}\nspec: {tasks: []}\n"))
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params: map[string]string{
			"name":    "p",
			"version": "1.0",
			"kind":    "pipeline",
			"catalog": "private",
		},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "/v1/resource/private/pipeline/p/1.0/yaml"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

// TestHubResolverMissingNameFailsLoudly: `name` is required.
func TestHubResolverMissingNameFailsLoudly(t *testing.T) {
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: "https://api.hub.tekton.dev"})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q does not mention 'name'", err)
	}
}

// TestHubResolverInsecureBaseURLRefused: hub requires HTTPS regardless of
// the global --resolver-allow-insecure-http flag (that flag only applies
// to the http resolver, per spec).
func TestHubResolverInsecureBaseURLRefused(t *testing.T) {
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: "http://api.hub.tekton.dev"})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for plain-http hub URL")
	}
	if !strings.Contains(err.Error(), "https") && !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error should mention https; got %q", err)
	}
}

// TestHubResolver404HelpfulHint: 404 from the hub surfaces as an error
// that mentions the canonical "not found" diagnostic.
func TestHubResolver404HelpfulHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "missing", "version": "0.1"},
	})
	if err == nil {
		t.Fatal("expected 404 to surface as error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found'; got %q", err)
	}
	// Should include the (resolver, name, version) context.
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should include resource name; got %q", err)
	}
}

// TestHubResolver5xxRetriesOnce: a single 5xx is retried; a second 5xx
// fails. We assert via call counter.
func TestHubResolver5xxRetriesOnce(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x", "version": "0.1"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(out.Bytes) == "" {
		t.Errorf("expected bytes after retry")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (one retry), got %d", got)
	}
}

// TestHubResolver5xxGivesUp: persistent 5xx returns an error after the
// retry budget is exhausted.
func TestHubResolver5xxGivesUp(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x", "version": "0.1"},
	})
	if err == nil {
		t.Fatal("expected error after persistent 5xx")
	}
	// Budget = 1 retry → 2 total calls.
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
}

// TestHubResolverBearerToken: when HubOptions.Token is set, the resolver
// sends Authorization: Bearer <token>.
func TestHubResolverBearerToken(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL, Token: "abc123"})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x"},
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer abc123")
	}
}

// TestHubResolverNotRegisteredByDefaultRegistry: confirms hub is wired
// into NewDefaultRegistry's allow-list and dispatch table after Phase 3.
func TestHubResolverRegisteredInDefaultRegistry(t *testing.T) {
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow: []string{"hub"},
	})
	// The default registry points at the real hub; we just want to
	// confirm dispatch happens — i.e., we expect either success or a
	// network/4xx-shaped error, NEVER ErrResolverNotRegistered. Use a
	// short context to keep the test quick; we don't care if the hub
	// is actually reachable from the CI runner.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := reg.Resolve(ctx, refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "this-task-does-not-exist-on-real-hub", "version": "0.0.0"},
	})
	if err == nil {
		return
	}
	if errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("hub should be registered by default; got ErrResolverNotRegistered: %v", err)
	}
}

// TestHubResolverName: sanity check on the Name() identifier.
func TestHubResolverName(t *testing.T) {
	if got := refresolver.NewHubResolver(refresolver.HubOptions{}).Name(); got != "hub" {
		t.Errorf("Name() = %q, want %q", got, "hub")
	}
}
