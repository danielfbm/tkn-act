package reporter

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Verbosity controls how much the pretty reporter prints.
//
//	Quiet  → final task + run summaries only
//	Normal → pipeline header + live step logs + task/run summaries (default)
//	Verbose → adds step-start/step-end markers
type Verbosity int

const (
	Quiet   Verbosity = -1
	Normal  Verbosity = 0
	Verbose Verbosity = 1
)

// PrettyOptions configures NewPretty.
type PrettyOptions struct {
	Color     bool      // already resolved via ResolveColor
	Verbosity Verbosity // Quiet | Normal | Verbose
}

type prettySink struct {
	mu       sync.Mutex
	w        io.Writer
	pal      palette
	verb     Verbosity
	pipeline string
}

// NewPretty returns a Reporter that prints human-readable, live-ordered
// output. Step logs stream as they arrive, prefixed with their task and step
// names so parallel runs remain readable.
func NewPretty(w io.Writer, opt PrettyOptions) Reporter {
	return &prettySink{
		w:    w,
		pal:  newPalette(opt.Color),
		verb: opt.Verbosity,
	}
}

func (p *prettySink) Emit(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch e.Kind {
	case EvtRunStart:
		p.pipeline = e.Pipeline
		if p.verb >= Normal {
			fmt.Fprintf(p.w, "%s %s\n",
				p.pal.wrap(p.pal.cyan, "▶"),
				p.pal.wrap(p.pal.bold, labelOf(or(e.Pipeline, "pipeline"), e.DisplayName)),
			)
		}

	case EvtTaskStart:
		if p.verb >= Verbose {
			fmt.Fprintf(p.w, "%s %s\n",
				p.pal.wrap(p.pal.cyan, "▸"),
				labelOf(e.Task, e.DisplayName),
			)
		}

	case EvtStepStart:
		if p.verb >= Verbose {
			fmt.Fprintf(p.w, "  %s %s started\n",
				p.pal.wrap(p.pal.dim, "·"),
				p.pal.wrap(p.pal.dim, prefixOf(e.Task, labelOf(e.Step, e.DisplayName))),
			)
		}

	case EvtStepEnd:
		if p.verb >= Verbose {
			fmt.Fprintf(p.w, "  %s %s finished (exit %d)\n",
				p.pal.wrap(p.pal.dim, "·"),
				p.pal.wrap(p.pal.dim, prefixOf(e.Task, labelOf(e.Step, e.DisplayName))),
				e.ExitCode,
			)
		}

	case EvtStepLog:
		if p.verb < Normal {
			return
		}
		// Stream every log line in arrival order. The task/step prefix lets the
		// user disambiguate parallel tasks; the bar separator keeps the line
		// itself unindented so copy-paste of error messages is clean.
		prefix := prefixOf(e.Task, labelOf(e.Step, e.DisplayName))
		stream := ""
		if e.Stream == "stderr" {
			stream = p.pal.wrap(p.pal.yellow, "!")
		} else {
			stream = " "
		}
		fmt.Fprintf(p.w, "  %s %s %s %s\n",
			p.pal.wrap(p.pal.cyan, prefix),
			p.pal.wrap(p.pal.dim, "│"),
			stream,
			e.Line,
		)

	case EvtSidecarLog:
		if p.verb < Normal {
			return
		}
		// Use ":" between task and sidecar name (steps use "/") so
		// mixed step + sidecar logs are visually attributable at a glance.
		stream := " "
		if e.Stream == "sidecar-stderr" {
			stream = p.pal.wrap(p.pal.yellow, "!")
		}
		fmt.Fprintf(p.w, "  %s %s %s %s\n",
			p.pal.wrap(p.pal.cyan, e.Task+":"+e.Step),
			p.pal.wrap(p.pal.dim, "│"),
			stream,
			e.Line,
		)

	case EvtSidecarStart:
		if p.verb >= Verbose {
			fmt.Fprintf(p.w, "  %s %s sidecar started\n",
				p.pal.wrap(p.pal.dim, "·"),
				p.pal.wrap(p.pal.dim, e.Task+":"+e.Step),
			)
		}

	case EvtSidecarEnd:
		// Always surface anomalies (non-zero exit, infrafailed). Quiet
		// only the clean shutdown case unless we're in Verbose.
		if e.Status != StatusSucceeded || e.ExitCode != 0 {
			detail := e.Status
			if e.Status == StatusInfraFailed {
				detail = "failed to start"
			} else if e.Status == StatusFailed {
				detail = "crashed"
			}
			fmt.Fprintf(p.w, "  %s %s sidecar exited %d (%s)\n",
				p.pal.wrap(p.pal.yellow, "·"),
				p.pal.wrap(p.pal.dim, e.Task+":"+e.Step),
				e.ExitCode,
				detail,
			)
		} else if p.verb >= Verbose {
			fmt.Fprintf(p.w, "  %s %s sidecar exited 0\n",
				p.pal.wrap(p.pal.dim, "·"),
				p.pal.wrap(p.pal.dim, e.Task+":"+e.Step),
			)
		}

	case EvtTaskSkip:
		fmt.Fprintf(p.w, "%s %s  skipped (%s)\n",
			glyph("skipped", p.pal),
			labelOf(e.Task, e.DisplayName),
			e.Message,
		)

	case EvtTaskEnd:
		dur := e.Duration.Round(time.Millisecond)
		fmt.Fprintf(p.w, "%s %s  %s",
			glyph(e.Status, p.pal),
			labelOf(e.Task, e.DisplayName),
			p.pal.wrap(p.pal.dim, fmt.Sprintf("(%s)", dur)),
		)
		if e.Message != "" {
			fmt.Fprintf(p.w, "  %s", p.pal.wrap(p.pal.red, e.Message))
		}
		fmt.Fprintln(p.w)

	case EvtRunEnd:
		dur := e.Duration.Round(time.Millisecond)
		if p.verb >= Normal {
			fmt.Fprintln(p.w, p.pal.wrap(p.pal.dim, strings.Repeat("─", 40)))
		}
		fmt.Fprintf(p.w, "%s PipelineRun %s in %s",
			glyph(e.Status, p.pal),
			p.pal.wrap(p.pal.bold, statusWord(e.Status)),
			dur,
		)
		if e.Message != "" {
			fmt.Fprintf(p.w, "  %s", p.pal.wrap(p.pal.red, e.Message))
		}
		fmt.Fprintln(p.w)
		if len(e.Results) > 0 {
			// Stable iteration order so output is deterministic across runs.
			names := make([]string, 0, len(e.Results))
			for k := range e.Results {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(p.w, "  %s %s\n",
					p.pal.wrap(p.pal.bold, name+":"),
					formatResultValue(e.Results[name]),
				)
			}
		}

	case EvtError:
		fmt.Fprintf(p.w, "%s %s\n",
			p.pal.wrap(p.pal.red, "error:"),
			e.Message,
		)
	}
}

func (p *prettySink) Close() error { return nil }

// glyph maps a status to its colored single-character symbol.
func glyph(status string, pal palette) string {
	switch status {
	case "succeeded":
		return pal.wrap(pal.green, "✓")
	case "failed", "infrafailed":
		return pal.wrap(pal.red, "✗")
	case "skipped":
		return pal.wrap(pal.yellow, "⊘")
	case "not-run":
		return pal.wrap(pal.gray, "·")
	default:
		return "•"
	}
}

func statusWord(s string) string {
	if s == "" {
		return "completed"
	}
	return s
}

func prefixOf(task, step string) string {
	switch {
	case task == "" && step == "":
		return "?"
	case step == "":
		return task
	case task == "":
		return step
	default:
		return task + "/" + step
	}
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// labelOf returns displayName when non-empty, otherwise name. Used
// everywhere the pretty renderer prints a Pipeline / Task / Step
// identifier; agents reading -o json get both the raw name and the
// displayName separately.
func labelOf(name, displayName string) string {
	if displayName != "" {
		return displayName
	}
	return name
}

// formatResultValue renders a Pipeline.spec.results value for pretty
// output. Strings are passed through (truncated to 80 runes with an
// ellipsis if longer); arrays render as `[a, b, c]`; objects as
// `{k1: v1, k2: v2}`. Stable key order on objects.
//
// Truncation works on runes, not bytes — slicing a UTF-8 string at a
// byte index can land mid-codepoint and emit a malformed sequence.
func formatResultValue(v any) string {
	const max = 80
	truncate := func(s string) string {
		rs := []rune(s)
		if len(rs) <= max {
			return s
		}
		return string(rs[:max-1]) + "…"
	}
	switch t := v.(type) {
	case string:
		return truncate(t)
	case []string:
		return truncate("[" + strings.Join(t, ", ") + "]")
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+t[k])
		}
		return truncate("{" + strings.Join(parts, ", ") + "}")
	default:
		return truncate(fmt.Sprintf("%v", v))
	}
}
