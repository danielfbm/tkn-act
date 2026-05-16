package runstore_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/runstore"
)

type capture struct{ events []reporter.Event }

func (c *capture) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *capture) Close() error          { return nil }

func writeEventsFile(t *testing.T, dir string, events ...reporter.Event) string {
	t.Helper()
	path := filepath.Join(dir, "events.jsonl")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestReplay_StreamsEventsInOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeEventsFile(t, dir,
		reporter.Event{Kind: reporter.EvtRunStart, RunID: "r", Time: time.Unix(1, 0).UTC()},
		reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Line: "hi", Time: time.Unix(2, 0).UTC()},
		reporter.Event{Kind: reporter.EvtRunEnd, RunID: "r", ExitCode: 0, Time: time.Unix(3, 0).UTC()},
	)
	c := &capture{}
	if err := runstore.Replay(path, c); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(c.events) != 3 {
		t.Fatalf("events = %d, want 3", len(c.events))
	}
	if c.events[1].Line != "hi" {
		t.Errorf("event[1].Line = %q", c.events[1].Line)
	}
	if !c.events[0].Time.Equal(time.Unix(1, 0).UTC()) {
		t.Errorf("event[0].Time = %v", c.events[0].Time)
	}
}

func TestReplay_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &capture{}
	if err := runstore.Replay(path, c); err != nil {
		t.Errorf("Replay on empty file: %v", err)
	}
	if len(c.events) != 0 {
		t.Errorf("events = %d, want 0", len(c.events))
	}
}

func TestReplay_FileNotFound(t *testing.T) {
	c := &capture{}
	err := runstore.Replay(filepath.Join(t.TempDir(), "missing.jsonl"), c)
	if err == nil {
		t.Errorf("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("error wording = %v", err)
	}
}

func TestReplay_CorruptLineErrorsWithLineNumber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	good, _ := json.Marshal(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1, 0).UTC()})
	content := string(good) + "\n" + "{not json\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &capture{}
	err := runstore.Replay(path, c)
	if err == nil {
		t.Errorf("expected error on corrupt second line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should name the bad line, got: %v", err)
	}
	// The good first line should still have been emitted before the
	// decoder hit the bad line.
	if len(c.events) != 1 {
		t.Errorf("events emitted = %d, want 1", len(c.events))
	}
}

func TestReplay_LongLine(t *testing.T) {
	// events.jsonl can contain step-log lines up to 1 MiB. The decoder
	// must use a large enough scanner buffer to handle these.
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	huge := strings.Repeat("x", 700_000)
	good, _ := json.Marshal(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Line: huge, Time: time.Unix(1, 0).UTC()})
	if err := os.WriteFile(path, append(good, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &capture{}
	if err := runstore.Replay(path, c); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(c.events) != 1 || len(c.events[0].Line) != 700_000 {
		t.Errorf("got len=%d events, line len=%d", len(c.events), len(c.events[0].Line))
	}
}
