package runstore

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// AbandonAfter is how long an unfinalized run without usable liveness
// information (no PID recorded, or recorded on another host) is
// considered possibly in flight. After that it is "abandoned": the
// writer crashed or was SIGKILLed before Finalize ran.
const AbandonAfter = 24 * time.Hour

// Status values derived for unfinalized runs (never written to disk;
// a finalized run carries its recorded status instead).
const (
	// StatusRunning marks an unfinalized run whose writer is (or may
	// still be) alive.
	StatusRunning = "running"
	// StatusUnknown marks an abandoned run: never finalized and its
	// writer is gone, so the outcome is unknown.
	StatusUnknown = "unknown"
)

// processAlive is swapped out by tests.
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// hostname is swapped out by tests.
var hostname = func() string {
	h, _ := os.Hostname()
	return h
}

// Finalized reports whether the run recorded an end.
func (e IndexEntry) Finalized() bool { return !e.EndedAt.IsZero() }

// Abandoned reports whether e is an unfinalized run whose writer is
// gone. When the run was started on this host with a recorded PID, the
// PID's liveness decides; otherwise a run older than AbandonAfter is
// abandoned.
func (e IndexEntry) Abandoned(now time.Time) bool {
	if e.Finalized() {
		return false
	}
	if e.PID > 0 && e.Host != "" && e.Host == hostname() {
		return !processAlive(e.PID)
	}
	return e.StartedAt.IsZero() || now.Sub(e.StartedAt) > AbandonAfter
}

// InFlight reports whether e may still be written by a live process.
// Retention never deletes an in-flight run.
func (e IndexEntry) InFlight(now time.Time) bool {
	return !e.Finalized() && !e.Abandoned(now)
}

// EffectiveStatus is the status to present for e: the recorded status
// for finalized runs, StatusRunning for in-flight ones and
// StatusUnknown for abandoned ones.
func (e IndexEntry) EffectiveStatus(now time.Time) string {
	switch {
	case e.Finalized():
		return e.Status
	case e.Abandoned(now):
		return StatusUnknown
	default:
		return StatusRunning
	}
}
