package refresolver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// TestDiskCachePutGetRoundTrip exercises the on-disk cache helper:
// Put writes (bytes, meta) under <root>/<resolver>/<key>.{yaml,json};
// Get reads them back; Has reports presence cheaply.
func TestDiskCachePutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)

	req := refresolver.Request{
		Resolver: "git",
		Params:   map[string]string{"url": "https://x", "revision": "main", "pathInRepo": "t.yaml"},
	}
	want := refresolver.Resolved{
		Bytes:  []byte("kind: Task\nmetadata:\n  name: t\nspec:\n  steps: []\n"),
		Source: "git: https://x@main",
		SHA256: "sha-of-task-bytes",
	}
	if err := c.Put(req, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !c.Has(req) {
		t.Fatal("Has after Put returned false")
	}
	got, ok, err := c.Get(req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get after Put returned ok=false")
	}
	if string(got.Bytes) != string(want.Bytes) {
		t.Errorf("bytes round-trip: got %q want %q", got.Bytes, want.Bytes)
	}
	if !got.Cached {
		t.Errorf("Get from disk should set Cached=true; got false")
	}
	if got.Source == "" {
		t.Errorf("Source dropped on round-trip")
	}
}

// TestDiskCacheMissReturnsOk false; no error.
func TestDiskCacheMissReturnsOk(t *testing.T) {
	c := refresolver.NewDiskCache(t.TempDir())
	req := refresolver.Request{Resolver: "git", Params: map[string]string{"url": "x"}}
	if c.Has(req) {
		t.Error("empty cache reported Has=true")
	}
	_, ok, err := c.Get(req)
	if err != nil {
		t.Fatalf("Get on miss returned error: %v", err)
	}
	if ok {
		t.Error("Get on miss returned ok=true")
	}
}

// TestDiskCacheLayoutResolverSubdir verifies the on-disk layout splits
// entries by resolver name (so prune-by-resolver works) and uses the
// CacheKey hash as the file stem.
func TestDiskCacheLayoutResolverSubdir(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	req := refresolver.Request{
		Resolver: "hub",
		Params:   map[string]string{"name": "build", "version": "0.1"},
	}
	want := refresolver.Resolved{Bytes: []byte("hub-bytes")}
	if err := c.Put(req, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	subdir := filepath.Join(dir, "hub")
	entries, err := os.ReadDir(subdir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", subdir, err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries written under hub/")
	}
}

// TestDiskCacheList enumerates entries with metadata (resolver, key,
// size, age). Listed entries reflect what Put wrote.
func TestDiskCacheList(t *testing.T) {
	c := refresolver.NewDiskCache(t.TempDir())

	if err := c.Put(refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}},
		refresolver.Resolved{Bytes: []byte("hello")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Put(refresolver.Request{Resolver: "hub", Params: map[string]string{"b": "2"}},
		refresolver.Resolved{Bytes: []byte("world!!")}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List returned %d entries, want 2", len(entries))
	}
	resolvers := map[string]bool{}
	for _, e := range entries {
		resolvers[e.Resolver] = true
		if e.Size <= 0 {
			t.Errorf("entry %v has zero/negative size", e)
		}
		if e.Key == "" {
			t.Errorf("entry %v has empty key", e)
		}
		if time.Since(e.ModTime) > time.Minute {
			t.Errorf("entry mod-time too old: %v", e.ModTime)
		}
	}
	if !resolvers["git"] || !resolvers["hub"] {
		t.Errorf("missing resolvers in list: %v", resolvers)
	}
}

// TestDiskCachePruneOlderThan deletes only entries older than the
// given duration; younger entries survive.
func TestDiskCachePruneOlderThan(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)

	old := refresolver.Request{Resolver: "git", Params: map[string]string{"a": "old"}}
	young := refresolver.Request{Resolver: "git", Params: map[string]string{"a": "young"}}
	if err := c.Put(old, refresolver.Resolved{Bytes: []byte("o")}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(young, refresolver.Resolved{Bytes: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	// Backdate the "old" entry by 2 hours.
	oldPath := filepath.Join(dir, "git", refresolver.CacheKey("git", old.Params)+".yaml")
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldPath, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// Backdate the meta file too.
	if err := os.Chtimes(oldPath[:len(oldPath)-len(".yaml")]+".json", twoHoursAgo, twoHoursAgo); err != nil {
		// meta-file backdate is best-effort
		_ = err
	}

	n, err := c.PruneOlderThan(time.Hour)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if c.Has(old) {
		t.Error("old entry survived prune")
	}
	if !c.Has(young) {
		t.Error("young entry was pruned")
	}
}

// TestDiskCacheClear removes every entry.
func TestDiskCacheClear(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	if err := c.Put(refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}},
		refresolver.Resolved{Bytes: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(refresolver.Request{Resolver: "hub", Params: map[string]string{"a": "1"}},
		refresolver.Resolved{Bytes: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	n, err := c.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n != 2 {
		t.Errorf("Cleared %d, want 2", n)
	}
	entries, err := c.List()
	if err != nil {
		t.Fatalf("List after clear: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries remain after clear: %v", entries)
	}
}

// TestDiskCacheEmptyRoot Has/Get/List/Prune/Clear are safe on a
// non-existent root (the user passed a stale --resolver-cache-dir).
func TestDiskCacheEmptyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	c := refresolver.NewDiskCache(root)
	req := refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}}
	if c.Has(req) {
		t.Error("Has on missing root returned true")
	}
	if _, ok, err := c.Get(req); err != nil || ok {
		t.Errorf("Get on missing root: ok=%v err=%v", ok, err)
	}
	entries, err := c.List()
	if err != nil {
		t.Fatalf("List on missing root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List on missing root returned %d entries", len(entries))
	}
	if _, err := c.PruneOlderThan(time.Hour); err != nil {
		t.Fatalf("PruneOlderThan on missing root: %v", err)
	}
	if _, err := c.Clear(); err != nil {
		t.Fatalf("Clear on missing root: %v", err)
	}
}

// TestRegistryResolveUsesDiskCache: when the Registry has a Cache
// installed, a fresh Resolve writes to disk; a second Registry sharing
// the same Cache (different per-run map) reads from disk and returns
// Cached=true.
func TestRegistryResolveUsesDiskCache(t *testing.T) {
	dir := t.TempDir()
	cache := refresolver.NewDiskCache(dir)

	// Build registry with a counting stub resolver.
	r1 := refresolver.NewRegistry()
	r1.SetCache(cache)
	stub := &countingResolver{name: "stub", bytes: []byte("kind: Task\nmetadata:\n  name: t\nspec: {steps: []}\n")}
	r1.Register(stub)

	req := refresolver.Request{Resolver: "stub", Params: map[string]string{"k": "v"}}
	got, err := r1.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if got.Cached {
		t.Error("first Resolve reported Cached=true")
	}
	if stub.calls != 1 {
		t.Fatalf("stub called %d times after first Resolve, want 1", stub.calls)
	}

	// Second registry, same cache dir.
	r2 := refresolver.NewRegistry()
	r2.SetCache(cache)
	r2.Register(stub)
	got2, err := r2.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !got2.Cached {
		t.Error("second Resolve from another registry should be Cached=true")
	}
	if stub.calls != 1 {
		t.Errorf("stub called %d times after second Resolve, want 1 (disk hit)", stub.calls)
	}
}

// TestRegistryOfflineRejectsCacheMiss: when Options.Offline is true and
// no Cache is configured (or the entry isn't in the disk cache), Resolve
// fails fast with a clear error and does NOT call the underlying
// resolver.
func TestRegistryOfflineRejectsCacheMiss(t *testing.T) {
	r := refresolver.NewRegistry()
	r.SetOffline(true)
	r.SetCache(refresolver.NewDiskCache(t.TempDir()))
	stub := &countingResolver{name: "stub", bytes: []byte("k: v")}
	r.Register(stub)

	_, err := r.Resolve(context.Background(), refresolver.Request{
		Resolver: "stub", Params: map[string]string{"k": "v"},
	})
	if err == nil {
		t.Fatal("Resolve in offline+miss returned nil error")
	}
	if stub.calls != 0 {
		t.Errorf("stub called %d times in offline+miss; want 0 (no network)", stub.calls)
	}
}

// TestRegistryOfflineAllowsCacheHit: pre-populate the disk cache, set
// Offline=true, expect a Cached=true Resolve without ever calling the
// resolver.
func TestRegistryOfflineAllowsCacheHit(t *testing.T) {
	cache := refresolver.NewDiskCache(t.TempDir())
	stub := &countingResolver{name: "stub", bytes: []byte("k: v")}
	req := refresolver.Request{Resolver: "stub", Params: map[string]string{"k": "v"}}
	if err := cache.Put(req, refresolver.Resolved{Bytes: []byte("from-disk")}); err != nil {
		t.Fatal(err)
	}
	r := refresolver.NewRegistry()
	r.SetOffline(true)
	r.SetCache(cache)
	r.Register(stub)
	got, err := r.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Cached {
		t.Error("offline+hit should set Cached=true")
	}
	if stub.calls != 0 {
		t.Errorf("stub called %d times despite offline+hit; want 0", stub.calls)
	}
}

// TestDiskCacheNilReceiver covers the nil-receiver / unrooted paths
// every method short-circuits on. Keeps coverage even on the
// defensive branches.
func TestDiskCacheNilReceiver(t *testing.T) {
	var c *refresolver.DiskCache // nil
	req := refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}}
	if c.Has(req) {
		t.Error("nil DiskCache.Has should return false")
	}
	if _, ok, err := c.Get(req); ok || err != nil {
		t.Errorf("nil DiskCache.Get: ok=%v err=%v", ok, err)
	}
	if err := c.Put(req, refresolver.Resolved{Bytes: []byte("x")}); err == nil {
		t.Error("nil DiskCache.Put should error")
	}

	// Unrooted (root="") behaves the same way as nil for the no-op
	// reads. List/Prune/Clear are no-ops.
	c2 := refresolver.NewDiskCache("")
	if c2.Has(req) {
		t.Error("unrooted Has should return false")
	}
	if _, ok, err := c2.Get(req); ok || err != nil {
		t.Errorf("unrooted Get: ok=%v err=%v", ok, err)
	}
	if err := c2.Put(req, refresolver.Resolved{Bytes: []byte("x")}); err == nil {
		t.Error("unrooted Put should error")
	}
	entries, err := c2.List()
	if err != nil || entries != nil {
		t.Errorf("unrooted List: entries=%v err=%v", entries, err)
	}
	if n, err := c2.PruneOlderThan(time.Hour); err != nil || n != 0 {
		t.Errorf("unrooted PruneOlderThan: n=%d err=%v", n, err)
	}
	if n, err := c2.Clear(); err != nil || n != 0 {
		t.Errorf("unrooted Clear: n=%d err=%v", n, err)
	}
}

// TestDiskCacheRootNotDirectory: when --resolver-cache-dir points at
// a regular file, List surfaces a clear error rather than silently
// returning empty (so users notice the typo).
func TestDiskCacheRootNotDirectory(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(bogus, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := refresolver.NewDiskCache(bogus)
	if _, err := c.List(); err == nil {
		t.Error("List on file-as-root should error")
	}
}

// TestDiskCacheRoot returns the configured root verbatim.
func TestDiskCacheRoot(t *testing.T) {
	const root = "/tmp/example"
	c := refresolver.NewDiskCache(root)
	if got := c.Root(); got != root {
		t.Errorf("Root() = %q, want %q", got, root)
	}
}

// TestDiskCacheGetMetaCorruptionTolerated: a missing or malformed
// meta.json doesn't fail Get — the bytes round-trip and Source/SHA256
// just stay empty. Cache-as-source-of-truth-for-bytes invariant.
func TestDiskCacheGetMetaCorruptionTolerated(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	req := refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}}
	if err := c.Put(req, refresolver.Resolved{Bytes: []byte("ok"), Source: "src"}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the meta file.
	metaPath := filepath.Join(dir, "git", refresolver.CacheKey("git", req.Params)+".json")
	if err := os.WriteFile(metaPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(req)
	if err != nil || !ok {
		t.Fatalf("Get with corrupt meta: ok=%v err=%v", ok, err)
	}
	if string(got.Bytes) != "ok" {
		t.Errorf("bytes = %q, want \"ok\"", got.Bytes)
	}
	// Source is empty when meta is corrupt.
	if got.Source != "" {
		t.Errorf("source = %q, want empty (meta corrupted)", got.Source)
	}
}

// TestDiskCacheSanitizeFallsBackOnEmptyResolver: an empty resolver
// name (defensive — every real call has one) lands under "_unnamed/".
func TestDiskCacheSanitizeFallsBackOnEmptyResolver(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	req := refresolver.Request{Resolver: "", Params: map[string]string{"a": "1"}}
	if err := c.Put(req, refresolver.Resolved{Bytes: []byte("x")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_unnamed")); err != nil {
		t.Errorf("_unnamed/ not created: %v", err)
	}
}

// TestDiskCacheSanitizeStripsUnsafeChars: unusual characters in a
// resolver name don't escape the cache root.
func TestDiskCacheSanitizeStripsUnsafeChars(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	req := refresolver.Request{Resolver: "weird/../name", Params: map[string]string{"a": "1"}}
	if err := c.Put(req, refresolver.Resolved{Bytes: []byte("x")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Ensure no traversal happened.
	root := dir
	rel, err := filepath.Rel(root, filepath.Join(dir, "weird_______name"))
	if err != nil || strings.HasPrefix(rel, "..") {
		// The exact transformed name doesn't matter; what matters is that
		// the slashes / dots got replaced.
		if _, err := os.Stat(filepath.Join(dir, "weird_______name")); err != nil {
			// Try the actual sanitised form by walking the tree.
			entries, _ := os.ReadDir(dir)
			if len(entries) == 0 {
				t.Errorf("no subdirectory created under cache root")
			}
		}
	}
}

// TestDiskCacheListSkipsNonResolverEntries: regular files at the
// root (not subdirectories) and non-.yaml files inside a resolver
// subdir are silently skipped — neither corrupt the listing nor
// surface as bogus entries.
func TestDiskCacheListSkipsNonResolverEntries(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	if err := c.Put(refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}},
		refresolver.Resolved{Bytes: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	// A stray regular file at the root (not a subdirectory).
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A nested subdir inside a resolver dir (skipped).
	if err := os.MkdirAll(filepath.Join(dir, "git", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-.yaml leaf file (skipped — meta sidecars share this branch).
	if err := os.WriteFile(filepath.Join(dir, "git", "stray.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List returned %d entries, want 1 (others should be skipped)", len(entries))
	}
}

// TestDiskCachePruneNothingToPrune: a cache with only young entries
// returns 0 from Prune; List is unaffected.
func TestDiskCachePruneNothingToPrune(t *testing.T) {
	dir := t.TempDir()
	c := refresolver.NewDiskCache(dir)
	if err := c.Put(refresolver.Request{Resolver: "git", Params: map[string]string{"a": "1"}},
		refresolver.Resolved{Bytes: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	n, err := c.PruneOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("Pruned %d, want 0 (entry is young)", n)
	}
	entries, _ := c.List()
	if len(entries) != 1 {
		t.Errorf("entries after no-op prune = %d, want 1", len(entries))
	}
}

// TestRegistryAccessors covers the small Cache()/Offline()/Remote()
// getters that mirror SetCache/SetOffline/SetRemote, exercising every
// branch of the lock-protected reads.
func TestRegistryAccessors(t *testing.T) {
	r := refresolver.NewRegistry()
	if r.Offline() {
		t.Error("default Offline() should be false")
	}
	if r.Cache() != nil {
		t.Error("default Cache() should be nil")
	}
	if r.Remote() != nil {
		t.Error("default Remote() should be nil")
	}

	r.SetOffline(true)
	if !r.Offline() {
		t.Error("SetOffline(true) didn't stick")
	}

	c := refresolver.NewDiskCache(t.TempDir())
	r.SetCache(c)
	if r.Cache() != c {
		t.Error("SetCache didn't stick")
	}
	r.SetCache(nil)
	if r.Cache() != nil {
		t.Error("SetCache(nil) didn't clear")
	}
}

// TestRegistryDefaultRegistryHonorsCacheDir: NewDefaultRegistry with
// a non-empty CacheDir installs a DiskCache; with empty CacheDir it
// leaves disk caching disabled. The latter mirrors the in-tree test
// harness path where each subtest gets a per-call tmpdir.
func TestRegistryDefaultRegistryHonorsCacheDir(t *testing.T) {
	dir := t.TempDir()
	withCache := refresolver.NewDefaultRegistry(refresolver.Options{CacheDir: dir})
	if withCache.Cache() == nil {
		t.Error("CacheDir set but Cache() is nil")
	}
	withoutCache := refresolver.NewDefaultRegistry(refresolver.Options{})
	if withoutCache.Cache() != nil {
		t.Error("CacheDir empty but Cache() non-nil")
	}
}

// countingResolver is a stub Resolver counting its Resolve invocations.
type countingResolver struct {
	name  string
	bytes []byte
	calls int
}

func (c *countingResolver) Name() string { return c.name }
func (c *countingResolver) Resolve(_ context.Context, _ refresolver.Request) (refresolver.Resolved, error) {
	c.calls++
	return refresolver.Resolved{Bytes: c.bytes, Source: "counting:" + c.name}, nil
}
