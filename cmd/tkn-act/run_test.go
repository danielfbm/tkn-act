package main

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

// TestBuildVolumeStoresIngestsBundle asserts that bundle-loaded
// ConfigMap/Secret bytes flow into the Store and are visible via
// Resolve, while inline overrides still win on the same key.
func TestBuildVolumeStoresIngestsBundle(t *testing.T) {
	// Reset any global flag state from other tests in this package.
	gf = globalFlags{
		configMaps: []string{"app-config=greeting=from-inline"},
	}
	b := &loader.Bundle{
		ConfigMaps: map[string]map[string][]byte{
			"app-config": {
				"greeting": []byte("from-bundle"),
				"lang":     []byte("en"),
			},
			"other-cfg": {
				"k": []byte("v"),
			},
		},
		Secrets: map[string]map[string][]byte{
			"app-secret": {"token": []byte("hunter2")},
		},
	}

	cm, sec, err := buildVolumeStores(t.TempDir(), b)
	if err != nil {
		t.Fatalf("buildVolumeStores: %v", err)
	}
	got, err := cm.Resolve("app-config")
	if err != nil {
		t.Fatalf("resolve app-config: %v", err)
	}
	if s := string(got["greeting"]); s != "from-inline" {
		t.Errorf("greeting = %q, want from-inline (inline beats bundle)", s)
	}
	if s := string(got["lang"]); s != "en" {
		t.Errorf("lang = %q, want en (bundle-only key)", s)
	}
	got2, err := cm.Resolve("other-cfg")
	if err != nil {
		t.Fatalf("resolve other-cfg: %v", err)
	}
	if s := string(got2["k"]); s != "v" {
		t.Errorf("other-cfg.k = %q, want v", s)
	}
	gotSec, err := sec.Resolve("app-secret")
	if err != nil {
		t.Fatalf("resolve app-secret: %v", err)
	}
	if s := string(gotSec["token"]); s != "hunter2" {
		t.Errorf("token = %q, want hunter2 (bundle)", s)
	}
}

// TestBuildVolumeStoresNilBundleIsBackwardCompatible ensures the
// old call signature semantics survive when no `-f`-loaded
// resources are present.
func TestBuildVolumeStoresNilBundleIsBackwardCompatible(t *testing.T) {
	gf = globalFlags{}
	cm, sec, err := buildVolumeStores(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cm == nil || sec == nil {
		t.Fatal("expected non-nil stores")
	}
}

// TestRunFlagsResolverDefaults asserts that the resolver flags wire
// into globalFlags and that the default cache dir, allow-list, and
// offline mode are sane out of the box.
func TestRunFlagsResolverDefaults(t *testing.T) {
	// Reset
	gf = globalFlags{}
	cmd := newRootCmd()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The flags must exist (this is the scaffolding promise).
	if cmd.PersistentFlags().Lookup("resolver-cache-dir") == nil {
		t.Error("missing --resolver-cache-dir flag")
	}
	if cmd.PersistentFlags().Lookup("offline") == nil {
		t.Error("missing --offline flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-allow") == nil {
		t.Error("missing --resolver-allow flag")
	}
	if cmd.PersistentFlags().Lookup("remote-resolver-context") == nil {
		t.Error("missing --remote-resolver-context flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-config") == nil {
		t.Error("missing --resolver-config flag")
	}
	if cmd.PersistentFlags().Lookup("resolver-allow-insecure-http") == nil {
		t.Error("missing --resolver-allow-insecure-http flag")
	}

	// Default allow-list MUST match docs/AGENTS.md (Phase 1: git, hub,
	// http, bundles — cluster is opt-in).
	want := []string{"git", "hub", "http", "bundles"}
	if len(gf.resolverAllow) != len(want) {
		t.Errorf("resolverAllow = %v, want %v", gf.resolverAllow, want)
	}
	for i, n := range want {
		if i < len(gf.resolverAllow) && gf.resolverAllow[i] != n {
			t.Errorf("resolverAllow[%d] = %q, want %q", i, gf.resolverAllow[i], n)
		}
	}
	if gf.offline {
		t.Error("--offline default = true, want false")
	}
}

// TestResolverCacheDirDefault: when --resolver-cache-dir is empty, the
// CLI's helper resolves to $XDG_CACHE_HOME/tkn-act/resolved.
func TestResolverCacheDirDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-test")
	got := resolveResolverCacheDir("")
	want := "/tmp/xdg-test/tkn-act/resolved"
	if got != want {
		t.Errorf("resolverCacheDir = %q, want %q", got, want)
	}

	// Explicit override wins.
	got = resolveResolverCacheDir("/explicit/path")
	if got != "/explicit/path" {
		t.Errorf("explicit = %q, want /explicit/path", got)
	}
}
