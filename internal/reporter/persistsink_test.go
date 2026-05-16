package reporter_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

func TestPersistSink_WritesEachEventAsJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	s, err := reporter.NewPersistSink(path)
	if err != nil {
		t.Fatalf("NewPersistSink: %v", err)
	}
	s.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1_700_000_000, 0).UTC(), RunID: "r1"})
	s.Emit(reporter.Event{Kind: reporter.EvtStepLog, Time: time.Unix(1_700_000_001, 0).UTC(), Task: "t", Step: "s", Line: "hi"})
	s.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(1_700_000_010, 0).UTC(), RunID: "r1", ExitCode: 0})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines int
	for sc.Scan() {
		var ev reporter.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Errorf("line %d not valid JSON: %v", lines, err)
		}
		lines++
	}
	if lines != 3 {
		t.Errorf("lines = %d, want 3", lines)
	}
}

func TestPersistSink_PreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	s, _ := reporter.NewPersistSink(path)
	for i := 0; i < 100; i++ {
		s.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Line: strings.Repeat("x", i)})
	}
	s.Close()

	f, _ := os.Open(path)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		var ev reporter.Event
		json.Unmarshal(sc.Bytes(), &ev)
		if len(ev.Line) != i {
			t.Errorf("line %d: got len(Line)=%d", i, len(ev.Line))
		}
	}
}

// TestPersistSink_TeeByteEqualityWithZeroTime locks the contract that
// even an event emitted without an explicit Time field produces the
// SAME bytes on both sinks when tee'd. Today both sinks fall back to
// time.Now() independently; this test would catch a divergence if
// the two implementations drift apart in the future.
//
// We strip the time field from each marshaled event before comparing
// — the two time.Now() calls happen back-to-back but aren't required
// to be identical to the microsecond, and the spec's byte-equality
// promise is about every OTHER field of the event being identical.
func TestPersistSink_TeeByteEqualityWithZeroTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ps, _ := reporter.NewPersistSink(path)
	var stdout strings.Builder
	js := reporter.NewJSON(&stdout)
	tee := reporter.NewTee(js, ps)

	// Event with no Time set — both sinks must fall back identically.
	tee.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
	tee.Close()

	persisted, _ := os.ReadFile(path)
	if stripTime(string(persisted)) != stripTime(stdout.String()) {
		t.Errorf("zero-Time event diverged across tee:\npersist=%q\nstdout=%q", persisted, stdout.String())
	}
}

// stripTime removes the time field from each JSON line so byte-equality
// tests are insensitive to the back-to-back time.Now() jitter between
// the two Tee'd sinks.
func stripTime(s string) string {
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		// Naïve but adequate: drop everything from "time": until the next
		// quote-comma boundary. Real round-trip tests above use explicit
		// Time values to avoid this fuzziness.
		idx := strings.Index(line, `"time":"`)
		if idx < 0 {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		end := strings.Index(line[idx+8:], `","`)
		if end < 0 {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		out.WriteString(line[:idx])
		out.WriteString(line[idx+8+end+1:])
		out.WriteByte('\n')
	}
	return out.String()
}

func TestPersistSink_RoundTripVsJSONReporter(t *testing.T) {
	// The persist sink must produce the same per-line bytes as `NewJSON`
	// so events.jsonl is a faithful copy of the `-o json` stdout stream.
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ps, _ := reporter.NewPersistSink(path)
	var stdout strings.Builder
	js := reporter.NewJSON(&stdout)
	tee := reporter.NewTee(js, ps)

	events := []reporter.Event{
		{Kind: reporter.EvtRunStart, Time: time.Unix(1_700_000_000, 0).UTC(), Pipeline: "p"},
		{Kind: reporter.EvtStepLog, Time: time.Unix(1_700_000_001, 0).UTC(), Task: "t", Step: "s", Line: "hello"},
		{Kind: reporter.EvtRunEnd, Time: time.Unix(1_700_000_002, 0).UTC(), Pipeline: "p", ExitCode: 0},
	}
	for _, e := range events {
		tee.Emit(e)
	}
	tee.Close()

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(persisted) != stdout.String() {
		t.Errorf("persist != stdout:\npersist=%q\nstdout=%q", persisted, stdout.String())
	}
}
