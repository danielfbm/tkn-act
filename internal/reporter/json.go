package reporter

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type jsonSink struct {
	mu sync.Mutex
	w  io.Writer
}

func NewJSON(w io.Writer) Reporter { return &jsonSink{w: w} }

func (j *jsonSink) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = j.w.Write(b)
	_, _ = j.w.Write([]byte("\n"))
}

func (j *jsonSink) Close() error { return nil }
