package docker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
)

// maxLogLineBytes caps a single emitted log line. Longer lines are
// truncated with a marker; the rest of the line is read and dropped so
// the stream keeps flowing (a stalled reader would block stdcopy and
// leak the daemon connection).
const maxLogLineBytes = 1 << 20

// logJoinTimeout bounds how long a Step / Task waits for its log
// stream to drain after the container exits, so the final lines are
// reported before step / task end without risking a hang on a daemon
// that never closes the follow stream.
const logJoinTimeout = 5 * time.Second

// scanLines reads r to EOF and calls emit for every line, with
// bufio.ScanLines semantics (trailing "\r\n" / "\n" stripped, final
// unterminated line emitted). Unlike bufio.Scanner it never stops on
// a long line: anything past maxLogLineBytes is discarded and noted
// in a truncation marker appended to the emitted line.
func scanLines(r io.Reader, emit func(string)) {
	br := bufio.NewReaderSize(r, 64*1024)
	line := make([]byte, 0, 4096)
	dropped := 0
	flush := func() {
		if n := len(line); n > 0 && line[n-1] == '\r' && dropped == 0 {
			line = line[:n-1]
		}
		s := string(line)
		if dropped > 0 {
			s += fmt.Sprintf(" [tkn-act: line truncated, %d bytes dropped]", dropped)
		}
		emit(s)
		line = line[:0]
		dropped = 0
	}
	for {
		chunk, err := br.ReadSlice('\n')
		eol := err == nil
		if eol {
			chunk = chunk[:len(chunk)-1]
		}
		if room := maxLogLineBytes - len(line); room >= len(chunk) {
			line = append(line, chunk...)
		} else {
			if room > 0 {
				line = append(line, chunk[:room]...)
				chunk = chunk[room:]
			}
			dropped += len(chunk)
		}
		switch {
		case eol:
			flush()
		case errors.Is(err, bufio.ErrBufferFull):
			// Partial line; keep accumulating.
		default:
			if len(line) > 0 || dropped > 0 {
				flush()
			}
			if !errors.Is(err, io.EOF) {
				// Keep the writer side unblocked whatever happened.
				_, _ = io.Copy(io.Discard, r)
			}
			return
		}
	}
}

// demuxLogs splits a docker multiplexed log stream into stdout and
// stderr lines and returns only once both halves are fully consumed.
func demuxLogs(rc io.ReadCloser, emit func(stream, line string), stdoutName, stderrName string) {
	defer func() { _ = rc.Close() }()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(stdoutW, stderrW, rc)
		_ = stdoutW.CloseWithError(err)
		_ = stderrW.CloseWithError(err)
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = stdoutR.Close() }()
		scanLines(stdoutR, func(l string) { emit(stdoutName, l) })
	}()
	scanLines(stderrR, func(l string) { emit(stderrName, l) })
	_ = stderrR.Close()
	wg.Wait()
}

// waitLogs waits up to d for done to close. It reports whether the
// stream finished in time.
func waitLogs(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
