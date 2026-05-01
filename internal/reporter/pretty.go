package reporter

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type prettySink struct {
	mu       sync.Mutex
	w        io.Writer
	color    bool
	taskBuf  map[string]*taskState // task name → buffered logs (last N lines)
	startAt  time.Time
	pipeline string
}

type taskState struct {
	logs []string
}

const maxTailLines = 20

func NewPretty(w io.Writer, color bool) Reporter {
	return &prettySink{w: w, color: color, taskBuf: map[string]*taskState{}}
}

func (p *prettySink) Emit(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case EvtRunStart:
		p.startAt = e.Time
		p.pipeline = e.Pipeline
		_, _ = fmt.Fprintf(p.w, "▶ %s\n", or(e.Pipeline, "pipeline"))
	case EvtTaskStart:
		// nothing — print on end
		p.taskBuf[e.Task] = &taskState{}
	case EvtStepLog:
		if st, ok := p.taskBuf[e.Task]; ok {
			st.logs = append(st.logs, e.Line)
			if len(st.logs) > maxTailLines {
				st.logs = st.logs[len(st.logs)-maxTailLines:]
			}
		}
	case EvtTaskSkip:
		_, _ = fmt.Fprintf(p.w, "%s %s  skipped (%s)\n", glyph("skipped", p.color), e.Task, e.Message)
	case EvtTaskEnd:
		dur := e.Duration.Round(time.Millisecond)
		_, _ = fmt.Fprintf(p.w, "%s %s  (%s)", glyph(e.Status, p.color), e.Task, dur)
		if e.Message != "" {
			_, _ = fmt.Fprintf(p.w, "  %s", e.Message)
		}
		_, _ = fmt.Fprintln(p.w)
		if e.Status == "failed" {
			if st, ok := p.taskBuf[e.Task]; ok {
				for _, l := range st.logs {
					_, _ = fmt.Fprintf(p.w, "    │ %s\n", l)
				}
			}
		}
	case EvtRunEnd:
		dur := e.Duration.Round(time.Millisecond)
		_, _ = fmt.Fprintln(p.w, strings.Repeat("─", 40))
		_, _ = fmt.Fprintf(p.w, "PipelineRun %s in %s", e.Status, dur)
		if e.Message != "" {
			_, _ = fmt.Fprintf(p.w, "  (%s)", e.Message)
		}
		_, _ = fmt.Fprintln(p.w)
	case EvtError:
		_, _ = fmt.Fprintf(p.w, "error: %s\n", e.Message)
	}
}

func (p *prettySink) Close() error { return nil }

func glyph(status string, color bool) string {
	switch status {
	case "succeeded":
		return tint("✓", color, "\033[32m")
	case "failed":
		return tint("✗", color, "\033[31m")
	case "skipped":
		return tint("⊘", color, "\033[33m")
	case "not-run":
		return tint("·", color, "\033[90m")
	default:
		return "•"
	}
}

func tint(s string, on bool, code string) string {
	if !on {
		return s
	}
	return code + s + "\033[0m"
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

