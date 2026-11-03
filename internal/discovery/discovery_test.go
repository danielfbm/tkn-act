package discovery_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/discovery"
)

func TestFindsPipelineYAMLAtRoot(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "pipeline.yaml"), []byte("x"), 0o644))
	got, err := discovery.Find(dir)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "pipeline.yaml" {
		t.Errorf("got %v", got)
	}
}

func TestFindsTektonDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, ".tekton")
	must(t, os.MkdirAll(sub, 0o755))
	must(t, os.WriteFile(filepath.Join(sub, "task.yaml"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(sub, "pipeline.yaml"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(sub, "ignored.txt"), []byte("x"), 0o644))
	got, err := discovery.Find(dir)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d files, want 2: %v", len(got), got)
	}
}

func TestNoFilesIsError(t *testing.T) {
	_, err := discovery.Find(t.TempDir())
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestMissingDirIsNotNoneFound(t *testing.T) {
	_, err := discovery.Find(filepath.Join(t.TempDir(), "no-such-dir"))
	if err == nil || errors.Is(err, discovery.ErrNoneFound) {
		t.Fatalf("missing dir must be a distinct error, got %v", err)
	}
	f := filepath.Join(t.TempDir(), "file")
	must(t, os.WriteFile(f, []byte("x"), 0o644))
	if _, err := discovery.Find(f); err == nil || errors.Is(err, discovery.ErrNoneFound) {
		t.Fatalf("file as dir must be a distinct error, got %v", err)
	}
	if _, err := discovery.Find(t.TempDir()); !errors.Is(err, discovery.ErrNoneFound) {
		t.Fatalf("empty dir: want ErrNoneFound, got %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
