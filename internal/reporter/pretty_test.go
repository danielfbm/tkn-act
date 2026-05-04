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
