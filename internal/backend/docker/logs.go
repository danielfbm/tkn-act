package docker

import (
	"io"
	"sync"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/docker/docker/pkg/stdcopy"
)

// logJoinTimeout bounds how long a Step / Task waits for its log
// stream to drain after the container exits, so the final lines are
// reported before step / task end without risking a hang on a daemon
// that never closes the follow stream.
const logJoinTimeout = 5 * time.Second

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
		backend.ScanLogLines(stdoutR, func(l string) { emit(stdoutName, l) })
	}()
	backend.ScanLogLines(stderrR, func(l string) { emit(stderrName, l) })
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
