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
	// Timestamps, when true, prefixes each step-log, sidecar-log, and
	// debug line with `[HH:MM:SS.mmm] ` (UTC). Useful for correlating
	// log lines across parallel tasks. Wired by the `--timestamps`
	// CLI flag; off by default.
	Timestamps bool
}

type prettySink struct {
	mu         sync.Mutex
	w          io.Writer
	pal        palette
	verb       Verbosity
	timestamps bool
	pipeline   string
}

// NewPretty returns a Reporter that prints human-readable, live-ordered
// output. Step logs stream as they arrive, prefixed with their task and step
// names so parallel runs remain readable.
func NewPretty(w io.Writer, opt PrettyOptions) Reporter {
	return &prettySink{
		w:          w,
		pal:        newPalette(opt.Color),
		verb:       opt.Verbosity,
		timestamps: opt.Timestamps,
	}
}

// writeTimestampPrefix writes a `[HH:MM:SS.mmm] ` prefix to the
// reporter's output when Timestamps is enabled and the event has a
// non-zero Time. Used by step-log, sidecar-log, and debug branches
// to give every "live" line a wall-clock anchor.
func (p *prettySink) writeTimestampPrefix(e Event) {
	if !p.timestamps || e.Time.IsZero() {
		return
	}
	fmt.Fprintf(p.w, "[%s] ", e.Time.UTC().Format("15:04:05.000"))
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
		p.writeTimestampPrefix(e)
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
		// Format: `  ◊ task:sidecar │   line`
		// "◊" tags this as sidecar output (steps use the cyan task/step
		// prefix); ":" separates task and sidecar name (steps use "/")
		// so mixed step + sidecar logs are visually attributable at a
		// glance.
		p.writeTimestampPrefix(e)
		stream := " "
		if e.Stream == "sidecar-stderr" {
			stream = p.pal.wrap(p.pal.yellow, "!")
		}
		fmt.Fprintf(p.w, "  %s %s %s %s %s\n",
			p.pal.wrap(p.pal.cyan, "◊"),
			p.pal.wrap(p.pal.cyan, e.Task+":"+labelOf(e.Step, e.DisplayName)),
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

	case EvtResolverStart:
		// resolver-start is mostly noise on the pretty stream — the
		// matching resolver-end carries the load-bearing info (status,
		// duration, source, cached). We only surface starts in Verbose.
		if p.verb >= Verbose {
			label := e.Task
			if label == "" {
				label = "(pipeline)"
			}
			fmt.Fprintf(p.w, "  %s %s resolver %s starting\n",
				p.pal.wrap(p.pal.dim, "↳"),
				p.pal.wrap(p.pal.dim, label),
				p.pal.wrap(p.pal.cyan, e.Resolver),
			)
		}

	case EvtResolverEnd:
		if p.verb < Normal {
			return
		}
		// One indented line under the parent task name, per spec §13.
		// Format:
		//   ↳ resolver <name> <source> (<duration>|cached|<message>)
		// Top-level pipelineRef.resolver: e.Task is empty; render
		// without a task prefix.
		var taskPrefix string
		if e.Task != "" {
			taskPrefix = p.pal.wrap(p.pal.dim, e.Task) + " "
		}
		var trail string
		switch {
		case e.Status == StatusFailed:
			trail = p.pal.wrap(p.pal.red, e.Message)
		case e.Cached:
			trail = p.pal.wrap(p.pal.dim, "(cached)")
		default:
			dur := e.Duration.Round(time.Millisecond)
			trail = p.pal.wrap(p.pal.dim, fmt.Sprintf("(%s)", dur))
		}
		src := e.Source
		if src == "" {
			src = "(no source reported)"
		}
		fmt.Fprintf(p.w, "  %s %sresolver %s %s %s\n",
			p.pal.wrap(p.pal.dim, "↳"),
			taskPrefix,
			p.pal.wrap(p.pal.cyan, e.Resolver),
			src,
			trail,
		)

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

	case EvtDebug:
		// Suppressed in Quiet — debug is trace data, not summary
		// output.
		if p.verb < Normal {
			return
		}
		// Render as: `  [debug] component=<c> k=v k=v — msg`.
		// Indented to align with step-log output. Fields render in
		// sorted key order so the line is deterministic across runs.
		p.writeTimestampPrefix(e)
		var sb strings.Builder
		sb.WriteString("  ")
		sb.WriteString(p.pal.wrap(p.pal.gray, "[debug]"))
		sb.WriteString(" component=")
		sb.WriteString(e.Component)
		if len(e.Fields) > 0 {
			keys := make([]string, 0, len(e.Fields))
			for k := range e.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&sb, " %s=%v", k, e.Fields[k])
			}
		}
		if e.Message != "" {
			sb.WriteString(" — ")
			sb.WriteString(e.Message)
		}
		sb.WriteByte('\n')
		p.w.Write([]byte(sb.String()))
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
