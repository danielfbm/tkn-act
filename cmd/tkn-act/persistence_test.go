package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/runstore"
)

func TestOpenRunRecord_CreatesRunDir(t *testing.T) {
	dir := t.TempDir()
	var warn bytes.Buffer
	r := openRunRecord(&warn, dir, "hello", []string{"run", "-f", "hello.yaml"})
	if r == nil {
		t.Fatalf("openRunRecord returned nil; warnings=%q", warn.String())
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warnings: %q", warn.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", string(r.ID))); err != nil {
		t.Errorf("run dir missing: %v", err)
	}
	if _, err := runstore.ReadMeta(filepath.Join(dir, "runs", string(r.ID), "meta.json")); err != nil {
		t.Errorf("meta.json missing: %v", err)
	}
}

func TestOpenRunRecord_FailSoftOnUnwritableStateDir(t *testing.T) {
	// /dev/null/x is guaranteed unopenable as a directory.
	var warn bytes.Buffer
	r := openRunRecord(&warn, "/dev/null/cannot-create", "hello", nil)
	if r != nil {
		t.Errorf("openRunRecord should have returned nil on bad state-dir")
	}
	if !strings.Contains(warn.String(), "state-dir") {
		t.Errorf("expected state-dir warning, got: %q", warn.String())
	}
}

func TestFinalizeRun_SuccessRecordsSucceeded(t *testing.T) {
	dir := t.TempDir()
	store, _ := runstore.Open(dir, "tkn-act-test")
	r, _ := store.NewRun(time.Unix(1_700_000_000, 0), "p", nil)
	finalizeRun(io.Discard, r, nil)
	m, _ := runstore.ReadMeta(filepath.Join(r.Dir, "meta.json"))
	if m.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", m.Status)
	}
	if m.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", m.ExitCode)
	}
	if m.EndedAt.IsZero() {
		t.Errorf("EndedAt is zero")
	}
}

func TestFinalizeRun_FailureCapturesExitCode(t *testing.T) {
	dir := t.TempDir()
	store, _ := runstore.Open(dir, "tkn-act-test")
	r, _ := store.NewRun(time.Unix(1_700_000_000, 0), "p", nil)
	wrapped := exitcode.Wrap(exitcode.Pipeline, errors.New("boom"))
	finalizeRun(io.Discard, r, wrapped)
	m, _ := runstore.ReadMeta(filepath.Join(r.Dir, "meta.json"))
	if m.Status != "failed" {
		t.Errorf("Status = %q, want failed", m.Status)
	}
	if m.ExitCode != exitcode.Pipeline {
		t.Errorf("ExitCode = %d, want %d", m.ExitCode, exitcode.Pipeline)
	}
}

func TestWrapReporterWithPersist_NilRunReturnsLive(t *testing.T) {
	var warn bytes.Buffer
	live := reporter.NewJSON(new(bytes.Buffer))
	rep, closer := wrapReporterWithPersist(&warn, live, nil)
	if rep != live {
		t.Errorf("expected live reporter to be returned unchanged")
	}
	if closer != nil {
		t.Errorf("closer should be nil when run is nil")
	}
}

func TestWrapReporterWithPersist_TeesAndFlushes(t *testing.T) {
	dir := t.TempDir()
	store, _ := runstore.Open(dir, "tkn-act-test")
	r, _ := store.NewRun(time.Now(), "p", nil)

	var liveBuf bytes.Buffer
	live := reporter.NewJSON(&liveBuf)
	var warn bytes.Buffer
	rep, closer := wrapReporterWithPersist(&warn, live, r)
	if closer == nil {
		t.Fatalf("closer should not be nil when run is set")
	}
	rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1, 0).UTC(), Pipeline: "p"})
	rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(2, 0).UTC(), Pipeline: "p", ExitCode: 0})
	if err := closer(); err != nil {
		t.Errorf("closer returned error: %v", err)
	}

	persisted, err := os.ReadFile(r.EventsPath())
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if string(persisted) != liveBuf.String() {
		t.Errorf("persist (%q) != live (%q)", persisted, liveBuf.String())
	}
	// Confirm both lines are decodable.
	for i, line := range strings.Split(strings.TrimSpace(string(persisted)), "\n") {
		var e reporter.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
	}
}

func TestWrapReporterWithPersist_FailSoftWhenEventsFileUnopenable(t *testing.T) {
	bad := &runstore.Run{
		ID:  "01HQYZAB0000000000000000RR",
		Seq: 1,
		Dir: "/dev/null/x",
	}
	var liveBuf bytes.Buffer
	live := reporter.NewJSON(&liveBuf)
	var warn bytes.Buffer
	rep, closer := wrapReporterWithPersist(&warn, live, bad)
	if rep != live {
		t.Errorf("expected fall-back to live-only reporter")
	}
	if closer != nil {
		t.Errorf("closer should be nil when persist sink failed to open")
	}
	if !strings.Contains(warn.String(), "events file") {
		t.Errorf("expected events-file warning, got %q", warn.String())
	}
}

// setupRunPersistence is the single helper runWith calls. It exercises
// openRunRecord + wrapReporterWithPersist + finalizeRun together.

func TestSetupRunPersistence_HappyPath(t *testing.T) {
	dir := t.TempDir()
	var warn bytes.Buffer
	var liveBuf bytes.Buffer
	live := reporter.NewJSON(&liveBuf)
	rep, cleanup := setupRunPersistence(&warn, dir, "hello", []string{"run", "-f", "hello.yaml"}, live)
	if rep == live {
		t.Errorf("expected Tee'd reporter when persistence is available")
	}
	rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1, 0).UTC(), Pipeline: "hello"})
	rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(2, 0).UTC(), Pipeline: "hello", ExitCode: 0})
	cleanup(nil)

	entries, err := os.ReadDir(filepath.Join(dir, "runs"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 run dir; got %d (err=%v)", len(entries), err)
	}
	runDir := filepath.Join(dir, "runs", entries[0].Name())
	m, err := runstore.ReadMeta(filepath.Join(runDir, "meta.json"))
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if m.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", m.Status)
	}
	events, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if string(events) != liveBuf.String() {
		t.Errorf("events.jsonl != stdout JSON")
	}
}

func TestSetupRunPersistence_FailSoftWhenStateDirBad(t *testing.T) {
	var warn bytes.Buffer
	var liveBuf bytes.Buffer
	live := reporter.NewJSON(&liveBuf)
	rep, cleanup := setupRunPersistence(&warn, "/dev/null/x", "hello", nil, live)
	if rep != live {
		t.Errorf("expected fall-back to live reporter when state-dir bad")
	}
	// cleanup must be a no-op rather than panic.
	cleanup(errors.New("ignored"))
	if !strings.Contains(warn.String(), "state-dir") {
		t.Errorf("expected state-dir warning")
	}
}

func TestSetupRunPersistence_PropagatesFailure(t *testing.T) {
	dir := t.TempDir()
	var warn bytes.Buffer
	var liveBuf bytes.Buffer
	rep, cleanup := setupRunPersistence(&warn, dir, "p", nil, reporter.NewJSON(&liveBuf))
	rep.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1, 0), Pipeline: "p"})
	cleanup(exitcode.Wrap(exitcode.Timeout, errors.New("ran long")))

	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	runDir := filepath.Join(dir, "runs", entries[0].Name())
	m, _ := runstore.ReadMeta(filepath.Join(runDir, "meta.json"))
	if m.Status != "failed" {
		t.Errorf("Status = %q, want failed", m.Status)
	}
	if m.ExitCode != exitcode.Timeout {
		t.Errorf("ExitCode = %d, want %d", m.ExitCode, exitcode.Timeout)
	}
}
