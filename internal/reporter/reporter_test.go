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

// TestJSONSinkPreservesStepLogOrder is the shape AI agents depend on.
func TestJSONSinkPreservesStepLogOrder(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	for i, line := range []string{"alpha", "beta", "gamma"} {
		r.Emit(reporter.Event{
			Kind: reporter.EvtStepLog, Task: "t", Step: "s",
			Stream: "stdout", Line: line, Time: time.Unix(int64(i), 0),
		})
	}
	got := []string{}
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = append(got, v["line"].(string))
	}
	if want := []string{"alpha", "beta", "gamma"}; !equalStrings(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestPrettyRendersTaskAndRunSummaries(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{})
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, RunID: "r1", Pipeline: "p"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "a"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "a", Status: "succeeded", Duration: 100 * time.Millisecond})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "b", Status: "failed", Duration: 200 * time.Millisecond, Message: "step x exited 1"})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "failed", Duration: 350 * time.Millisecond})
	out := buf.String()
	for _, want := range []string{"a", "b", "failed", "PipelineRun"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestPrettyStreamsLogsInArrivalOrder is the must-have UX contract: step logs
// must appear in the order they were emitted, even when interleaved across
// parallel tasks.
func TestPrettyStreamsLogsInArrivalOrder(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{})
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "build"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "test"})
	want := []string{
		"build-1",
		"test-1",
		"build-2",
		"test-2",
	}
	for i, line := range want {
		task := "build"
		if i%2 == 1 {
			task = "test"
		}
		r.Emit(reporter.Event{
			Kind: reporter.EvtStepLog, Task: task, Step: "main",
			Stream: "stdout", Line: line,
		})
	}
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "build", Status: "succeeded", Duration: time.Second})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "test", Status: "succeeded", Duration: time.Second})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded", Duration: time.Second})

	out := buf.String()
	// Sanity: every line we emitted must appear, in order.
	last := 0
	for _, line := range want {
		idx := strings.Index(out[last:], line)
		if idx < 0 {
			t.Fatalf("line %q not found at or after %d:\n%s", line, last, out)
		}
		last += idx + len(line)
	}
	// And every log line must be prefixed by its task — that's how the user
	// disambiguates parallel tasks.
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "build-"):
			if !strings.Contains(l, "build/main") {
				t.Errorf("log line %q missing task/step prefix", l)
			}
		case strings.Contains(l, "test-"):
			if !strings.Contains(l, "test/main") {
				t.Errorf("log line %q missing task/step prefix", l)
			}
		}
	}
}

func TestPrettyQuietSuppressesLogsAndHeader(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Quiet})
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
	r.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Line: "noisy"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t", Status: "succeeded", Duration: time.Second})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded", Duration: time.Second})
	out := buf.String()
	if strings.Contains(out, "noisy") {
		t.Errorf("quiet mode leaked step log: %s", out)
	}
	if strings.Contains(out, "▶") {
		t.Errorf("quiet mode emitted pipeline header: %s", out)
	}
	if !strings.Contains(out, "PipelineRun") {
		t.Errorf("quiet mode dropped run summary: %s", out)
	}
	if !strings.Contains(out, "t ") { // task summary still shown
		t.Errorf("quiet mode dropped task summary: %s", out)
	}
}

func TestPrettyVerboseShowsStepBoundaries(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Verbose})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Task: "t"})
	r.Emit(reporter.Event{Kind: reporter.EvtStepStart, Task: "t", Step: "s"})
	r.Emit(reporter.Event{Kind: reporter.EvtStepEnd, Task: "t", Step: "s", ExitCode: 0})
	out := buf.String()
	if !strings.Contains(out, "started") || !strings.Contains(out, "finished (exit 0)") {
		t.Errorf("verbose missing step boundaries:\n%s", out)
	}
}

func TestPrettyColorEmitsAnsiOnlyWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		color    bool
		wantAnsi bool
	}{
		{"on", true, true},
		{"off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: tc.color})
			r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
			r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t", Status: "succeeded", Duration: time.Second})
			r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded", Duration: time.Second})
			gotAnsi := strings.Contains(buf.String(), "\033[")
			if gotAnsi != tc.wantAnsi {
				t.Errorf("ansi=%v, want %v\n%s", gotAnsi, tc.wantAnsi, buf.String())
			}
		})
	}
}

func TestPrettyFailedTaskShowsRedMessage(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: true})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t", Status: "failed", Duration: time.Second, Message: "exited 1"})
	out := buf.String()
	if !strings.Contains(out, "\033[31m") {
		t.Errorf("expected red ANSI for failed message, got:\n%s", out)
	}
	if !strings.Contains(out, "exited 1") {
		t.Errorf("missing message text:\n%s", out)
	}
}

func TestPrettyStderrLineMarked(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{})
	r.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Stream: "stderr", Line: "boom"})
	r.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Stream: "stdout", Line: "ok"})
	out := buf.String()
	stderrLine, stdoutLine := "", ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "boom") {
			stderrLine = l
		}
		if strings.Contains(l, "ok") {
			stdoutLine = l
		}
	}
	if !strings.Contains(stderrLine, "!") {
		t.Errorf("stderr line missing marker: %q", stderrLine)
	}
	if strings.Contains(stdoutLine, "!") {
		t.Errorf("stdout line gained stderr marker: %q", stdoutLine)
	}
}

// --- color resolution -----------------------------------------------------

func TestParseColorMode(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    reporter.ColorMode
		wantErr bool
	}{
		"empty":       {"", reporter.ColorAuto, false},
		"auto":        {"auto", reporter.ColorAuto, false},
		"AUTO":        {"AUTO", reporter.ColorAuto, false},
		"always":      {"always", reporter.ColorAlways, false},
		"never":       {"never", reporter.ColorNever, false},
		"on alias":    {"on", reporter.ColorAlways, false},
		"off alias":   {"off", reporter.ColorNever, false},
		"junk":        {"chartreuse", reporter.ColorAuto, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := reporter.ParseColorMode(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestResolveColor(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	with := func(k, v string) func(string) (string, bool) {
		return func(s string) (string, bool) {
			if s == k {
				return v, true
			}
			return "", false
		}
	}

	cases := []struct {
		name string
		mode reporter.ColorMode
		tty  bool
		env  func(string) (string, bool)
		want bool
	}{
		{"explicit always wins over no-color env", reporter.ColorAlways, false, with("NO_COLOR", "1"), true},
		{"explicit never wins over force-color env", reporter.ColorNever, true, with("FORCE_COLOR", "1"), false},
		{"auto + tty + clean env -> on", reporter.ColorAuto, true, none, true},
		{"auto + no tty -> off", reporter.ColorAuto, false, none, false},
		{"NO_COLOR disables auto even with tty", reporter.ColorAuto, true, with("NO_COLOR", "1"), false},
		{"FORCE_COLOR enables auto without tty", reporter.ColorAuto, false, with("FORCE_COLOR", "1"), true},
		{"CLICOLOR_FORCE enables auto without tty", reporter.ColorAuto, false, with("CLICOLOR_FORCE", "1"), true},
		{"empty NO_COLOR is ignored", reporter.ColorAuto, true, with("NO_COLOR", ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reporter.ResolveColor(tc.mode, tc.tty, tc.env); got != tc.want {
				t.Errorf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPrettyRunEndPrintsResults(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})
	r.Emit(reporter.Event{
		Kind:     reporter.EvtRunEnd,
		Status:   "succeeded",
		Duration: 1500 * time.Millisecond,
		Results: map[string]any{
			"revision": "abc123",
			"files":    []string{"a.txt", "b.txt"},
			"meta":     map[string]string{"owner": "team-a"},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "PipelineRun") {
		t.Fatalf("missing run summary line: %q", out)
	}
	for _, want := range []string{"revision", "abc123", "files", "a.txt", "meta", "team-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output: %s", want, out)
		}
	}
}

func TestPrettyRunEndOmitsResultsWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded", Duration: 100 * time.Millisecond})
	out := buf.String()
	if strings.Contains(out, "results:") {
		t.Errorf("output should not include a results section when none resolved: %q", out)
	}
}

func TestJSONRunEndIncludesResults(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind:   reporter.EvtRunEnd,
		Status: "succeeded",
		Results: map[string]any{
			"revision": "abc123",
			"files":    []string{"a.txt", "b.txt"},
			"meta":     map[string]string{"owner": "team-a"},
		},
	})
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res, ok := got["results"].(map[string]any)
	if !ok {
		t.Fatalf("results field missing or wrong type: %T %v", got["results"], got["results"])
	}
	if res["revision"] != "abc123" {
		t.Errorf("results.revision = %v, want abc123", res["revision"])
	}
}

// captureSink is a tiny in-test recorder used by the LogSink tests.
type captureSink struct {
	events []reporter.Event
}

func (c *captureSink) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *captureSink) Close() error          { return nil }

func TestLogSinkStepLogPropagatesDisplayName(t *testing.T) {
	cap := &captureSink{}
	ls := reporter.NewLogSink(cap)
	ls.StepLog("t1", "s1", "Compile binary", "stdout", "hello\n")

	got := cap.events
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	e := got[0]
	if e.Kind != reporter.EvtStepLog {
		t.Errorf("Kind = %q", e.Kind)
	}
	if e.Task != "t1" || e.Step != "s1" {
		t.Errorf("Task/Step = %q/%q", e.Task, e.Step)
	}
	if e.DisplayName != "Compile binary" {
		t.Errorf("DisplayName = %q (want %q)", e.DisplayName, "Compile binary")
	}
	if e.Stream != "stdout" || e.Line != "hello\n" {
		t.Errorf("Stream/Line = %q/%q", e.Stream, e.Line)
	}
}

func TestLogSinkStepLogEmptyDisplayNameOmitsField(t *testing.T) {
	// Coverage: empty displayName must produce an event whose JSON omits
	// display_name (so agents fall back to e.Step).
	cap := &captureSink{}
	ls := reporter.NewLogSink(cap)
	ls.StepLog("t1", "s1", "", "stdout", "hello\n")

	if len(cap.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(cap.events))
	}
	if cap.events[0].DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty", cap.events[0].DisplayName)
	}
	// Encode and assert omitempty.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(cap.events[0]); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("display_name")) {
		t.Errorf("expected display_name to be omitted; got: %s", buf.Bytes())
	}
}

func TestPrettyPrefersDisplayNameOverName(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})

	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p-raw", DisplayName: "Build & test"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t-raw", DisplayName: "Compile binary", Status: "succeeded"})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded"})

	out := buf.String()
	if !strings.Contains(out, "Build & test") {
		t.Errorf("missing pipeline displayName in pretty output:\n%s", out)
	}
	if !strings.Contains(out, "Compile binary") {
		t.Errorf("missing task displayName in pretty output:\n%s", out)
	}
	// Structured assertion: when displayName is set, the raw name MUST
	// NOT appear anywhere in the pretty output.
	if strings.Contains(out, "p-raw") {
		t.Errorf("pretty output leaked raw pipeline name 'p-raw' even though displayName is set; got:\n%s", out)
	}
	if strings.Contains(out, "t-raw") {
		t.Errorf("pretty output leaked raw task name 't-raw' even though displayName is set; got:\n%s", out)
	}
}

// Fallback: empty displayName MUST use the raw name. One assertion per
// label site so a future refactor can't drop labelOf at one site silently.
func TestPrettyFallsBackToNameWhenDisplayNameEmpty(t *testing.T) {
	cases := []struct {
		name string
		evt  reporter.Event
		want string
	}{
		{"run-start", reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p-only"}, "p-only"},
		{"task-start", reporter.Event{Kind: reporter.EvtTaskStart, Task: "t-only"}, "t-only"},
		{"task-end", reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t-end-only", Status: "succeeded"}, "t-end-only"},
		{"task-skip", reporter.Event{Kind: reporter.EvtTaskSkip, Task: "t-skip-only", Message: "when=false"}, "t-skip-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Verbose})
			r.Emit(tc.evt)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("expected raw name %q in pretty output (no displayName); got:\n%s", tc.want, buf.String())
			}
		})
	}
}

func TestJSONEventCarriesDisplayNameAndDescription(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind:        reporter.EvtRunStart,
		Pipeline:    "p",
		DisplayName: "Build & test",
		Description: "Build then test.",
	})
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got["display_name"] != "Build & test" {
		t.Errorf("display_name = %v", got["display_name"])
	}
	if got["description"] != "Build then test." {
		t.Errorf("description = %v", got["description"])
	}
}

func TestJSONEventOmitsEmptyDisplayName(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
	if bytes.Contains(buf.Bytes(), []byte("display_name")) {
		t.Errorf("expected display_name to be omitted, got: %s", buf.Bytes())
	}
	if bytes.Contains(buf.Bytes(), []byte(`"description"`)) {
		t.Errorf("expected description to be omitted, got: %s", buf.Bytes())
	}
}

func TestPrettyRendersSidecarLogsAndCrashEvents(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Normal})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Line: "ready"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "failed", ExitCode: 137})
	out := buf.String()
	if !strings.Contains(out, "t:redis") {
		t.Errorf("missing sidecar log line prefix; got: %s", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("missing sidecar log content; got: %s", out)
	}
	if !strings.Contains(out, "137") {
		t.Errorf("missing sidecar exit code; got: %s", out)
	}
}

func TestPrettyQuietSuppressesSidecarLogs(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Quiet})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Line: "noisy"})
	if strings.Contains(buf.String(), "noisy") {
		t.Errorf("quiet mode leaked sidecar log: %s", buf.String())
	}
}

func TestPrettyVerboseShowsSidecarStart(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Verbose})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarStart, Task: "t", Step: "redis"})
	if !strings.Contains(buf.String(), "sidecar started") {
		t.Errorf("verbose mode missing sidecar-start line: %s", buf.String())
	}
}

func TestPrettyNormalSuppressesCleanSidecarEnd(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Normal})
	// Clean shutdown (status succeeded, exit 0) → no output unless Verbose.
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "succeeded", ExitCode: 0})
	if strings.Contains(buf.String(), "sidecar exited") {
		t.Errorf("normal mode should suppress clean sidecar-end: %s", buf.String())
	}
}

func TestPrettyVerboseShowsCleanSidecarEnd(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Verbose})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "succeeded", ExitCode: 0})
	if !strings.Contains(buf.String(), "sidecar exited 0") {
		t.Errorf("verbose mode missing clean sidecar-end: %s", buf.String())
	}
}

func TestPrettySidecarEndInfraFailedShowsFailedToStart(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Normal})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "infrafailed", ExitCode: 0})
	if !strings.Contains(buf.String(), "failed to start") {
		t.Errorf("infrafailed sidecar-end should show 'failed to start': %s", buf.String())
	}
}

func TestPrettySidecarLogStderrMarked(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.Normal})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Stream: "sidecar-stderr", Line: "warn"})
	if !strings.Contains(buf.String(), "!") {
		t.Errorf("sidecar-stderr line should carry stderr marker: %s", buf.String())
	}
}

func TestJSONSidecarEventsEncodeKind(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarStart, Task: "t", Step: "redis"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Stream: "sidecar-stdout", Line: "ready"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "succeeded", ExitCode: 0})
	out := buf.String()
	for _, want := range []string{`"kind":"sidecar-start"`, `"kind":"sidecar-log"`, `"kind":"sidecar-end"`, `"stream":"sidecar-stdout"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got: %s", want, out)
		}
	}
}

func TestLogSinkEmitSidecarStartEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	sink := reporter.NewLogSink(r)
	sink.EmitSidecarStart("t", "redis")
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["kind"] != "sidecar-start" || got["task"] != "t" || got["step"] != "redis" || got["stream"] != "sidecar" {
		t.Errorf("got = %v, want kind=sidecar-start task=t step=redis stream=sidecar", got)
	}
}

func TestLogSinkEmitSidecarEndEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	sink := reporter.NewLogSink(r)
	sink.EmitSidecarEnd("t", "redis", 137, "failed", "killed")
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["kind"] != "sidecar-end" {
		t.Errorf("kind = %v, want sidecar-end", got["kind"])
	}
	if got["status"] != "failed" {
		t.Errorf("status = %v, want failed", got["status"])
	}
	if got["exitCode"].(float64) != 137 {
		t.Errorf("exitCode = %v, want 137", got["exitCode"])
	}
	if got["message"] != "killed" {
		t.Errorf("message = %v, want killed", got["message"])
	}
}

func TestLogSinkSidecarLogEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	sink := reporter.NewLogSink(r)
	sink.SidecarLog("t", "redis", "sidecar-stdout", "hello")
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["kind"] != "sidecar-log" {
		t.Errorf("kind = %v, want sidecar-log", got["kind"])
	}
	if got["task"] != "t" {
		t.Errorf("task = %v", got["task"])
	}
	if got["step"] != "redis" {
		t.Errorf("step = %v", got["step"])
	}
	if got["stream"] != "sidecar-stdout" {
		t.Errorf("stream = %v", got["stream"])
	}
	if got["line"] != "hello" {
		t.Errorf("line = %v", got["line"])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestJSONResolverEvents covers the resolver-start / resolver-end JSON
// shape from spec §12. Both events appear with their fields populated.
func TestJSONResolverEvents(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind: reporter.EvtResolverStart, Time: time.Unix(1, 0),
		Task: "build", Resolver: "git",
	})
	r.Emit(reporter.Event{
		Kind: reporter.EvtResolverEnd, Time: time.Unix(2, 0),
		Task: "build", Resolver: "git", Status: "succeeded",
		Duration: time.Second, SHA256: "abc", Source: "git: foo",
	})
	out := buf.String()
	if !strings.Contains(out, `"kind":"resolver-start"`) {
		t.Errorf("missing resolver-start: %s", out)
	}
	if !strings.Contains(out, `"kind":"resolver-end"`) {
		t.Errorf("missing resolver-end: %s", out)
	}
	if !strings.Contains(out, `"resolver":"git"`) {
		t.Errorf("missing resolver field: %s", out)
	}
	if !strings.Contains(out, `"sha256":"abc"`) {
		t.Errorf("missing sha256 field: %s", out)
	}
	if !strings.Contains(out, `"source":"git: foo"`) {
		t.Errorf("missing source field: %s", out)
	}
}

// TestJSONResolverEventOmitsZeroValues: a resolver-end Event whose
// Resolver/Cached/SHA256/Source are all zero must NOT serialize those
// keys. Mirrors the omitempty convention every other optional Event
// field uses.
func TestJSONResolverEventOmitsZeroValues(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Unix(1, 0),
		Status: "succeeded",
	})
	out := buf.String()
	for _, key := range []string{`"resolver"`, `"cached"`, `"sha256"`, `"source"`} {
		if strings.Contains(out, key) {
			t.Errorf("expected %s to be omitted via omitempty, got %s", key, out)
		}
	}
}

// TestJSONResolverEventEmptyTaskForTopLevelPipelineRef: emitting a
// resolver-end without a Task field (top-level pipelineRef path)
// must omit the "task" key entirely. The consumer disambiguates the
// top-level resolution from per-task resolution via the absence of
// the field.
func TestJSONResolverEventEmptyTaskForTopLevelPipelineRef(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind: reporter.EvtResolverEnd, Time: time.Unix(1, 0),
		Resolver: "git", Status: "succeeded",
	})
	out := buf.String()
	if strings.Contains(out, `"task"`) {
		t.Errorf("expected no task key for top-level pipelineRef resolution: %s", out)
	}
	// Sanity: the resolver field IS present.
	if !strings.Contains(out, `"resolver":"git"`) {
		t.Errorf("missing resolver field: %s", out)
	}
}

// TestEventMatrixRoundTrip pins the JSON shape of Event.Matrix and
// verifies that non-matrix events (Matrix == nil) marshal to a JSON
// object that does NOT contain a "matrix" key. This is the
// byte-identical-non-matrix invariant promised by the spec § 6.4.
func TestEventMatrixRoundTrip(t *testing.T) {
	ev := reporter.Event{
		Kind: reporter.EvtTaskStart,
		Task: "build-1",
		Matrix: &reporter.MatrixEvent{
			Parent: "build",
			Index:  1,
			Of:     4,
			Params: map[string]string{"os": "darwin", "goversion": "1.21"},
		},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	m, ok := v["matrix"].(map[string]any)
	if !ok {
		t.Fatalf("matrix key missing or wrong type: %v", v)
	}
	if m["parent"] != "build" || m["index"].(float64) != 1 || m["of"].(float64) != 4 {
		t.Errorf("matrix payload = %+v", m)
	}
	ps, ok := m["params"].(map[string]any)
	if !ok || ps["os"] != "darwin" || ps["goversion"] != "1.21" {
		t.Errorf("matrix.params = %+v", m["params"])
	}
}

func TestEventMatrixOmitemptyOnNonMatrixEvents(t *testing.T) {
	ev := reporter.Event{Kind: reporter.EvtTaskEnd, Task: "ordinary", Status: "succeeded"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"matrix"`) {
		t.Errorf("non-matrix event should not include a matrix key, got %s", b)
	}
}
