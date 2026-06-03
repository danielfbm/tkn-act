package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func TestValidateJSONOnLoadError(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "unsupported.yaml")
	if werr := os.WriteFile(bad, []byte(`
apiVersion: tekton.dev/v99
kind: Pipeline
metadata: {name: p}
spec: {}
`), 0o644); werr != nil {
		t.Fatal(werr)
	}
	stdout, _, err := runRoot(t, []string{"validate", "-f", bad, "-o", "json"})
	if err == nil {
		t.Fatal("expected error for unsupported apiVersion")
	}
	if got := exitcode.From(err); got != exitcode.Validate {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Validate)
	}
	var got validateResult
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil {
		t.Fatalf("decode: %v\nout=%s", jerr, stdout)
	}
	if got.OK {
		t.Fatalf("ok = true, want false; got %+v", got)
	}
	if len(got.Errors) == 0 {
		t.Fatalf("expected non-empty errors, got %+v", got)
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

func TestValidateEmptyFileMatchesRunNoPipelineMessage(t *testing.T) {
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty.yaml")
	if werr := os.WriteFile(empty, nil, 0o644); werr != nil {
		t.Fatal(werr)
	}

	_, _, validateErr := runRoot(t, []string{"validate", "-f", empty})
	if validateErr == nil {
		t.Fatal("expected validate error for empty file")
	}
	if got := exitcode.From(validateErr); got != exitcode.Usage {
		t.Fatalf("validate exit code = %d, want %d", got, exitcode.Usage)
	}
	if strings.Contains(validateErr.Error(), "multiple pipelines loaded") {
		t.Fatalf("validate error = %q, want no-pipeline message", validateErr.Error())
	}

	_, _, runErr := runRoot(t, []string{"run", "-f", empty})
	if runErr == nil {
		t.Fatal("expected run error for empty file")
	}
	if got := exitcode.From(runErr); got != exitcode.Usage {
		t.Fatalf("run exit code = %d, want %d", got, exitcode.Usage)
	}
	if validateErr.Error() != runErr.Error() {
		t.Fatalf("validate error = %q, run error = %q; want exact match", validateErr.Error(), runErr.Error())
	}
}
