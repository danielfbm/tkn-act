package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/runstore"
)

// fixtureRun writes a 3-event run into stateDir and returns the
// ULID for assertions.
func fixtureRun(t *testing.T, stateDir string) string {
	t.Helper()
	s, err := runstore.Open(stateDir, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r, err := s.NewRun(time.Unix(1_700_000_000, 0), "hello", []string{"run", "-f", "hello.yaml"})
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	ps, _ := reporter.NewPersistSink(r.EventsPath())
	ps.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1_700_000_000, 0).UTC(), Pipeline: "hello"})
	ps.Emit(reporter.Event{Kind: reporter.EvtStepLog, Time: time.Unix(1_700_000_001, 0).UTC(), Task: "t1", Step: "s1", Line: "hello world"})
	ps.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(1_700_000_002, 0).UTC(), Pipeline: "hello", ExitCode: 0})
	ps.Close()
	if err := r.Finalize(time.Unix(1_700_000_002, 0), 0, "succeeded"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return string(r.ID)
}

func TestLogs_DefaultsToLatest_JSON(t *testing.T) {
	dir := t.TempDir()
	_ = fixtureRun(t, dir)
	gf = globalFlags{output: "json", stateDir: dir}

	var buf bytes.Buffer
	if err := runLogs(&buf, ""); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("emitted %d lines, want 3:\n%s", len(lines), buf.String())
	}
	for i, l := range lines {
		var ev reporter.Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
	}
}

func TestLogs_BySeq(t *testing.T) {
	dir := t.TempDir()
	_ = fixtureRun(t, dir) // seq 1
	_ = fixtureRun(t, dir) // seq 2
	gf = globalFlags{output: "json", stateDir: dir}

	var buf bytes.Buffer
	if err := runLogs(&buf, "1"); err != nil {
		t.Fatalf("runLogs(1): %v", err)
	}
	// seq=1 has the same 3 events as the second fixture; assert count.
	if got := strings.Count(strings.TrimSpace(buf.String()), "\n"); got != 2 {
		t.Errorf("seq 1 emitted %d newlines, want 2 (= 3 lines)", got)
	}
}

func TestLogs_ByULIDPrefix(t *testing.T) {
	dir := t.TempDir()
	id := fixtureRun(t, dir)
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	if err := runLogs(&buf, id[:10]); err != nil {
		t.Fatalf("runLogs(prefix): %v", err)
	}
	if !strings.Contains(buf.String(), `"pipeline":"hello"`) {
		t.Errorf("prefix replay missing run-start: %s", buf.String())
	}
}

func TestLogs_NotFound_LatestOnPresentButEmptyStateDir(t *testing.T) {
	// State-dir exists (with runs/ subdir, mimicking a prior `tkn-act
	// run` that finished but wrote nothing somehow) — "latest" with
	// no entries yields Usage (no matching run).
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	gf = globalFlags{output: "json", stateDir: dir}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error for empty state-dir")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (Usage)", got, exitcode.Usage)
	}
}

func TestLogs_NoStateDirYet_UsageNotEnv(t *testing.T) {
	// First-run UX: user invokes `tkn-act logs` before ever running
	// a pipeline. The state-dir doesn't exist. logs should NOT
	// create it (read path is read-only) and should return Usage
	// with a friendly "no runs recorded" message — not Env.
	dir := filepath.Join(t.TempDir(), "never-existed")
	gf = globalFlags{output: "json", stateDir: dir}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error when state-dir absent")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (Usage)", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "no runs recorded") {
		t.Errorf("error wording = %v", err)
	}
	// Crucial: confirm the side-effect-free promise — directory must
	// still not exist after the read.
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("logs created state-dir (%v); read path must be read-only", statErr)
	}
}

func TestLogs_CorruptIndex_ReturnsEnvNotUsage(t *testing.T) {
	// Corrupt index.json — Open succeeds (file exists, lock acquires)
	// but JSON decode fails. The classifier should map this to Env(3),
	// not Usage(2), because it's an on-disk-state failure rather than
	// a user-supplied-id failure.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	gf = globalFlags{output: "json", stateDir: dir}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error on corrupt index")
	}
	if got := exitcode.From(err); got != exitcode.Env {
		t.Errorf("exit code = %d, want %d (Env)", got, exitcode.Env)
	}
}

func TestLogs_UnreadableStateDir_ReturnsEnv(t *testing.T) {
	// /dev/null/x is a path under a non-directory ancestor — stat
	// returns ENOTDIR, which is NOT os.IsNotExist. Should map to
	// Env (filesystem can't satisfy the read), not Usage.
	gf = globalFlags{output: "json", stateDir: "/dev/null/x"}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error for unreadable state-dir")
	}
	if got := exitcode.From(err); got != exitcode.Env {
		t.Errorf("exit code = %d, want %d (Env)", got, exitcode.Env)
	}
}

func TestLogs_EmptyPositional_UsageNotSilentLatest(t *testing.T) {
	dir := t.TempDir()
	_ = fixtureRun(t, dir)
	gf = globalFlags{output: "json", stateDir: dir}
	// runLogs is the internal API where empty == latest by design;
	// but the cobra RunE handler should reject an explicit empty
	// positional. Exercise via Cobra:
	cmd := newLogsCmd()
	cmd.SetArgs([]string{""})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("want error on empty positional")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (Usage)", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty: %v", err)
	}
}

func TestLogs_CorruptEventsFile(t *testing.T) {
	dir := t.TempDir()
	id := fixtureRun(t, dir)
	// Corrupt the events.jsonl: append a bad line.
	gf = globalFlags{output: "json", stateDir: dir}
	bad := "{not json\n"
	store, _ := runstore.Open(dir, "test")
	entry, _ := store.Resolve(id)
	path := filepath.Join(store.RunDir(entry), "events.jsonl")
	if err := appendFile(t, path, []byte(bad)); err != nil {
		t.Fatalf("appendFile: %v", err)
	}
	err := runLogs(new(bytes.Buffer), id[:10])
	if err == nil {
		t.Fatalf("want error on corrupt events file")
	}
	if got := exitcode.From(err); got != exitcode.Env {
		t.Errorf("exit code = %d, want %d (Env)", got, exitcode.Env)
	}
}

func TestLogs_PrettyOutput(t *testing.T) {
	dir := t.TempDir()
	_ = fixtureRun(t, dir)
	gf = globalFlags{output: "pretty", color: "never", stateDir: dir}
	var buf bytes.Buffer
	if err := runLogs(&buf, ""); err != nil {
		t.Fatalf("runLogs: %v", err)
	}
	// Pretty output for a step-log line contains the step name AND
	// the literal log line.
	if !strings.Contains(buf.String(), "hello world") {
		t.Errorf("pretty output missing log line:\n%s", buf.String())
	}
}

func appendFile(t *testing.T, path string, b []byte) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}
