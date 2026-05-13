package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/cmd/tkn-act/internal/agentguide"
)

// writeSrc lays down a src tree with one file per entry in
// agentguide.Order, content = section name. README.md for "overview".
func writeSrc(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, section := range agentguide.Order {
		path := filepath.Join(root, agentguide.FileName(section))
		body := "## " + section + "\n\nhello from " + section + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunHappyPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, section := range agentguide.Order {
		path := filepath.Join(dst, agentguide.FileName(section))
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

func TestRunIdempotent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	if err := run(src, dst); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	// Backdate the destination files; the generator should skip the
	// rewrite on the second run because source bytes are unchanged.
	stale := time.Unix(1700000000, 0)
	for _, section := range agentguide.Order {
		path := filepath.Join(dst, agentguide.FileName(section))
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	if err := run(src, dst); err != nil {
		t.Fatalf("run #2: %v", err)
	}

	for _, section := range agentguide.Order {
		path := filepath.Join(dst, agentguide.FileName(section))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stale) {
			t.Errorf("%s was rewritten on idempotent rerun (mtime moved from %v to %v)", path, stale, info.ModTime())
		}
	}
}

func TestRunMissingSourceFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	// Delete one expected file.
	target := filepath.Join(src, agentguide.FileName("matrix"))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	err := run(src, dst)
	if err == nil {
		t.Fatal("expected error for missing file; got nil")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "matrix.md") {
		t.Errorf("error %q should mention missing matrix.md", err)
	}
}

func TestRunExtraSourceFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSrc(t, src)

	// Add an extra .md not in the order.
	extra := filepath.Join(src, "extra-section.md")
	if err := os.WriteFile(extra, []byte("## extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run(src, dst)
	if err == nil {
		t.Fatal("expected error for extra file; got nil")
	}
	if !strings.Contains(err.Error(), "extra") || !strings.Contains(err.Error(), "extra-section.md") {
		t.Errorf("error %q should mention extra-section.md", err)
	}
}

func TestRunRemovesStaleDstFile(t *testing.T) {
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

	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file %s should have been removed; stat err=%v", stale, err)
	}
}

func TestRunNormalizesTrailingWhitespace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, section := range agentguide.Order {
		path := filepath.Join(src, agentguide.FileName(section))
		body := "## " + section + "\n\nhello\n\n\n   \n" // extra trailing whitespace
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}

	path := filepath.Join(dst, agentguide.FileName("overview"))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "hello\n") {
		t.Errorf("trailing whitespace not normalized: %q", string(b))
	}
}
