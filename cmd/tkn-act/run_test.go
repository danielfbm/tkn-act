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
