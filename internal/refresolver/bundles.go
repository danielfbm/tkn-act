package refresolver

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Standard Tekton bundle layer annotation keys (per the Tekton bundles
// spec at https://github.com/tektoncd/community/blob/main/teps/0079-tekton-catalog-support-tiers.md
// and the bundles-resolver implementation in tektoncd/pipeline).
const (
	annotationName       = "dev.tekton.image.name"
	annotationKind       = "dev.tekton.image.kind"
	annotationAPIVersion = "dev.tekton.image.apiVersion"
)

// BundlesOptions configures a bundles resolver. All fields optional.
type BundlesOptions struct {
	// AllowInsecureHTTP opts the bundles resolver into plain HTTP for
	// non-loopback registries. Loopback registries (127.0.0.1, ::1,
	// localhost) always permit plain HTTP so unit tests using
	// httptest.NewServer + go-containerregistry's pkg/registry work
	// out of the box.
	//
	// Threaded through from --resolver-allow-insecure-http on the run
	// command (CI-only escape hatch). The default keychain via
	// ~/.docker/config.json is honored regardless.
	AllowInsecureHTTP bool

	// Keychain overrides the authentication source. Production callers
	// pass nil; the resolver then uses authn.DefaultKeychain which reads
	// ~/.docker/config.json. Tests may inject a custom keychain (e.g.
	// authn.NewMultiKeychain).
	Keychain authn.Keychain

	// RemoteOptions, when non-nil, replaces the default remote.Option
	// list passed to go-containerregistry. Tests use this to inject
	// registry transports without going through global state.
	RemoteOptions []remote.Option
}

// BundlesResolver implements Resolver for taskRef.resolver: bundles.
//
// Resolver params:
//
//	bundle   required. OCI ref like gcr.io/foo/catalog:v1.
//	name     required. metadata.name of the resource to extract.
//	kind     optional, default "task". One of "task", "pipeline".
//
// On a happy path the resolver pulls the named OCI image, walks each
// layer's annotations looking for the matching (kind, name) pair, and
// returns the YAML bytes embedded in that layer's tar entry.
type BundlesResolver struct {
	opts BundlesOptions
}

// NewBundlesResolver constructs a bundles resolver.
func NewBundlesResolver(opts BundlesOptions) *BundlesResolver {
	if opts.Keychain == nil {
		opts.Keychain = authn.DefaultKeychain
	}
	return &BundlesResolver{opts: opts}
}

// Name implements Resolver.
func (b *BundlesResolver) Name() string { return "bundles" }

// Resolve implements Resolver. See type-level docs for the param shape.
func (b *BundlesResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	bundleRef := strings.TrimSpace(req.Params["bundle"])
	if bundleRef == "" {
		return Resolved{}, errors.New("bundles: bundle param is required (OCI image reference)")
	}
	resourceName := strings.TrimSpace(req.Params["name"])
	if resourceName == "" {
		return Resolved{}, errors.New("bundles: name param is required (the resource metadata.name to extract)")
	}
	kind := strings.TrimSpace(req.Params["kind"])
	if kind == "" {
		kind = "task"
	}
	kind = strings.ToLower(kind)

	nameOpts := []name.Option{name.StrictValidation}
	if b.allowInsecureFor(bundleRef) {
		nameOpts = append(nameOpts, name.Insecure)
	}
	ref, err := name.ParseReference(bundleRef, nameOpts...)
	if err != nil {
		return Resolved{}, fmt.Errorf("bundles: parsing bundle ref %q: %w", bundleRef, err)
	}

	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(b.opts.Keychain),
	}
	if len(b.opts.RemoteOptions) > 0 {
		remoteOpts = append(remoteOpts, b.opts.RemoteOptions...)
	}

	img, err := remote.Image(ref, remoteOpts...)
	if err != nil {
		return Resolved{}, fmt.Errorf("bundles: fetching %s: %w", bundleRef, err)
	}

	bytesYAML, err := extractTektonResource(img, kind, resourceName)
	if err != nil {
		return Resolved{}, fmt.Errorf("bundles: in %s: %w", bundleRef, err)
	}

	return Resolved{
		Bytes:  bytesYAML,
		Source: fmt.Sprintf("bundles: %s -> %s/%s", bundleRef, kind, resourceName),
	}, nil
}

// allowInsecureFor reports whether the resolver should treat the given
// reference as plain-http. Loopback hosts always qualify so test
// registries work; non-loopback hosts qualify only when the resolver
// option is explicitly set (--resolver-allow-insecure-http).
func (b *BundlesResolver) allowInsecureFor(ref string) bool {
	if b.opts.AllowInsecureHTTP {
		return true
	}
	// Take just the registry hostname from the ref (everything before
	// the first slash). go-containerregistry would reject a malformed
	// ref further down anyway.
	host := ref
	if i := strings.Index(host, "/"); i != -1 {
		host = host[:i]
	}
	return isLoopbackRegistryHost(host)
}

// isLoopbackRegistryHost matches host:port forms that are 127.0.0.1, ::1,
// or localhost (case-insensitive). go-containerregistry's name package
// has its own loopback check but it isn't exported; this duplicates the
// minimal slice we need.
func isLoopbackRegistryHost(host string) bool {
	if host == "" {
		return false
	}
	// Strip port if present. IPv6 hosts come wrapped in [...].
	h := host
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end != -1 {
			h = h[1:end]
		}
	} else if i := strings.LastIndex(h, ":"); i != -1 {
		// host:port form. Avoid stripping a colon inside an unwrapped
		// IPv6 literal (impossible in valid registry refs but be safe).
		h = h[:i]
	}
	switch strings.ToLower(h) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if strings.HasPrefix(h, "127.") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// extractTektonResource walks img's layers in declaration order and
// returns the YAML bytes of the first layer whose annotations match the
// requested (kind, name). The YAML lives as a single file inside the
// layer's tar archive; we read the first regular file in the tar.
func extractTektonResource(img v1.Image, wantKind, wantName string) ([]byte, error) {
	manifest, err := img.Manifest()
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("layers: %w", err)
	}
	if len(layers) != len(manifest.Layers) {
		return nil, fmt.Errorf("manifest/layer count mismatch: %d vs %d", len(manifest.Layers), len(layers))
	}

	var foundNames []string
	for i, descriptor := range manifest.Layers {
		ann := descriptor.Annotations
		gotName := ann[annotationName]
		gotKind := strings.ToLower(ann[annotationKind])
		if gotName != "" {
			foundNames = append(foundNames, fmt.Sprintf("%s/%s", gotKind, gotName))
		}
		if gotName != wantName {
			continue
		}
		if wantKind != "" && gotKind != "" && gotKind != wantKind {
			continue
		}

		// Layer matches — extract the YAML bytes.
		rc, err := layers[i].Uncompressed()
		if err != nil {
			return nil, fmt.Errorf("layer %d uncompressed: %w", i, err)
		}
		defer rc.Close()
		body, err := readFirstTarFile(rc)
		if err != nil {
			return nil, fmt.Errorf("layer %d tar: %w", i, err)
		}
		return body, nil
	}

	if len(foundNames) > 0 {
		return nil, fmt.Errorf("resource %q (kind %q) not found; layers found: %s",
			wantName, wantKind, strings.Join(foundNames, ", "))
	}
	return nil, fmt.Errorf("resource %q (kind %q) not found in bundle (no Tekton-bundle annotations)", wantName, wantKind)
}

// readFirstTarFile returns the bytes of the first regular file in the
// tar stream. Tekton bundle layers contain exactly one YAML file by
// convention; we tolerate stray directories or symlinks.
func readFirstTarFile(r io.Reader) ([]byte, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("empty layer tar (no files)")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}
