package refresolver_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// bundleSpec describes one Tekton resource we want to embed in a bundle.
type bundleSpec struct {
	kind       string // "task" or "pipeline"
	apiVersion string
	yaml       string
}

// bundleFromYAMLs builds an in-memory OCI image whose layers each carry a
// single Tekton resource as a YAML file, annotated per the canonical
// Tekton bundle spec:
//
//	dev.tekton.image.name        -> resource name
//	dev.tekton.image.kind        -> "task"/"pipeline"
//	dev.tekton.image.apiVersion  -> e.g. "tekton.dev/v1"
//
// The map key is the metadata.name; the map value carries the kind /
// apiVersion / YAML payload.
func bundleFromYAMLs(specs map[string]bundleSpec) (v1.Image, error) {
	adds := make([]mutate.Addendum, 0, len(specs))
	for name, spec := range specs {
		layerBytes, err := tarSingleFile(name+".yaml", []byte(spec.yaml))
		if err != nil {
			return nil, err
		}
		body := layerBytes // capture
		layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		})
		if err != nil {
			return nil, err
		}
		adds = append(adds, mutate.Addendum{
			Layer: layer,
			Annotations: map[string]string{
				"dev.tekton.image.name":       name,
				"dev.tekton.image.kind":       spec.kind,
				"dev.tekton.image.apiVersion": spec.apiVersion,
			},
		})
	}
	return mutate.Append(empty.Image, adds...)
}

// tarSingleFile writes one tar entry (name -> contents) into a buffer
// and returns the bytes. Used to package a YAML file as a layer body.
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

const sampleTektonTaskYAML = `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: greet-bundle
spec:
  steps:
    - name: greet
      image: alpine:3
      script: echo bundle
`

// TestBundlesResolverHappyPath: serves a Tekton bundle from an in-memory
// OCI registry and resolves the named Task out of it.
func TestBundlesResolverHappyPath(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := bundleFromYAMLs(map[string]bundleSpec{
		"greet-bundle": {
			kind:       "task",
			apiVersion: "tekton.dev/v1",
			yaml:       sampleTektonTaskYAML,
		},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	ref := host + "/tkn-act/test-bundle:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("crane.Push: %v", err)
	}

	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{AllowInsecureHTTP: true})
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": ref,
			"name":   "greet-bundle",
			"kind":   "task",
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(out.Bytes), "kind: Task") || !strings.Contains(string(out.Bytes), "greet-bundle") {
		t.Errorf("returned bytes do not match the embedded Task: %q", string(out.Bytes))
	}
	if !strings.HasPrefix(out.Source, "bundles: ") {
		t.Errorf("Source = %q, want prefix 'bundles: '", out.Source)
	}
}

// TestBundlesResolverMissingResource: the bundle exists but contains no
// resource by the requested name.
func TestBundlesResolverMissingResource(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := bundleFromYAMLs(map[string]bundleSpec{
		"greet-bundle": {
			kind:       "task",
			apiVersion: "tekton.dev/v1",
			yaml:       sampleTektonTaskYAML,
		},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	ref := host + "/tkn-act/test-bundle:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("crane.Push: %v", err)
	}

	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{AllowInsecureHTTP: true})
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": ref,
			"name":   "no-such-task",
			"kind":   "task",
		},
	})
	if err == nil {
		t.Fatal("expected error for missing resource name")
	}
	if !strings.Contains(err.Error(), "no-such-task") {
		t.Errorf("error %q does not mention the missing name", err)
	}
}

// TestBundlesResolverDefaultKindIsTask: when `kind` is omitted, the
// resolver assumes "task" per the documented default.
func TestBundlesResolverDefaultKindIsTask(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := bundleFromYAMLs(map[string]bundleSpec{
		"greet-bundle": {
			kind:       "task",
			apiVersion: "tekton.dev/v1",
			yaml:       sampleTektonTaskYAML,
		},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	ref := host + "/tkn-act/test-bundle:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("crane.Push: %v", err)
	}

	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{AllowInsecureHTTP: true})
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": ref,
			"name":   "greet-bundle",
			// kind omitted — default "task".
		},
	})
	if err != nil {
		t.Fatalf("resolve (kind omitted): %v", err)
	}
}

// TestBundlesResolverMissingBundle: the `bundle` param is required.
func TestBundlesResolverMissingBundle(t *testing.T) {
	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params:   map[string]string{"name": "x"},
	})
	if err == nil {
		t.Fatal("expected error for missing bundle param")
	}
	if !strings.Contains(err.Error(), "bundle") {
		t.Errorf("error %q does not mention the bundle param", err)
	}
}

// TestBundlesResolverMissingName: the `name` param is required.
func TestBundlesResolverMissingName(t *testing.T) {
	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params:   map[string]string{"bundle": "gcr.io/x/y:tag"},
	})
	if err == nil {
		t.Fatal("expected error for missing name param")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error %q does not mention the name param", err)
	}
}

// TestBundlesResolverHTTPSOnlyByDefault: without AllowInsecureHTTP=true,
// a non-loopback http registry reference must NOT silently downgrade to
// plain HTTP. We point at TEST-NET-2 (203.0.113.0/24) so DNS isn't a
// factor; the resolver should attempt HTTPS and fail.
//
// The point of the test is the negative-space assertion: the error must
// not be the "missing param" path (which would mean we never tried).
func TestBundlesResolverHTTPSOnlyByDefault(t *testing.T) {
	res := refresolver.NewBundlesResolver(refresolver.BundlesOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := res.Resolve(ctx, refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": "203.0.113.1:1/x/y:tag",
			"name":   "greet-bundle",
		},
	})
	if err == nil {
		t.Fatal("expected error for unreachable bundle")
	}
	// Whatever the error is, it must NOT be the "missing param" error
	// path — that would mean we never tried to reach the registry.
	if strings.Contains(err.Error(), "is required") {
		t.Errorf("expected a fetch-time error, got: %v", err)
	}
}

// TestBundlesResolverName: sanity check on the registered name.
func TestBundlesResolverName(t *testing.T) {
	if got := refresolver.NewBundlesResolver(refresolver.BundlesOptions{}).Name(); got != "bundles" {
		t.Errorf("Name() = %q, want %q", got, "bundles")
	}
}

// TestBundlesResolverRegisteredInDefaultRegistry: bundles is wired into
// NewDefaultRegistry's allow-list and dispatch table after Phase 4.
func TestBundlesResolverRegisteredInDefaultRegistry(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := bundleFromYAMLs(map[string]bundleSpec{
		"greet-bundle": {
			kind:       "task",
			apiVersion: "tekton.dev/v1",
			yaml:       sampleTektonTaskYAML,
		},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	ref := host + "/tkn-act/test-bundle:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatalf("crane.Push: %v", err)
	}

	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow:             []string{"bundles"},
		AllowInsecureHTTP: true,
	})
	out, err := reg.Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params: map[string]string{
			"bundle": ref,
			"name":   "greet-bundle",
			"kind":   "task",
		},
	})
	if err != nil {
		t.Fatalf("expected default-registry bundles dispatch to succeed against loopback registry; got %v", err)
	}
	if errors.Is(err, refresolver.ErrResolverNotRegistered) {
		t.Errorf("bundles should be registered by default")
	}
	if !strings.Contains(string(out.Bytes), "greet-bundle") {
		t.Errorf("returned bytes don't match: %q", out.Bytes)
	}
}
