package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

// runRoot invokes the root command with the given args, capturing stdout +
// stderr. It restores os.Stdout/Stderr and the global flags after the call.
func runRoot(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	saved := gf
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origOut, origErr
		gf = saved
	})

	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(wOut)
	cmd.SetErr(wErr)
	err = cmd.Execute()

	_ = wOut.Close()
	_ = wErr.Close()
	bo, _ := io.ReadAll(rOut)
	be, _ := io.ReadAll(rErr)

	return string(bo), string(be), err
}

func TestListJSON(t *testing.T) {
	repoRoot, _ := filepath.Abs("../../")
	dir := filepath.Join(repoRoot, "testdata/e2e/hello")
	stdout, _, err := runRoot(t, []string{"list", "-C", dir, "-o", "json"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got listResult
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("decode: %v\nout=%s", jerr, stdout)
	}
	sort.Strings(got.Pipelines)
	sort.Strings(got.Tasks)
	if len(got.Pipelines) != 1 || got.Pipelines[0] != "hello" {
		t.Errorf("pipelines = %v", got.Pipelines)
	}
	if len(got.Tasks) != 1 || got.Tasks[0] != "greet" {
		t.Errorf("tasks = %v", got.Tasks)
	}
}

func TestValidateJSON(t *testing.T) {
	repoRoot, _ := filepath.Abs("../../")
	dir := filepath.Join(repoRoot, "testdata/e2e/hello")
	stdout, _, err := runRoot(t, []string{"validate", "-C", dir, "-o", "json"})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	var got validateResult
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("decode: %v\nout=%s", jerr, stdout)
	}
	if !got.OK {
		t.Errorf("expected ok=true, got %+v", got)
	}
	if got.Pipeline != "hello" {
		t.Errorf("pipeline = %q", got.Pipeline)
	}
}

func TestVersionJSON(t *testing.T) {
	stdout, _, err := runRoot(t, []string{"version", "-o", "json"})
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	var got versionInfo
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("decode: %v\nout=%s", jerr, stdout)
	}
	if got.Name != "tkn-act" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Version == "" {
		t.Error("version is empty")
	}
}

func TestValidateExitCodeOnBadFile(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "broken.yaml")
	if werr := os.WriteFile(bad, []byte("not: tekton\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	_, _, err := runRoot(t, []string{"validate", "-f", bad, "-o", "json"})
	if err == nil {
		t.Fatal("expected error for broken yaml")
	}
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Errorf("exit code = %d, want %d", got, exitcode.Validate)
	}
}

func TestValidateNoFilesUsageCode(t *testing.T) {
	tmp := t.TempDir()
	_, _, err := runRoot(t, []string{"validate", "-C", tmp})
	if err == nil {
		t.Fatal("expected error when no Tekton YAML in dir")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d", got, exitcode.Usage)
	}
}

