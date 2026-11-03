package refresolver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// gitWorkRepo is a bare repo plus a working clone the test can keep
// committing into (so a branch ref can move between resolves).
type gitWorkRepo struct {
	t    *testing.T
	bare string
	work string
}

func (g *gitWorkRepo) url() string { return "file://" + g.bare }

func (g *gitWorkRepo) git(args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.work
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=tkn-act-test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=tkn-act-test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGitWorkRepo(t *testing.T) *gitWorkRepo {
	t.Helper()
	root := t.TempDir()
	g := &gitWorkRepo{t: t, bare: filepath.Join(root, "repo.git"), work: filepath.Join(root, "work")}
	for _, d := range []string{g.bare, g.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main")
	cmd.Dir = g.bare
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	g.git("init", "--initial-branch=main")
	g.git("remote", "add", "origin", g.bare)
	return g
}

// commit writes files and symlinks (link path -> target) and pushes main.
func (g *gitWorkRepo) commit(files, links map[string]string) {
	g.t.Helper()
	for p, body := range files {
		full := filepath.Join(g.work, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			g.t.Fatal(err)
		}
		g.git("add", p)
	}
	for p, target := range links {
		full := filepath.Join(g.work, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			g.t.Fatal(err)
		}
		g.git("add", p)
	}
	g.git("commit", "-m", "c", "--no-gpg-sign")
	g.git("push", "origin", "main")
}

// TestGitResolverRefusesSymlinkEscape (B1): a repository whose
// pathInRepo is a symlink pointing outside the clone must not leak the
// target's contents — neither via an absolute nor a relative target.
func TestGitResolverRefusesSymlinkEscape(t *testing.T) {
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-HOST-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel := strings.Repeat("../", 40) + strings.TrimPrefix(secret, "/")

	repo := newGitWorkRepo(t)
	repo.commit(map[string]string{"real.yaml": "inside-repo"}, map[string]string{
		"abs.yaml":    secret,
		"rel.yaml":    rel,
		"dir/link":    secretDir,
		"dir/rellink": strings.Repeat("../", 40) + strings.TrimPrefix(secretDir, "/"),
		"inside.yaml": "real.yaml",
	})

	for _, withCache := range []bool{false, true} {
		cacheDir := ""
		if withCache {
			cacheDir = t.TempDir()
		}
		r := refresolver.NewGit(cacheDir)
		for _, p := range []string{"abs.yaml", "rel.yaml", "dir/link/secret.txt", "dir/rellink/secret.txt"} {
			out, err := r.Resolve(context.Background(), refresolver.Request{
				Resolver: "git",
				Params:   map[string]string{"url": repo.url(), "pathInRepo": p},
			})
			if err == nil {
				t.Fatalf("cache=%v path=%s: expected error, got bytes %q", withCache, p, out.Bytes)
			}
			if strings.Contains(err.Error(), "TOP-SECRET") || bytes.Contains(out.Bytes, []byte("TOP-SECRET")) {
				t.Fatalf("cache=%v path=%s: secret leaked: %v", withCache, p, err)
			}
			// Newer go-git refuses to materialize absolute symlinks at
			// all (-> "not found"); relative ones are checked out and
			// must be caught by the resolver itself.
			if !strings.Contains(err.Error(), "escapes the repository") && !strings.Contains(err.Error(), "not found") {
				t.Errorf("cache=%v path=%s: unexpected error %q", withCache, p, err)
			}
			if strings.Contains(p, "rel") && !strings.Contains(err.Error(), "escapes the repository") {
				t.Errorf("cache=%v path=%s: error %q should say it escapes the repository", withCache, p, err)
			}
		}
		// A symlink that stays inside the repository keeps working.
		out, err := r.Resolve(context.Background(), refresolver.Request{
			Resolver: "git",
			Params:   map[string]string{"url": repo.url(), "pathInRepo": "inside.yaml"},
		})
		if err != nil {
			t.Fatalf("cache=%v in-repo symlink: %v", withCache, err)
		}
		if string(out.Bytes) != "inside-repo" {
			t.Errorf("cache=%v in-repo symlink bytes = %q", withCache, out.Bytes)
		}
	}
}

func gitKey(url, revision string) string {
	h := sha256.New()
	h.Write([]byte(url))
	h.Write([]byte{0})
	h.Write([]byte(revision))
	return hex.EncodeToString(h.Sum(nil))
}

// TestGitCloneDirsVisibleToCacheCommands (B8): git clones under
// <cache>/git/<key>/ are listed, cleared and pruned by DiskCache, and a
// `cache clear` makes the next resolve of a branch ref see new commits.
func TestGitCloneDirsVisibleToCacheCommands(t *testing.T) {
	repo := newGitWorkRepo(t)
	repo.commit(map[string]string{"task.yaml": "v1"}, nil)
	cacheDir := t.TempDir()
	req := refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": repo.url(), "revision": "main", "pathInRepo": "task.yaml"},
	}

	out, err := refresolver.NewGit(cacheDir).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Bytes) != "v1" {
		t.Fatalf("bytes = %q, want v1", out.Bytes)
	}

	dc := refresolver.NewDiskCache(cacheDir)
	entries, err := dc.List()
	if err != nil {
		t.Fatal(err)
	}
	cloneDir := filepath.Join(cacheDir, "git", gitKey(repo.url(), "main"))
	found := false
	for _, e := range entries {
		if e.Path == cloneDir {
			found = true
			if e.Resolver != "git" || e.Size <= 0 {
				t.Errorf("clone entry = %+v, want resolver git and size > 0", e)
			}
		}
	}
	if !found {
		t.Fatalf("List() = %+v, missing git clone dir %s", entries, cloneDir)
	}

	// Branch moves upstream; a cache clear must make the next resolve
	// observe the new commit.
	repo.commit(map[string]string{"task.yaml": "v2"}, nil)
	n, err := dc.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("Clear() = %d, want >= 1", n)
	}
	if _, err := os.Stat(cloneDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clone dir still present after Clear: %v", err)
	}
	out, err = refresolver.NewGit(cacheDir).Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Bytes) != "v2" {
		t.Errorf("after clear bytes = %q, want v2", out.Bytes)
	}

	// Prune with a zero cutoff removes the clone too.
	if n, err := dc.PruneOlderThan(-1); err != nil || n < 1 {
		t.Fatalf("PruneOlderThan = %d, %v; want >= 1 pruned", n, err)
	}
	if _, err := os.Stat(cloneDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clone dir still present after prune: %v", err)
	}
}

// TestGitResolverRecoversFromPartialClone (B8): a leftover, broken
// repo dir (an interrupted clone) is not reused as if complete.
func TestGitResolverRecoversFromPartialClone(t *testing.T) {
	repo := newGitWorkRepo(t)
	repo.commit(map[string]string{"task.yaml": "complete"}, nil)
	cacheDir := t.TempDir()
	partial := filepath.Join(cacheDir, "git", gitKey(repo.url(), "main"), "repo")
	if err := os.MkdirAll(filepath.Join(partial, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := refresolver.NewGit(cacheDir).Resolve(context.Background(), refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": repo.url(), "pathInRepo": "task.yaml"},
	})
	if err != nil {
		t.Fatalf("resolve over partial clone: %v", err)
	}
	if string(out.Bytes) != "complete" {
		t.Errorf("bytes = %q, want complete", out.Bytes)
	}
	// No temp clone dirs left behind.
	ents, _ := os.ReadDir(filepath.Dir(partial))
	for _, e := range ents {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("leftover temp dir %s", e.Name())
		}
	}
}

// TestHTTPResolverRefusesHTTPSDowngradeRedirect (B3): an https URL that
// redirects to plain http must not follow (and must not send the
// bearer token in cleartext).
func TestHTTPResolverRefusesHTTPSDowngradeRedirect(t *testing.T) {
	var plainHits, plainAuth atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			plainAuth.Add(1)
		}
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/task.yaml", http.StatusFound)
	}))
	defer tlsSrv.Close()

	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{Token: "s3cret", Client: tlsSrv.Client()})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": tlsSrv.URL + "/task.yaml"},
	})
	if err == nil {
		t.Fatal("expected downgrade redirect to be refused")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error %q should mention the redirect", err)
	}
	if plainHits.Load() != 0 || plainAuth.Load() != 0 {
		t.Errorf("plain server hit %d times (%d with Authorization); want 0", plainHits.Load(), plainAuth.Load())
	}

	// With AllowInsecureHTTP the downgrade is followed.
	res = refresolver.NewHTTPResolver(refresolver.HTTPOptions{AllowInsecureHTTP: true, Client: tlsSrv.Client()})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": tlsSrv.URL + "/task.yaml"},
	}); err != nil {
		t.Fatalf("insecure-allowed downgrade: %v", err)
	}
}

// TestHTTPResolverDropsAuthOnHostChange (B3): a redirect to a different
// host keeps working but the bearer token is not forwarded.
func TestHTTPResolverDropsAuthOnHostChange(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer other.Close()
	// Same IP, different port => different host:port.
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/task.yaml", http.StatusFound)
	}))
	defer first.Close()

	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{Token: "s3cret"})
	if _, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": first.URL + "/task.yaml"},
	}); err != nil {
		t.Fatal(err)
	}
	if a := gotAuth.Load().(string); a != "" {
		t.Errorf("Authorization forwarded across hosts: %q", a)
	}
}

// TestHubResolverRefusesHTTPSDowngradeRedirect (B3).
func TestHubResolverRefusesHTTPSDowngradeRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer tlsSrv.Close()

	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: tlsSrv.URL, Token: "s3cret", Client: tlsSrv.Client()})
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "golang-build"},
	})
	if err == nil {
		t.Fatal("expected downgrade redirect to be refused")
	}
	if plainHits.Load() != 0 {
		t.Errorf("plain server hit %d times; want 0", plainHits.Load())
	}
}

// TestHTTPResolverRedactsCredentials (B4): userinfo and query strings
// never reach Source or error strings.
func TestHTTPResolverRedactsCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	withCreds := strings.Replace(srv.URL, "http://", "http://user:pw-SECRET@", 1)

	res := refresolver.NewHTTPResolver(refresolver.HTTPOptions{})
	out, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": withCreds + "/task.yaml?token=q-SECRET"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Source, "SECRET") {
		t.Errorf("Source leaks credentials: %q", out.Source)
	}
	if !strings.Contains(out.Source, "/task.yaml") {
		t.Errorf("Source %q should still name the path", out.Source)
	}

	for _, u := range []string{
		withCreds + "/missing.yaml?token=q-SECRET",
		"ftp://user:pw-SECRET@example.com/x?token=q-SECRET",
		"http://user:pw-SECRET@example.com/x?token=q-SECRET",
		"http://user:pw-SECRET@127.0.0.1:1/x?token=q-SECRET",
	} {
		_, err := res.Resolve(context.Background(), refresolver.Request{
			Resolver: "http",
			Params:   map[string]string{"url": u},
		})
		if err == nil {
			t.Fatalf("%s: expected error", u)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error leaks credentials: %v", err)
		}
	}
}

// TestDiskCacheRedactsParamsAndRestrictsPerms (B4): cache meta never
// stores credentials, and cache files/dirs are owner-only.
func TestDiskCacheRedactsParamsAndRestrictsPerms(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	req := refresolver.Request{Resolver: "git", Params: map[string]string{
		"url":        "https://user:tok-SECRET@example.com/org/repo.git",
		"pathInRepo": "task.yaml",
		"other":      "https://example.com/x?sig=q-SECRET",
	}}
	if err := c.Put(req, refresolver.Resolved{Bytes: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "git")
	files, err := os.ReadDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	for _, f := range files {
		p := filepath.Join(sub, f.Name())
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte("SECRET")) {
			t.Errorf("%s leaks credentials: %s", f.Name(), b)
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f.Name(), st.Mode().Perm())
		}
		if strings.HasSuffix(f.Name(), ".json") && !bytes.Contains(b, []byte("example.com/org/repo.git")) {
			t.Errorf("meta should keep the redacted url host/path: %s", b)
		}
	}
	st, _ := os.Stat(sub)
	if st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", st.Mode().Perm())
	}
	// The (unredacted-param) request still hits.
	if _, ok, err := c.Get(req); err != nil || !ok {
		t.Errorf("Get after Put: ok=%v err=%v", ok, err)
	}
}

// allowCountingResolver counts Resolve calls.
type allowCountingResolver struct {
	name  string
	calls atomic.Int32
}

func (c *allowCountingResolver) Name() string { return c.name }
func (c *allowCountingResolver) Resolve(context.Context, refresolver.Request) (refresolver.Resolved, error) {
	c.calls.Add(1)
	return refresolver.Resolved{Bytes: []byte("fresh")}, nil
}

// TestRegistryAllowListBeforeCache (B5): a resolver excluded by the
// allow-list is refused even when the disk / per-run cache holds bytes.
func TestRegistryAllowListBeforeCache(t *testing.T) {
	dir := t.TempDir()
	dc := refresolver.NewDiskCache(dir)
	req := refresolver.Request{Resolver: "http", Params: map[string]string{"url": "https://example.com/t.yaml"}}
	if err := dc.Put(req, refresolver.Resolved{Bytes: []byte("cached")}); err != nil {
		t.Fatal(err)
	}

	r := refresolver.NewRegistry()
	cr := &allowCountingResolver{name: "http"}
	r.Register(cr)
	r.SetCache(dc)
	r.SetAllow([]string{"git"})
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, refresolver.ErrResolverNotAllowed) {
		t.Fatalf("disk-cached disallowed resolver: err = %v, want ErrResolverNotAllowed", err)
	}

	// Populate the per-run cache while allowed, then restrict.
	r.SetCache(nil)
	r.SetAllow([]string{"http"})
	req2 := refresolver.Request{Resolver: "http", Params: map[string]string{"url": "https://example.com/u.yaml"}}
	if _, err := r.Resolve(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	r.SetAllow([]string{"git"})
	if _, err := r.Resolve(context.Background(), req2); !errors.Is(err, refresolver.ErrResolverNotAllowed) {
		t.Fatalf("per-run-cached disallowed resolver: err = %v, want ErrResolverNotAllowed", err)
	}
}

// TestHTTPResolverBodyLimit (B7): oversized bodies fail with a clear
// error instead of being buffered without bound.
func TestHTTPResolverBodyLimit(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, refresolver.MaxResolvedBytes+1)
	}))
	defer big.Close()

	_, err := refresolver.NewHTTPResolver(refresolver.HTTPOptions{}).Resolve(context.Background(), refresolver.Request{
		Resolver: "http",
		Params:   map[string]string{"url": big.URL + "/t.yaml"},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("http: err = %v, want size-limit error", err)
	}
	_, err = refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: big.URL}).Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("hub: err = %v, want size-limit error", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// TestBundlesResolverRequiresKindAnnotation (B7): a layer carrying only
// the name annotation is not a valid Tekton bundle layer.
func TestBundlesResolverRequiresKindAnnotation(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	layerBytes, err := tarSingleFile("greet.yaml", []byte(sampleTektonTaskYAML))
	if err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(layerBytes)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       layer,
		Annotations: map[string]string{"dev.tekton.image.name": "greet-bundle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := host + "/tkn-act/nokind:v1"
	if err := crane.Push(img, ref); err != nil {
		t.Fatal(err)
	}
	_, err = refresolver.NewBundlesResolver(refresolver.BundlesOptions{}).Resolve(context.Background(), refresolver.Request{
		Resolver: "bundles",
		Params:   map[string]string{"bundle": ref, "name": "greet-bundle"},
	})
	if err == nil {
		t.Fatal("expected layer without dev.tekton.image.kind to be rejected")
	}
}

// TestHubResolverTypeParam (B9): `type: tekton` (and unset) use the
// Tekton Hub API; `type: artifact` fails with a clear unsupported error.
func TestHubResolverTypeParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	res := refresolver.NewHubResolver(refresolver.HubOptions{BaseURL: srv.URL})

	for _, typ := range []string{"", "tekton", "Tekton"} {
		if _, err := res.Resolve(context.Background(), refresolver.Request{
			Resolver: "hub",
			Params:   map[string]string{"name": "golang-build", "type": typ},
		}); err != nil {
			t.Errorf("type=%q: %v", typ, err)
		}
	}
	_, err := res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "golang-build", "type": "artifact"},
	})
	if err == nil || !strings.Contains(err.Error(), "artifact hub") || !strings.Contains(err.Error(), "type: tekton") {
		t.Errorf("type=artifact: err = %v, want artifact-hub-unsupported error", err)
	}
	_, err = res.Resolve(context.Background(), refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "golang-build", "type": "bogus"},
	})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("type=bogus: err = %v, want invalid-type error", err)
	}
}

// TestHTTPResolverPrefixedTokenEnv (B10): TKN_ACT_HTTP_RESOLVER_TOKEN is
// the preferred env var; the legacy TKNACT_ name is a fallback.
func TestHTTPResolverPrefixedTokenEnv(t *testing.T) {
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(sampleTaskYAML))
	}))
	defer srv.Close()
	resolve := func() string {
		t.Helper()
		if _, err := refresolver.NewHTTPResolver(refresolver.HTTPOptions{}).Resolve(context.Background(), refresolver.Request{
			Resolver: "http",
			Params:   map[string]string{"url": srv.URL + "/t.yaml"},
		}); err != nil {
			t.Fatal(err)
		}
		return got.Load().(string)
	}

	t.Setenv(refresolver.HTTPTokenEnvLegacy, "")
	t.Setenv(refresolver.HTTPTokenEnv, "new-token")
	if a := resolve(); a != "Bearer new-token" {
		t.Errorf("new env only: Authorization = %q", a)
	}
	t.Setenv(refresolver.HTTPTokenEnvLegacy, "old-token")
	if a := resolve(); a != "Bearer new-token" {
		t.Errorf("both set: Authorization = %q, want new name to win", a)
	}
	t.Setenv(refresolver.HTTPTokenEnv, "")
	if a := resolve(); a != "Bearer old-token" {
		t.Errorf("legacy only: Authorization = %q", a)
	}
}
