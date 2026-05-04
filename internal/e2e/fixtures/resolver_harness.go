package fixtures

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// ResolverHarness wires up an httptest.Server + a refresolver.Registry
// for a fixture flagged Resolver=="http"|"hub"|"bundles". Both e2e
// harnesses (docker + cluster) use this so the resolver dispatch path
// is exercised identically across backends. The returned struct's
// Cleanup must be called after the run.
type ResolverHarness struct {
	Server   *httptest.Server
	Registry *refresolver.Registry
	// ExtraParam is a single (name, value) tuple the caller MUST add to
	// the Pipeline's run params before invoking RunPipeline. For "http"
	// fixtures it carries the test server's base URL via
	// fixture-server-url; for "hub" fixtures the resolver targets the
	// server through Registry construction so this field is empty.
	// For "bundles" fixtures it carries the OCI ref the test
	// registry serves at, so the Pipeline's $(params.bundle-ref)
	// resolves to a working reference.
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
	case "bundles":
		// Spin up an in-memory OCI registry, build a Tekton bundle from
		// `served/*.yaml`, push it as <host>/tkn-act/test-bundle:v1,
		// and configure a Registry with the bundles resolver enabled
		// against insecure HTTP (loopback test registry).
		srv := httptest.NewServer(registry.New())
		ref, err := buildAndPushTektonBundle(srv.URL, servedDir)
		if err != nil {
			srv.Close()
			return nil, fmt.Errorf("fixtures.NewResolverHarness: building bundle: %w", err)
		}
		reg := refresolver.NewDefaultRegistry(refresolver.Options{
			Allow:             []string{"bundles"},
			AllowInsecureHTTP: true,
		})
		return &ResolverHarness{
			Server:          srv,
			Registry:        reg,
			ExtraParamName:  "bundle-ref",
			ExtraParamValue: ref,
		}, nil
	default:
		return nil, fmt.Errorf("fixtures.NewResolverHarness: unknown Resolver %q", resolverName)
	}
}

// buildAndPushTektonBundle reads every *.yaml file under servedDir,
// packages each as a Tekton bundle layer (one tar entry per layer,
// annotated with dev.tekton.image.{name,kind,apiVersion}), and pushes
// the assembled OCI image to the loopback test registry at srvURL.
//
// The function returns the full image reference (host:port/repo:tag).
// The bundle resource names are taken from the YAML's metadata.name (we
// scan a tiny `name:` field heuristically — the harness fixtures keep
// the metadata block at column 0 so the regex below is fine).
//
// Callers feed this ref into the Pipeline's $(params.bundle-ref).
func buildAndPushTektonBundle(srvURL, servedDir string) (string, error) {
	entries, err := os.ReadDir(servedDir)
	if err != nil {
		return "", fmt.Errorf("read served dir %s: %w", servedDir, err)
	}

	adds := make([]mutate.Addendum, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(servedDir, e.Name())
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return "", fmt.Errorf("read %s: %w", path, rerr)
		}
		name, kind := extractTektonNameKindFromYAML(body)
		if name == "" {
			// Skip non-Tekton files quietly.
			continue
		}
		layerBytes, terr := tarSingleFile(name+".yaml", body)
		if terr != nil {
			return "", terr
		}
		captured := layerBytes
		layer, lerr := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(captured)), nil
		})
		if lerr != nil {
			return "", lerr
		}
		adds = append(adds, mutate.Addendum{
			Layer: layer,
			Annotations: map[string]string{
				"dev.tekton.image.name":       name,
				"dev.tekton.image.kind":       kind,
				"dev.tekton.image.apiVersion": "tekton.dev/v1",
			},
		})
	}
	img, err := mutate.Append(empty.Image, adds...)
	if err != nil {
		return "", fmt.Errorf("mutate.Append: %w", err)
	}
	u, err := url.Parse(srvURL)
	if err != nil {
		return "", fmt.Errorf("parse srvURL %s: %w", srvURL, err)
	}
	ref := u.Host + "/tkn-act/test-bundle:v1"
	if err := crane.Push(img, ref); err != nil {
		return "", fmt.Errorf("crane.Push: %w", err)
	}
	return ref, nil
}

// tarSingleFile writes one tar entry into a buffer and returns the
// bytes. Used to package a YAML file as a layer body.
func tarSingleFile(name string, contents []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(contents)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(contents); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// extractTektonNameKindFromYAML scans the YAML for the metadata.name
// and the top-level kind so the bundle harness can label its layers
// correctly. The fixture YAMLs follow the canonical Tekton layout —
// `kind: Task` / `kind: Pipeline` at column 0, `metadata:\n  name: X`
// indented two spaces — so a tiny line-by-line scan suffices.
//
// Returns ("", "") if the file isn't a recognized Tekton resource.
func extractTektonNameKindFromYAML(body []byte) (name, kind string) {
	lines := strings.Split(string(body), "\n")
	inMeta := false
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trim, "kind:") && !strings.HasPrefix(l, " "):
			k := strings.TrimSpace(strings.TrimPrefix(trim, "kind:"))
			switch strings.ToLower(k) {
			case "task":
				kind = "task"
			case "pipeline":
				kind = "pipeline"
			}
		case strings.HasPrefix(l, "metadata:"):
			inMeta = true
		case inMeta && strings.HasPrefix(strings.TrimLeft(l, " \t"), "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(l, " \t"), "name:"))
			// Only accept the first metadata.name we see (the top-level one).
			inMeta = false
		case len(l) > 0 && l[0] != ' ' && l[0] != '\t':
			// Top-level key other than metadata: ends the metadata block.
			if !strings.HasPrefix(l, "metadata:") {
				inMeta = false
			}
		}
	}
	return name, kind
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
