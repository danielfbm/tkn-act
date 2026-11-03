package docker

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/docker/docker/pkg/stdcopy"
)

type recSink struct {
	mu    sync.Mutex
	steps []string
	sides []string
}

func (r *recSink) StepLog(_, _, _, stream, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, stream+":"+line)
}

func (r *recSink) SidecarLog(_, _, stream, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sides = append(r.sides, stream+":"+line)
}

// muxed builds a docker multiplexed log stream: a 2MiB stdout line
// followed by n short stdout lines and one stderr line.
func muxed(t *testing.T, n int) io.ReadCloser {
	t.Helper()
	var buf bytes.Buffer
	out := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	errw := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	if _, err := out.Write([]byte(strings.Repeat("x", 2<<20) + "\n")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := fmt.Fprintf(out, "line %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := errw.Write([]byte("oops\r\n")); err != nil {
		t.Fatal(err)
	}
	return io.NopCloser(&buf)
}

func runWithin(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("log streaming hung (scanner stopped reading the pipe)")
	}
}

// TestStreamLogs_LongLineDoesNotStall is the regression for the
// bufio.Scanner ErrTooLong stall: a >1MiB line used to stop the
// scanner, which stopped reading the pipe, which blocked stdcopy
// forever and lost every later line.
func TestStreamLogs_LongLineDoesNotStall(t *testing.T) {
	sink := &recSink{}
	runWithin(t, 10*time.Second, func() {
		streamLogs(muxed(t, 100), sink, "t", "s", "")
	})
	if len(sink.steps) != 102 {
		t.Fatalf("got %d lines, want 102 (1 truncated + 100 + 1 stderr)", len(sink.steps))
	}
	first := sink.steps[0]
	if !strings.HasPrefix(first, "stdout:xxx") || !strings.Contains(first, "truncated") {
		t.Errorf("first line not truncated with marker: %.40q...%q", first, first[len(first)-60:])
	}
	if len(first) > backend.MaxLogLineBytes+200 {
		t.Errorf("truncated line still %d bytes", len(first))
	}
	var gotLast, gotErr bool
	for _, l := range sink.steps {
		if l == "stdout:line 99" {
			gotLast = true
		}
		if l == "stderr:oops" {
			gotErr = true
		}
	}
	if !gotLast || !gotErr {
		t.Errorf("missing later lines: last=%v stderr=%v", gotLast, gotErr)
	}
}

func TestStreamSidecarLogs_LongLineDoesNotStall(t *testing.T) {
	sink := &recSink{}
	runWithin(t, 10*time.Second, func() {
		streamSidecarLogs(muxed(t, 10), sink, "t", "sc")
	})
	if len(sink.sides) != 12 {
		t.Fatalf("got %d sidecar lines, want 12", len(sink.sides))
	}
}

// TestGraceSeconds: a sub-second stop grace must round up to 1s, not
// truncate to 0 (which Docker treats as immediate SIGKILL).
func TestGraceSeconds(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want int
	}{
		{0, 0},
		{-time.Second, 0},
		{500 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{30 * time.Second, 30},
	} {
		if got := graceSeconds(c.in); got != c.want {
			t.Errorf("graceSeconds(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestWaitLogs_Bounded verifies the join helper returns once the
// stream finishes and gives up after the timeout otherwise.
func TestWaitLogs_Bounded(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !waitLogs(done, time.Second) {
		t.Error("closed done: want true")
	}
	start := time.Now()
	if waitLogs(make(chan struct{}), 50*time.Millisecond) {
		t.Error("open done: want false")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("waitLogs did not honour its timeout")
	}
}
