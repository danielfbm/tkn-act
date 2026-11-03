package volumes_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
)

// Retries re-run MaterializeForTask for the same task name; each attempt
// must see a fresh emptyDir (Tekton: new Pod → new emptyDir), while
// hostPath contents are never touched.
func TestMaterializeEmptyDirIsFreshPerCall(t *testing.T) {
	base := t.TempDir()
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vs := []tektontypes.Volume{
		{Name: "scratch", EmptyDir: &tektontypes.EmptyDirSource{}},
		{Name: "h", HostPath: &tektontypes.HostPathSource{Path: host}},
	}
	hosts, err := volumes.MaterializeForTask("t", vs, base, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(hosts["scratch"], "stale")
	if err := os.WriteFile(stale, []byte("old attempt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := volumes.MaterializeForTask("t", vs, base, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale emptyDir file survived a second materialisation (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(host, "keep")); err != nil {
		t.Errorf("hostPath content must never be removed: %v", err)
	}
}

// ConfigMap / Secret volumes marked optional mount an empty dir when the
// source is missing (and skip missing item keys), instead of failing.
func TestMaterializeOptionalMissingSourcesMountEmpty(t *testing.T) {
	yes := true
	empty := volumes.NewStore("")
	hosts, err := volumes.MaterializeForTask("t", []tektontypes.Volume{
		{Name: "c", ConfigMap: &tektontypes.ConfigMapSource{Name: "absent", Optional: &yes}},
		{Name: "s", Secret: &tektontypes.SecretSource{SecretName: "absent", Optional: &yes}},
	}, t.TempDir(), empty, nil)
	if err != nil {
		t.Fatalf("optional missing sources must not fail: %v", err)
	}
	for _, n := range []string{"c", "s"} {
		entries, err := os.ReadDir(hosts[n])
		if err != nil || len(entries) != 0 {
			t.Errorf("%s: want empty existing dir, got entries=%v err=%v", n, entries, err)
		}
	}

	cm := volumes.NewStore("")
	cm.Add("present", "a", "1")
	hosts, err = volumes.MaterializeForTask("t", []tektontypes.Volume{{
		Name: "p",
		ConfigMap: &tektontypes.ConfigMapSource{Name: "present", Optional: &yes,
			Items: []tektontypes.KeyToPath{{Key: "a", Path: "a"}, {Key: "missing", Path: "m"}}},
	}}, t.TempDir(), cm, nil)
	if err != nil {
		t.Fatalf("optional missing item key must not fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hosts["p"], "a")); err != nil {
		t.Errorf("present key not projected: %v", err)
	}

	// Non-optional still fails.
	if _, err := volumes.MaterializeForTask("t", []tektontypes.Volume{
		{Name: "c", ConfigMap: &tektontypes.ConfigMapSource{Name: "absent"}},
	}, t.TempDir(), empty, nil); err == nil {
		t.Error("non-optional missing configMap must still fail")
	}
}
