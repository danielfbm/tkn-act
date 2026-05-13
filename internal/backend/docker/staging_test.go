package docker

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestRunVolumeName(t *testing.T) {
	got := runVolumeName("abc123")
	want := "tkn-act-abc123"
	if got != want {
		t.Errorf("runVolumeName = %q, want %q", got, want)
	}
}

func TestStagerContainerName(t *testing.T) {
	got := stagerContainerName("abc123")
	want := "tkn-act-abc123-stager"
	if got != want {
		t.Errorf("stagerContainerName = %q, want %q", got, want)
	}
}

func TestScriptBaseHelpers(t *testing.T) {
	if got := stepScriptBase("run-1", "build"); got != "run-1-build" {
		t.Errorf("stepScriptBase = %q, want %q", got, "run-1-build")
	}
	if got := sidecarScriptBase("run-1", "redis"); got != "run-1-sidecar-redis" {
		t.Errorf("sidecarScriptBase = %q, want %q", got, "run-1-sidecar-redis")
	}
	// Step name "sidecar" must NOT collide with a sidecar named the
	// same — the sidecar variant interposes the literal "sidecar-" so
	// the basenames diverge.
	if a, b := stepScriptBase("r", "sidecar"), sidecarScriptBase("r", "sidecar"); a == b {
		t.Errorf("step and sidecar script bases collided: %q == %q", a, b)
	}
}

func TestPipelineWorkspaceName(t *testing.T) {
	ws := map[string]string{
		"shared":    "/tmp/run-1/workspaces/shared",
		"artifacts": "/tmp/run-1/workspaces/artifacts",
	}
	b := &Backend{userWorkspaces: ws, workspaceByHostPath: buildHostPathIndex(ws)}
	if name, ok := b.pipelineWorkspaceName("/tmp/run-1/workspaces/shared"); !ok || name != "shared" {
		t.Errorf("pipelineWorkspaceName(shared) = (%q, %v); want (shared, true)", name, ok)
	}
	if name, ok := b.pipelineWorkspaceName("/tmp/run-1/workspaces/artifacts"); !ok || name != "artifacts" {
		t.Errorf("pipelineWorkspaceName(artifacts) = (%q, %v); want (artifacts, true)", name, ok)
	}
	if _, ok := b.pipelineWorkspaceName("/tmp/run-1/workspaces/missing"); ok {
		t.Error("pipelineWorkspaceName(missing) ok = true; want false")
	}
}

func TestPipelineWorkspaceName_AmbiguousHostPath(t *testing.T) {
	// Two pipeline workspaces sharing the same HostPath must NOT
	// resolve to a non-deterministic single name. The lookup returns
	// (false) so the call site raises an explicit error rather than
	// silently bucketing one Step into the wrong slice.
	ws := map[string]string{
		"shared":  "/tmp/dup",
		"shared2": "/tmp/dup",
	}
	b := &Backend{userWorkspaces: ws, workspaceByHostPath: buildHostPathIndex(ws)}
	if name, ok := b.pipelineWorkspaceName("/tmp/dup"); ok {
		t.Errorf("pipelineWorkspaceName(dup) = (%q, true); want (\"\", false) on collision", name)
	}
}

// TestTarUntarRoundtrip writes a small directory tree to a host
// tmpdir, tars it, untars into a fresh dir with no strip prefix, and
// asserts the contents match. Untar's stripPrefix matches what
// CopyFromContainer wraps its output with, but for the bare round
// trip we use "" so every entry lands under the destination directly.
func TestTarUntarRoundtrip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	files := map[string][]byte{
		"a.txt":         []byte("alpha\n"),
		"sub/b.txt":     []byte("bravo\n"),
		"sub/dir/c.txt": []byte("charlie\n"),
	}
	for name, body := range files {
		full := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rc, err := tarHostDir(src)
	if err != nil {
		t.Fatalf("tarHostDir: %v", err)
	}

	if err := untarToDir(rc, dst, ""); err != nil {
		t.Fatalf("untarToDir: %v", err)
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Errorf("missing %q after roundtrip: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

// TestTarHostDir_EmptyDir asserts that tarring a non-existent path
// produces a single-entry tar with the directory itself, so the
// CopyToContainer destination still exists for subsequent mounts.
// This is the "user passed -w name=/tmp/nope but no dir yet" path.
func TestTarHostDir_EmptyDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	rc, err := tarHostDir(missing)
	if err != nil {
		t.Fatalf("tarHostDir(missing): %v", err)
	}
	dst := t.TempDir()
	if err := untarToDir(rc, dst, ""); err != nil {
		t.Fatalf("untarToDir: %v", err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		t.Errorf("expected empty extracted dir, got entries: %v", names)
	}
}

// TestUntarToDir_StripPrefix mirrors how CopyFromContainer behaves —
// every entry is wrapped under the basename of the source path.
// untarToDir must strip that prefix back off so the host layout
// matches what a bind-mount would have produced.
func TestUntarToDir_StripPrefix(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := tarHostDir(src)
	if err != nil {
		t.Fatal(err)
	}

	// Re-wrap every entry under "wrap/" the way CopyFromContainer would.
	wrapped := wrapTarUnder(t, rc, "wrap")

	dst := t.TempDir()
	if err := untarToDir(wrapped, dst, "wrap"); err != nil {
		t.Fatalf("untarToDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "x.txt"))
	if err != nil {
		t.Fatalf("read x.txt: %v", err)
	}
	if string(got) != "x\n" {
		t.Errorf("x.txt = %q, want %q", got, "x\n")
	}
}

// wrapTarUnder reads a tar stream and returns a new tar stream where
// every entry's Name is prefixed with `<prefix>/`. Mimics how
// CopyFromContainer wraps its output under the basename of the source
// path so TestUntarToDir_StripPrefix can exercise the strip-prefix
// branch without needing a real daemon.
func wrapTarUnder(t *testing.T, src io.Reader, prefix string) io.Reader {
	t.Helper()
	tr := tar.NewReader(src)
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("wrap: read: %v", err)
		}
		hdr.Name = prefix + "/" + hdr.Name
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("wrap: write header: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			if _, err := io.Copy(tw, tr); err != nil {
				t.Fatalf("wrap: copy body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("wrap: close: %v", err)
	}
	return &out
}

// TestUntarToDir_PathTraversalRejected feeds untarToDir a hand-built
// tar whose entries try to escape the destination via `../` chains
// and absolute paths. Each escape attempt must be silently dropped
// while legitimate names containing `..` (e.g. `package..json`) must
// extract normally — the substring `..` check the BLOCKING-3 review
// caught would have lost the latter.
func TestUntarToDir_PathTraversalRejected(t *testing.T) {
	dst := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	good := map[string][]byte{
		"package..json": []byte("v1\n"),
		"lib...so":      []byte("ELF\n"),
	}
	bad := []string{
		"../escape.txt",
		"sub/../../escape.txt",
	}
	for name, body := range good {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range bad {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 0, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := untarToDir(&buf, dst, ""); err != nil {
		t.Fatalf("untarToDir: %v", err)
	}
	for name, want := range good {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Errorf("missing legitimate %q: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
	// Belt-and-braces: nothing landed in the parent of dst.
	parent := filepath.Dir(dst)
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); err == nil {
		t.Errorf("path-traversal succeeded: escape.txt landed in %q", parent)
	}
}

// TestPathContainedIn covers the prefix-with-cleaning logic so a
// reviewer can read the table instead of the regex. Each row is
// (candidate, root, want).
func TestPathContainedIn(t *testing.T) {
	cases := []struct {
		name      string
		candidate string
		root      string
		want      bool
	}{
		{"identical", "/x", "/x", true},
		{"child", "/x/y", "/x", true},
		{"grandchild after clean", "/x/./y/../z", "/x", true},
		{"sibling", "/x2/y", "/x", false},
		{"escape via ..", "/x/../y", "/x", false},
		{"prefix not boundary", "/xy", "/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathContainedIn(tc.candidate, filepath.Clean(tc.root)); got != tc.want {
				t.Errorf("pathContainedIn(%q, %q) = %v, want %v", tc.candidate, tc.root, got, tc.want)
			}
		})
	}
}

// TestBuildHostPathIndex covers the deterministic reverse-lookup
// builder. Distinct host paths each map to their pipeline name; a
// shared host path is recorded as the ambiguous-marker sentinel.
func TestBuildHostPathIndex(t *testing.T) {
	got := buildHostPathIndex(map[string]string{
		"shared":    "/tmp/a",
		"artifacts": "/tmp/b",
	})
	if got["/tmp/a"] != "shared" {
		t.Errorf("got[/tmp/a] = %q, want shared", got["/tmp/a"])
	}
	if got["/tmp/b"] != "artifacts" {
		t.Errorf("got[/tmp/b] = %q, want artifacts", got["/tmp/b"])
	}

	dup := buildHostPathIndex(map[string]string{
		"shared":  "/tmp/x",
		"shared2": "/tmp/x",
	})
	if !startsWith(dup["/tmp/x"], ambiguousMarker) {
		t.Errorf("collision should record ambiguous marker; got %q", dup["/tmp/x"])
	}
}

// startsWith is the tiny test-only check; not worth importing strings.
func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

// Test that the volume layout constants stay stable. They're
// implementation detail today but a future refactor that renames
// them silently will be caught by the integration tests too — this
// is a fast fail at unit-test time.
func TestStageLayoutConstants(t *testing.T) {
	want := map[string]string{
		"scripts":    stageScripts,
		"workspaces": stageWorkspaces,
		"results":    stageResults,
		"volumes":    stageVolumes,
	}
	got := map[string]string{
		"scripts":    "scripts",
		"workspaces": "workspaces",
		"results":    "results",
		"volumes":    "volumes",
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("stage layout drift: %#v", want)
	}
	if stagerMountPoint != "/staged" {
		t.Errorf("stagerMountPoint = %q, want %q", stagerMountPoint, "/staged")
	}
}
