package volumes_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
)

func TestStoreInlineWinsOverDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cfg", "k"), []byte("from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := volumes.NewStore(dir)
	s.Add("cfg", "k", "from-inline")
	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["k"]) != "from-inline" {
		t.Errorf("inline did not win: %s", got["k"])
	}
}

func TestStoreReadsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cfg", "greeting"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := volumes.NewStore(dir)
	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["greeting"]) != "hello" {
		t.Errorf("got %q", got["greeting"])
	}
}

func TestStoreMissingErrors(t *testing.T) {
	s := volumes.NewStore(t.TempDir())
	_, err := s.Resolve("nope")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "no keys") {
		t.Errorf("err = %v", err)
	}
}

func TestMaterializeEmptyDir(t *testing.T) {
	base := t.TempDir()
	hosts, err := volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name:     "scratch",
		EmptyDir: &tektontypes.EmptyDirSource{},
	}}, base, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(hosts["scratch"])
	if err != nil || !st.IsDir() {
		t.Fatalf("expected scratch dir; got %v err=%v", st, err)
	}
}

func TestMaterializeHostPath(t *testing.T) {
	hosts, err := volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name:     "data",
		HostPath: &tektontypes.HostPathSource{Path: "/etc"},
	}}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hosts["data"] != "/etc" {
		t.Errorf("got %q", hosts["data"])
	}
}

func TestMaterializeConfigMap(t *testing.T) {
	cm := volumes.NewStore("")
	cm.Add("greeter", "msg", "hello")
	cm.Add("greeter", "lang", "en")
	hosts, err := volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name:      "g",
		ConfigMap: &tektontypes.ConfigMapSource{Name: "greeter"},
	}}, t.TempDir(), cm, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := hosts["g"]
	for _, key := range []string{"msg", "lang"} {
		data, err := os.ReadFile(filepath.Join(d, key))
		if err != nil {
			t.Errorf("read %q: %v", key, err)
		}
		if len(data) == 0 {
			t.Errorf("empty file for %q", key)
		}
	}
}

func TestMaterializeConfigMapWithItems(t *testing.T) {
	cm := volumes.NewStore("")
	cm.Add("g", "msg", "hello")
	cm.Add("g", "ignored", "noise")
	hosts, err := volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name: "v",
		ConfigMap: &tektontypes.ConfigMapSource{
			Name:  "g",
			Items: []tektontypes.KeyToPath{{Key: "msg", Path: "renamed"}},
		},
	}}, t.TempDir(), cm, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := hosts["v"]
	if _, err := os.Stat(filepath.Join(d, "renamed")); err != nil {
		t.Errorf("expected renamed file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d, "ignored")); err == nil {
		t.Errorf("did not expect 'ignored' file (items projection)")
	}
}

func TestStoreBundleIsLowestPrecedence(t *testing.T) {
	dir := t.TempDir()
	// On-disk dir: cfg/k = from-disk
	if err := os.MkdirAll(filepath.Join(dir, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cfg", "k"), []byte("from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := volumes.NewStore(dir)
	// Bundle (lowest) loads cfg/k = from-bundle
	s.LoadBytes("cfg", map[string][]byte{"k": []byte("from-bundle")})

	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["k"]) != "from-disk" {
		t.Errorf("k = %q, want from-disk (dir beats bundle)", got["k"])
	}

	// Inline (highest) overrides both
	s.Add("cfg", "k", "from-inline")
	got, err = s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["k"]) != "from-inline" {
		t.Errorf("k = %q, want from-inline (inline beats dir beats bundle)", got["k"])
	}
}

// TestStoreResetClearsInlineAndBundle covers the test-harness affordance
// added so the cluster-e2e harness can keep one *Store across the
// fixture table without inline / bundle entries from one fixture
// shadowing the next. Dir is deliberately preserved (it's on-disk
// state, not in-memory mutable surface).
func TestStoreResetClearsInlineAndBundle(t *testing.T) {
	dir := t.TempDir()
	s := volumes.NewStore(dir)
	s.Add("cfg", "k", "from-inline")
	s.LoadBytes("cfg", map[string][]byte{"k": []byte("from-bundle")})

	s.Reset()

	if got, err := s.Resolve("cfg"); err == nil {
		t.Errorf("after Reset, Resolve returned %v; want error (no keys)", got)
	}
	if s.Dir != dir {
		t.Errorf("Reset wiped Dir = %q; want %q (Reset must not touch on-disk root)", s.Dir, dir)
	}
	// Adding a fresh entry after Reset must work — Reset must
	// re-initialize the maps, not nil them out.
	s.Add("cfg", "k", "after-reset")
	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatalf("after Reset+Add, Resolve: %v", err)
	}
	if string(got["k"]) != "after-reset" {
		t.Errorf("after Reset+Add, k = %q, want after-reset", got["k"])
	}
}

func TestStoreBundleOnlyResolves(t *testing.T) {
	s := volumes.NewStore("")
	s.LoadBytes("cfg", map[string][]byte{
		"a": []byte("alpha"),
		"b": []byte("beta"),
	})
	got, err := s.Resolve("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["a"]) != "alpha" || string(got["b"]) != "beta" {
		t.Errorf("got %+v, want a=alpha b=beta", got)
	}
}

func TestMaterializeRejectsPathTraversal(t *testing.T) {
	cm := volumes.NewStore("")
	cm.Add("g", "k", "v")
	_, err := volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name: "v",
		ConfigMap: &tektontypes.ConfigMapSource{
			Name:  "g",
			Items: []tektontypes.KeyToPath{{Key: "k", Path: "../escape"}},
		},
	}}, t.TempDir(), cm, nil)
	if err == nil || !strings.Contains(err.Error(), "must be a relative path") {
		t.Errorf("err = %v", err)
	}
}
