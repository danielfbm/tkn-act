package main

import (
	"bytes"
	"encoding/json"
	"os"
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
	r.Finalize(time.Unix(1_700_000_002, 0), 0, "succeeded")
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

func TestLogs_NotFound(t *testing.T) {
	dir := t.TempDir()
	gf = globalFlags{output: "json", stateDir: dir}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error for empty state-dir")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (Usage)", got, exitcode.Usage)
	}
}

func TestLogs_BadStateDirCorrupts(t *testing.T) {
	// /dev/null/x is unopenable, so runstore.Open fails. The logs
	// subcommand should surface Env (3), not silently succeed.
	gf = globalFlags{output: "json", stateDir: "/dev/null/x"}
	err := runLogs(new(bytes.Buffer), "latest")
	if err == nil {
		t.Fatalf("want error for bad state-dir")
	}
	if got := exitcode.From(err); got != exitcode.Env {
		t.Errorf("exit code = %d, want %d (Env)", got, exitcode.Env)
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
	path := store.RunDir(entry) + "/events.jsonl"
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
