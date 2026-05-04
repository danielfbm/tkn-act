package refresolver_test

import (
	"context"
	"os"
	"path/filepath"
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
