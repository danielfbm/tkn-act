package fixtures

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// ResolverHarness wires up an httptest.Server + a refresolver.Registry
// for a fixture flagged Resolver=="http"|"hub". Both e2e harnesses
// (docker + cluster) use this so the resolver dispatch path is exercised
// identically across backends. The returned struct's Cleanup must be
// called after the run.
type ResolverHarness struct {
	Server   *httptest.Server
	Registry *refresolver.Registry
	// ExtraParam is a single (name, value) tuple the caller MUST add to
	// the Pipeline's run params before invoking RunPipeline. For "http"
	// fixtures it carries the test server's base URL via
	// fixture-server-url; for "hub" fixtures the resolver targets the
	// server through Registry construction so this field is empty.
	ExtraParamName  string
	ExtraParamValue string
}

// Close shuts down the test server.
func (h *ResolverHarness) Close() {
	if h != nil && h.Server != nil {
		h.Server.Close()
	}
}

// NewResolverHarness sets up the test server + registry for the given
// fixture's Resolver string. fixtureDir is the absolute path to
// testdata/e2e/<dir>; the served/ subdirectory is the corpus the test
// server returns. Empty Resolver returns nil (harness inactive).
func NewResolverHarness(fixtureDir, resolverName string) (*ResolverHarness, error) {
	servedDir := filepath.Join(fixtureDir, "served")
	switch resolverName {
	case "":
		return nil, nil
	case "http":
		srv := newServedFileServer(servedDir)
		reg := refresolver.NewDefaultRegistry(refresolver.Options{
			Allow: []string{"http"},
		})
		return &ResolverHarness{
			Server:          srv,
			Registry:        reg,
			ExtraParamName:  "fixture-server-url",
			ExtraParamValue: srv.URL,
		}, nil
	case "hub":
		srv := newHubFakeServer(servedDir)
		// Build a Registry whose hub resolver targets the test server.
		// We can't tunnel the BaseURL through resolver.params (the hub
		// API doesn't define a baseURL param); instead we register a
		// hub resolver instance pointed at the test server.
		reg := refresolver.NewRegistry()
		reg.Register(refresolver.NewInlineResolver())
		reg.Register(refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL}))
		reg.SetAllow([]string{"hub", "inline"})
		return &ResolverHarness{
			Server:          srv,
			Registry:        reg,
			ExtraParamName:  "fixture-server-url",
			ExtraParamValue: srv.URL, // not consumed; kept for symmetry
		}, nil
	default:
		return nil, fmt.Errorf("fixtures.NewResolverHarness: unknown Resolver %q", resolverName)
	}
}

// newServedFileServer returns an httptest.Server that serves files from
// dir at their basename path (e.g. served/task.yaml at /task.yaml).
func newServedFileServer(dir string) *httptest.Server {
	mux := http.NewServeMux()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) {
			body, err := os.ReadFile(path)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/yaml")
			_, _ = w.Write(body)
		})
	}
	return httptest.NewServer(mux)
}

// newHubFakeServer impersonates the Tekton Hub v1 resource endpoint:
//
//	GET /v1/resource/<catalog>/<kind>/<name>/<version>/yaml
//
// It serves files from `<dir>/<name>.yaml`, ignoring catalog / kind /
// version for fixture simplicity (the resolver dispatch pieces still
// run, which is what we're testing).
func newHubFakeServer(dir string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path shape: /v1/resource/<catalog>/<kind>/<name>/<version>/yaml.
		// After splitting on "/" with the leading slash trimmed, that's
		// seven segments: v1, resource, catalog, kind, name, version, yaml.
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 7 || parts[0] != "v1" || parts[1] != "resource" || parts[6] != "yaml" {
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
			return
		}
		name := parts[4]
		body, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
		if err != nil {
			http.Error(w, "not found: "+name, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write(body)
	}))
}
