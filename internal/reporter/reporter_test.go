package reporter_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

func TestJSONSinkEmitsOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, RunID: "r1"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, RunID: "r1", Task: "a"})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, RunID: "r1", Duration: time.Second})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	for i, l := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Errorf("line %d not JSON: %v: %q", i, err, l)
		}
	}
}

func TestPrettySinkRendersTree(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, false)
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, RunID: "r1", Pipeline: "p"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "a"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "a", Status: "succeeded", Duration: 100 * time.Millisecond})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "b", Status: "failed", Duration: 200 * time.Millisecond, Message: "step x exited 1"})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "failed", Duration: 350 * time.Millisecond})
	out := buf.String()
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("missing task names: %q", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("missing failed: %q", out)
	}
}
