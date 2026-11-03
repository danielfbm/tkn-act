package docker

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestUntarToDir_SymlinkChainEscapeRefused is the regression for a
// lexical-only symlink check: `l2 -> .` and `l1 -> l2/..` both look
// contained when joined lexically, but the kernel resolves l1 to the
// parent of dst, so a following `l1/evil` entry would land outside.
func TestUntarToDir_SymlinkChainEscapeRefused(t *testing.T) {
	parent := t.TempDir()
	dst := filepath.Join(parent, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "l2", Typeflag: tar.TypeSymlink, Linkname: "."},
		{Name: "l1", Typeflag: tar.TypeSymlink, Linkname: "l2/.."},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte("pwned\n")
	if err := tw.WriteHeader(&tar.Header{Name: "l1/evil", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	err := untarToDir(&buf, dst, "")
	if _, statErr := os.Stat(filepath.Join(parent, "evil")); statErr == nil {
		t.Fatalf("symlink-chain traversal wrote %s/evil outside dst", parent)
	}
	if err == nil {
		t.Errorf("untarToDir: want error refusing the escaping write, got nil")
	}
}

// TestUntarToDir_SymlinkedDirParentRefused covers a pre-existing
// symlink inside dst pointing outside it: an entry whose parent is
// that symlink must not be written through it.
func TestUntarToDir_SymlinkedDirParentRefused(t *testing.T) {
	parent := t.TempDir()
	dst := filepath.Join(parent, "dst")
	outside := filepath.Join(parent, "outside")
	for _, d := range []string{dst, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "link/f", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = untarToDir(&buf, dst, "")
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("write followed a symlinked parent outside dst")
	}
}

// TestUntarToDir_InRootSymlinkStillWorks guards against the fix being
// too strict: a symlinked directory that stays inside dst is fine.
func TestUntarToDir_InRootSymlinkStillWorks(t *testing.T) {
	dst := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "real/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "real"},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "alias/f", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := untarToDir(&buf, dst, ""); err != nil {
		t.Fatalf("untarToDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "real", "f"))
	if err != nil || string(got) != "ok" {
		t.Fatalf("real/f = %q, %v", got, err)
	}
}
