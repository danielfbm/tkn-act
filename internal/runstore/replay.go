package runstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

// Replay reads events.jsonl line by line and emits each event to rep.
// The first malformed line aborts the replay with a descriptive error
// (well-formed events earlier in the file have already been emitted).
//
// Buffer is sized to handle the 1 MiB max-line invariant the engine
// guarantees per step-log line.
func Replay(eventsPath string, rep reporter.Reporter) error {
	f, err := os.Open(eventsPath)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Increase the per-line cap to match the engine's max step-log
	// line size (1 MiB). bufio.Scanner's default 64 KiB starts the
	// buffer; supply a larger max so big log lines don't ErrTooLong.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue // tolerate stray blank lines without bumping the error
		}
		var ev reporter.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			return fmt.Errorf("events line %d: %w", line, err)
		}
		rep.Emit(ev)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("scan events: %w", err)
	}
	return nil
}
