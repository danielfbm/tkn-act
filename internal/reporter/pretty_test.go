package reporter

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// formatResultValue truncates long values to 80 chars with an ellipsis.
// Truncating at the byte boundary `s[:max-1]` can land in the middle of
// a multibyte UTF-8 sequence, producing a malformed string and a stray
// replacement character once printed. The truncator must work in runes,
// not bytes. Regression for PR #18 reviewer Min-3.
func TestFormatResultValueRuneAwareTruncation(t *testing.T) {
	// Each Chinese char is 3 bytes in UTF-8, so 100 of them is 300 bytes
	// — well past the 80-char/rune ceiling. After truncation the result
	// must still be valid UTF-8 and end with the ellipsis.
	long := strings.Repeat("中", 100)
	got := formatResultValue(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated output should end with ellipsis: %q", got)
	}
	// The truncated rune count should be the 80-rune ceiling: 79 source
	// runes + 1 ellipsis rune = 80 runes total.
	if rc := utf8.RuneCountInString(got); rc != 80 {
		t.Errorf("rune count = %d, want 80", rc)
	}
}

// Sanity check that short strings (< ceiling) pass through unchanged
// in either byte- or rune-counting modes.
func TestFormatResultValueShortStringPassthrough(t *testing.T) {
	in := "hello"
	if got := formatResultValue(in); got != in {
		t.Errorf("short string: got %q, want %q", got, in)
	}
}

// ASCII at exactly the boundary should not be truncated.
func TestFormatResultValueAtBoundary(t *testing.T) {
	in := strings.Repeat("a", 80)
	if got := formatResultValue(in); got != in {
		t.Errorf("80-char string should pass through, got len=%d", len(got))
	}
}

// TestPrettyResolverEndPrintsLineWithDuration: a successful resolver-end
// surfaces as one indented line under the task name, mirroring the spec
// §13 layout. The line includes the resolver name, the source string,
// and the duration when the resolution wasn't cached.
func TestPrettyResolverEndPrintsLineWithDuration(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
		Status:   StatusSucceeded,
		Duration: 1234 * time.Millisecond,
		Source:   "git: github.com/foo/bar@abc123 → t.yaml",
	})
	out := buf.String()
	if !strings.Contains(out, "git") {
		t.Errorf("expected resolver name in pretty output: %q", out)
	}
	if !strings.Contains(out, "github.com/foo/bar") {
		t.Errorf("expected source in pretty output: %q", out)
	}
	if !strings.Contains(out, "1.234s") {
		t.Errorf("expected duration in pretty output: %q", out)
	}
}

// TestPrettyResolverEndCachedSuffix: a Cached=true resolver-end appends
// "(cached)" instead of a duration, so users can see at a glance that
// the run reused on-disk bytes. The suffix must appear on every backend
// (docker and cluster) — Phase 6 ships this consistently.
func TestPrettyResolverEndCachedSuffix(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Task: "build", Resolver: "hub",
		Status: StatusSucceeded,
		Cached: true,
		Source: "hub: build@0.1",
	})
	out := buf.String()
	if !strings.Contains(out, "(cached)") {
		t.Errorf("expected (cached) marker, got %q", out)
	}
}

// TestPrettyResolverEndQuietSuppresses: -q suppresses the resolver
// line entirely; only task / run summaries reach the user.
func TestPrettyResolverEndQuietSuppresses(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Quiet})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
		Status: StatusSucceeded, Source: "x",
	})
	if buf.String() != "" {
		t.Errorf("quiet mode should suppress resolver-end; got %q", buf.String())
	}
}

// TestPrettyResolverEndFailureSurfacesMessage: a failed resolver-end
// shows the message in red so the cause is obvious before the parent
// task-end fires.
func TestPrettyResolverEndFailureSurfacesMessage(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
		Status:  StatusFailed,
		Message: "clone failed: connection refused",
	})
	out := buf.String()
	if !strings.Contains(out, "clone failed") {
		t.Errorf("expected failure message in output: %q", out)
	}
}

// TestPrettyResolverStartVerboseShowsLine: at -v, resolver-start
// emits a tiny "starting" line so users see the dispatch begin
// before the network round-trip resolves.
func TestPrettyResolverStartVerboseShowsLine(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Verbose})
	rep.Emit(Event{
		Kind: EvtResolverStart, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
	})
	out := buf.String()
	if !strings.Contains(out, "git") || !strings.Contains(out, "starting") {
		t.Errorf("expected resolver-start verbose line; got %q", out)
	}
}

// TestPrettyResolverStartNormalSuppressed: at the default verbosity,
// resolver-start is silent — the resolver-end line carries the
// load-bearing info.
func TestPrettyResolverStartNormalSuppressed(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverStart, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
	})
	if buf.String() != "" {
		t.Errorf("normal mode resolver-start should be silent; got %q", buf.String())
	}
}

// TestPrettyResolverStartVerboseTopLevel: an empty Task field on the
// top-level pipelineRef path renders the friendly placeholder.
func TestPrettyResolverStartVerboseTopLevel(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Verbose})
	rep.Emit(Event{
		Kind: EvtResolverStart, Time: time.Unix(1, 0),
		Resolver: "git",
	})
	out := buf.String()
	if !strings.Contains(out, "(pipeline)") {
		t.Errorf("expected (pipeline) placeholder for empty task; got %q", out)
	}
}

// TestPrettyResolverEndNoSourceFallback: when Source is empty
// (e.g. a malformed resolver), pretty falls back to a short
// placeholder rather than emitting raw whitespace.
func TestPrettyResolverEndNoSourceFallback(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
		Status: StatusSucceeded,
	})
	out := buf.String()
	if !strings.Contains(out, "no source reported") {
		t.Errorf("expected fallback placeholder; got %q", out)
	}
}

// TestPrettyResolverEndTopLevelPipelineRefHasNoTaskPrefix: when Task is
// empty (the top-level pipelineRef.resolver eager-resolution path), the
// pretty line still renders without dangling whitespace.
func TestPrettyResolverEndTopLevelPipelineRefHasNoTaskPrefix(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtResolverEnd, Time: time.Unix(1, 0),
		Resolver: "git", // Task: ""
		Status:   StatusSucceeded,
		Source:   "pipeline ref",
	})
	out := buf.String()
	if !strings.Contains(out, "git") {
		t.Errorf("expected resolver name in top-level output: %q", out)
	}
	if strings.Contains(out, "/git") {
		t.Errorf("top-level resolver line shouldn't carry a task prefix: %q", out)
	}
}

// TestPretty_DebugLine: a debug event renders as one indented line with
// the [debug] tag, the component, sorted key=value pairs from Fields,
// and the human message after an em-dash separator. Indented to align
// with step-log output so visual flow is preserved.
func TestPretty_DebugLine(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind:      EvtDebug,
		Component: "resolver",
		Message:   "cache hit",
		Fields:    map[string]any{"ref": "hub://git-clone:0.9", "bytes": 4096},
	})
	out := buf.String()
	if !strings.Contains(out, "[debug]") {
		t.Errorf("missing [debug] tag: %q", out)
	}
	if !strings.Contains(out, "component=resolver") {
		t.Errorf("missing component=resolver: %q", out)
	}
	if !strings.Contains(out, "ref=hub://git-clone:0.9") {
		t.Errorf("missing ref field: %q", out)
	}
	if !strings.Contains(out, "bytes=4096") {
		t.Errorf("missing bytes field: %q", out)
	}
	if !strings.Contains(out, "cache hit") {
		t.Errorf("missing message: %q", out)
	}
	// Keys must render in sorted order (bytes before ref).
	if i, j := strings.Index(out, "bytes="), strings.Index(out, "ref="); i < 0 || j < 0 || i > j {
		t.Errorf("fields not sorted: %q", out)
	}
}

// TestPretty_DebugLine_NoFields: a debug event with no Fields still
// renders the [debug] tag, component, and message — no stray spacing
// or trailing artifacts from the empty map.
func TestPretty_DebugLine_NoFields(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind:      EvtDebug,
		Component: "engine",
		Message:   "task ready",
	})
	out := buf.String()
	if !strings.Contains(out, "[debug]") || !strings.Contains(out, "component=engine") || !strings.Contains(out, "task ready") {
		t.Errorf("debug-line render: %q", out)
	}
}

// TestPretty_DebugLine_Quiet: debug events are suppressed in Quiet
// verbosity (they're trace data, not summary output).
func TestPretty_DebugLine_Quiet(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Quiet})
	rep.Emit(Event{
		Kind:      EvtDebug,
		Component: "engine",
		Message:   "task ready",
	})
	if buf.Len() != 0 {
		t.Errorf("quiet should suppress debug events: %q", buf.String())
	}
}

// TestPretty_SidecarLine: a sidecar log line carries the diamond
// glyph (◊) so a reader scanning multi-task output can spot sidecar
// emission at a glance. The task:sidecar separator stays ":" (steps
// use "/"). Stream "sidecar-stderr" surfaces the yellow "!" marker.
func TestPretty_SidecarLine(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind:   EvtSidecarLog,
		Task:   "t",
		Step:   "redis",
		Stream: "sidecar-stdout",
		Line:   "ready",
	})
	out := buf.String()
	if !strings.Contains(out, "◊") {
		t.Errorf("missing ◊ glyph in sidecar line: %q", out)
	}
	if !strings.Contains(out, "t:redis") {
		t.Errorf("missing task:sidecar prefix: %q", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("missing log line content: %q", out)
	}
}

// TestPretty_SidecarLine_StderrShowsBangMarker: sidecar-stderr lines
// must surface the yellow "!" marker so failures are visible mixed
// in with stdout.
func TestPretty_SidecarLine_StderrShowsBangMarker(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind:   EvtSidecarLog,
		Task:   "t",
		Step:   "redis",
		Stream: "sidecar-stderr",
		Line:   "oom",
	})
	if !strings.Contains(buf.String(), "!") {
		t.Errorf("missing stderr ! marker: %q", buf.String())
	}
}

// TestPretty_SidecarLine_PreferDisplayName: when the sidecar carries
// a DisplayName the renderer prefers it over the raw step name (same
// convention as step-log).
func TestPretty_SidecarLine_PreferDisplayName(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind:        EvtSidecarLog,
		Task:        "t",
		Step:        "redis",
		DisplayName: "Redis Cache",
		Stream:      "sidecar-stdout",
		Line:        "ok",
	})
	if !strings.Contains(buf.String(), "Redis Cache") {
		t.Errorf("DisplayName not preferred: %q", buf.String())
	}
}

// TestPretty_Timestamps_StepLog: with PrettyOptions{Timestamps:true}
// step-log lines are prefixed with `[HH:MM:SS.mmm] ` (UTC). The
// prefix is omitted when the event's Time field is zero (never
// surface a bogus 00:00:00.000).
func TestPretty_Timestamps_StepLog(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal, Timestamps: true})
	rep.Emit(Event{
		Kind: EvtStepLog, Time: time.Date(2026, 5, 15, 9, 56, 13, 252_000_000, time.UTC),
		Task: "t", Step: "s", Line: "hi",
	})
	if !strings.Contains(buf.String(), "[09:56:13.252]") {
		t.Errorf("missing timestamp prefix on step-log: %q", buf.String())
	}
}

// TestPretty_Timestamps_OffByDefault: omitting Timestamps must keep
// the prefix off — the historical pretty output stays unchanged for
// users who don't opt in.
func TestPretty_Timestamps_OffByDefault(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal})
	rep.Emit(Event{
		Kind: EvtStepLog, Time: time.Date(2026, 5, 15, 9, 56, 13, 0, time.UTC),
		Task: "t", Step: "s", Line: "hi",
	})
	if strings.Contains(buf.String(), "09:56:13") {
		t.Errorf("unexpected timestamp prefix when not requested: %q", buf.String())
	}
}

// TestPretty_Timestamps_ZeroTimeSuppressed: an event with a zero
// Time must NOT render a `[00:00:00.000]` prefix — that would be
// misleading. Common case: tests that build events without a time
// stamp.
func TestPretty_Timestamps_ZeroTimeSuppressed(t *testing.T) {
	var buf bytes.Buffer
	rep := NewPretty(&buf, PrettyOptions{Verbosity: Normal, Timestamps: true})
	rep.Emit(Event{Kind: EvtStepLog, Task: "t", Step: "s", Line: "hi"})
	if strings.Contains(buf.String(), "[00:00:00.000]") {
		t.Errorf("zero Time produced a bogus timestamp prefix: %q", buf.String())
	}
}

// TestPretty_Timestamps_SidecarAndDebug: the prefix also lands on
// sidecar-log and debug lines, so all three "live" line types
// correlate against the same wall clock.
func TestPretty_Timestamps_SidecarAndDebug(t *testing.T) {
	now := time.Date(2026, 5, 15, 9, 56, 13, 252_000_000, time.UTC)

	var sidecarBuf bytes.Buffer
	NewPretty(&sidecarBuf, PrettyOptions{Verbosity: Normal, Timestamps: true}).Emit(Event{
		Kind: EvtSidecarLog, Time: now, Task: "t", Step: "redis",
		Stream: "sidecar-stdout", Line: "ok",
	})
	if !strings.Contains(sidecarBuf.String(), "[09:56:13.252]") {
		t.Errorf("missing timestamp on sidecar-log: %q", sidecarBuf.String())
	}

	var debugBuf bytes.Buffer
	NewPretty(&debugBuf, PrettyOptions{Verbosity: Normal, Timestamps: true}).Emit(Event{
		Kind: EvtDebug, Time: now, Component: "engine", Message: "task ready",
	})
	if !strings.Contains(debugBuf.String(), "[09:56:13.252]") {
		t.Errorf("missing timestamp on debug: %q", debugBuf.String())
	}
}
