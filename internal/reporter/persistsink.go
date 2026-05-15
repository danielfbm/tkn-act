package reporter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// persistSink writes each event as one JSON line to a file. Used by
// the run lifecycle to record an exact copy of the JSON event stream
// for `tkn-act logs` to replay later.
//
// The encoder mirrors NewJSON's per-event encoding (same zero-Time
// fill, same json.Marshal, same trailing newline) so events.jsonl is
// byte-for-byte identical to what `-o json` writes to stdout when the
// two sinks are tee'd in the same run.
type persistSink struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// NewPersistSink opens path for append and returns a Reporter that
// writes one JSON event per line.
func NewPersistSink(path string) (Reporter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open events file: %w", err)
	}
	return &persistSink{f: f, w: bufio.NewWriter(f)}, nil
}

// NewPersistSinkOnWriter is a test-friendly constructor that writes
// to an arbitrary io.Writer instead of opening a file. The returned
// sink's Close calls Flush but does not close the underlying writer.
func NewPersistSinkOnWriter(w io.Writer) Reporter {
	return &persistSink{w: bufio.NewWriter(w)}
}

func (p *persistSink) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = p.w.Write(b)
	_, _ = p.w.Write([]byte("\n"))
}

func (p *persistSink) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w != nil {
		_ = p.w.Flush()
	}
	if p.f != nil {
		return p.f.Close()
	}
	return nil
}
