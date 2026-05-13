package agentguide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSrc lays down a src tree with one file per entry in Order
// (README.md for "overview"). Body marks each section so tests can
// confirm content lands in the right place.
func writeSrc(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, section := range Order {
		path := filepath.Join(root, FileName(section))
		body := "## " + section + "\n\nhello from " + section + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerateHappyPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, section := range Order {
		path := filepath.Join(dst, FileName(section))
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.HasSuffix(string(b), "\n") {
			t.Errorf("%s: expected trailing newline", path)
		}
		if strings.HasSuffix(string(b), "\n\n") {
			t.Errorf("%s: expected exactly one trailing newline, got >1", path)
		}
		if !strings.Contains(string(b), "hello from "+section) {
			t.Errorf("%s: missing body marker", path)
		}
	}
}

func TestGenerateIdempotent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate #1: %v", err)
	}
	stale := time.Unix(1700000000, 0)
	for _, section := range Order {
		path := filepath.Join(dst, FileName(section))
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate #2: %v", err)
	}

	for _, section := range Order {
		path := filepath.Join(dst, FileName(section))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stale) {
			t.Errorf("%s was rewritten on idempotent rerun (mtime moved from %v to %v)", path, stale, info.ModTime())
		}
	}
}

func TestGenerateMissingSourceFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	target := filepath.Join(src, FileName("matrix"))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	err := Generate(src, dst)
	if err == nil {
		t.Fatal("expected error for missing file; got nil")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "matrix.md") {
		t.Errorf("error %q should mention missing matrix.md", err)
	}
}

func TestGenerateExtraSourceFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	extra := filepath.Join(src, "extra-section.md")
	if err := os.WriteFile(extra, []byte("## extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Generate(src, dst)
	if err == nil {
		t.Fatal("expected error for extra file; got nil")
	}
	if !strings.Contains(err.Error(), "extra") || !strings.Contains(err.Error(), "extra-section.md") {
		t.Errorf("error %q should mention extra-section.md", err)
	}
}

func TestGenerateRemovesStaleDstMD(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dst, "renamed-old.md")
	if err := os.WriteFile(stale, []byte("## old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .md file %s should have been removed; stat err=%v", stale, err)
	}
}

func TestGenerateRemovesStaleDstNonMD(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(dst, "leftover.txt")
	if err := os.WriteFile(junk, []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	junkDir := filepath.Join(dst, "abandoned-subdir")
	if err := os.MkdirAll(junkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	junkChild := filepath.Join(junkDir, "child.md")
	if err := os.WriteFile(junkChild, []byte("## child\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Errorf("non-md leftover should be swept; %s still present (err=%v)", junk, err)
	}
	if _, err := os.Stat(junkDir); !os.IsNotExist(err) {
		t.Errorf("leftover subdirectory should be swept; %s still present (err=%v)", junkDir, err)
	}
}

func TestGenerateNormalizesTrailingWhitespace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, section := range Order {
		path := filepath.Join(src, FileName(section))
		body := "## " + section + "\n\nhello\n\n\n   \n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := Generate(src, dst); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	path := filepath.Join(dst, FileName("overview"))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "hello\n") {
		t.Errorf("trailing whitespace not normalized: %q", string(b))
	}
}
