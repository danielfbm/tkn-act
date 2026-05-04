package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/refresolver"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was written. Mirrors the convention used elsewhere in the
// cmd/tkn-act/* tests.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	runErr := fn()
	_ = w.Close()
	<-done
	os.Stdout = old
	return buf.String(), runErr
}

// seedCache populates a DiskCache rooted at dir with two entries
// (one git, one hub) so the list/prune/clear commands have something
// to operate on.
func seedCache(t *testing.T, dir string) {
	t.Helper()
	c := refresolver.NewDiskCache(dir)
	if err := c.Put(refresolver.Request{Resolver: "git", Params: map[string]string{"url": "x", "revision": "main"}},
		refresolver.Resolved{Bytes: []byte("kind: Task\n"), Source: "git: x@main"}); err != nil {
		t.Fatalf("Put git: %v", err)
	}
	if err := c.Put(refresolver.Request{Resolver: "hub", Params: map[string]string{"name": "build", "version": "0.1"}},
		refresolver.Resolved{Bytes: []byte("kind: Task\n"), Source: "hub: build@0.1"}); err != nil {
		t.Fatalf("Put hub: %v", err)
	}
}

func TestCacheListPretty(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir)

	gf.output = "pretty"
	gf.resolverCacheDir = dir
	out, err := captureStdout(t, func() error {
		return runCacheList()
	})
	if err != nil {
		t.Fatalf("runCacheList: %v", err)
	}
	if !strings.Contains(out, "git") || !strings.Contains(out, "hub") {
		t.Errorf("expected resolver names in output: %q", out)
	}
}

func TestCacheListJSON(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir)

	gf.output = "json"
	gf.resolverCacheDir = dir
	out, err := captureStdout(t, func() error {
		return runCacheList()
	})
	if err != nil {
		t.Fatalf("runCacheList json: %v", err)
	}
	var got cacheListResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode json: %v\nout=%q", err, out)
	}
	if len(got.Entries) != 2 {
		t.Errorf("got %d entries, want 2: %+v", len(got.Entries), got.Entries)
	}
	if got.Root != dir {
		t.Errorf("root: got %q want %q", got.Root, dir)
	}
}

func TestCachePruneOlderThan(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir)

	// Backdate one entry by 2 hours.
	c := refresolver.NewDiskCache(dir)
	entries, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("seed: %d entries, want 2", len(entries))
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(entries[0].Path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	gf.output = "json"
	gf.resolverCacheDir = dir
	cachePruneOlder = time.Hour
	out, err := captureStdout(t, func() error {
		return runCachePrune()
	})
	if err != nil {
		t.Fatalf("runCachePrune: %v", err)
	}
	var res cachePruneResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode prune: %v\nout=%q", err, out)
	}
	if res.Pruned != 1 {
		t.Errorf("Pruned %d, want 1", res.Pruned)
	}
	// Survivors visible in List.
	left, _ := c.List()
	if len(left) != 1 {
		t.Errorf("left=%d, want 1", len(left))
	}
}

func TestCacheClearRequiresYes(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir)

	gf.output = "pretty"
	gf.resolverCacheDir = dir
	cacheYes = false
	if err := runCacheClear(); err == nil {
		t.Fatal("expected error without -y on cache clear")
	}

	cacheYes = true
	out, err := captureStdout(t, func() error {
		return runCacheClear()
	})
	if err != nil {
		t.Fatalf("runCacheClear -y: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "cleared") {
		t.Errorf("expected confirmation in output, got %q", out)
	}
	c := refresolver.NewDiskCache(dir)
	left, _ := c.List()
	if len(left) != 0 {
		t.Errorf("entries left after clear: %d", len(left))
	}
}

func TestCacheListEmptyRoot(t *testing.T) {
	dir := t.TempDir() + "/missing"
	gf.output = "json"
	gf.resolverCacheDir = dir
	out, err := captureStdout(t, func() error {
		return runCacheList()
	})
	if err != nil {
		t.Fatalf("runCacheList missing root: %v", err)
	}
	var got cacheListResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("expected zero entries on missing root, got %d", len(got.Entries))
	}
}
