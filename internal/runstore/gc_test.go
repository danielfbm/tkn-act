package runstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/runstore"
)

// makeFinalizedRuns creates n runs finalized at the given start time
// plus i seconds, in order. Returns the store and the run handles.
func makeFinalizedRuns(t *testing.T, dir string, n int, start time.Time) (*runstore.Store, []*runstore.Run) {
	t.Helper()
	s, err := runstore.Open(dir, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	runs := make([]*runstore.Run, 0, n)
	for i := 0; i < n; i++ {
		r, err := s.NewRun(start.Add(time.Duration(i)*time.Second), "p", nil)
		if err != nil {
			t.Fatalf("NewRun(%d): %v", i, err)
		}
		if err := r.Finalize(start.Add(time.Duration(i)*time.Second+time.Millisecond), 0, "succeeded"); err != nil {
			t.Fatalf("Finalize(%d): %v", i, err)
		}
		runs = append(runs, r)
	}
	return s, runs
}

func TestPrune_KeepsLastN(t *testing.T) {
	dir := t.TempDir()
	s, _ := makeFinalizedRuns(t, dir, 5, time.Unix(1_700_000_000, 0))
	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 2, KeepDays: 0, Now: time.Unix(1_700_000_100, 0)})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("pruned = %d, want 3", n)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(entries) != 2 {
		t.Errorf("dirs left = %d, want 2", len(entries))
	}

	// Index must reflect the remaining 2 — Resolve("latest") still
	// works and BySeq(5) returns the last kept run.
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	if all := idx.All(); len(all) != 2 {
		t.Errorf("index has %d entries, want 2", len(all))
	}
}

func TestPrune_KeepsByAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)
	young := now.Add(-1 * time.Hour)
	s, _ := runstore.Open(dir, "v")
	rOld, _ := s.NewRun(old, "p", nil)
	rOld.Finalize(old.Add(time.Second), 0, "succeeded")
	rYng, _ := s.NewRun(young, "p", nil)
	rYng.Finalize(young.Add(time.Second), 0, "succeeded")

	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 0, KeepDays: 30, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	// The young run is the survivor.
	if _, err := os.Stat(filepath.Join(dir, "runs", string(rYng.ID))); err != nil {
		t.Errorf("young run missing after prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", string(rOld.ID))); !os.IsNotExist(err) {
		t.Errorf("old run should be gone, got err=%v", err)
	}
}

func TestPrune_BothCountAndAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	// 6 runs: oldest 3 are >30 days old; newest 3 are young.
	s, _ := runstore.Open(dir, "v")
	for i := 0; i < 6; i++ {
		var t0 time.Time
		if i < 3 {
			t0 = now.Add(-60 * 24 * time.Hour).Add(time.Duration(i) * time.Second)
		} else {
			t0 = now.Add(-1 * time.Hour).Add(time.Duration(i) * time.Second)
		}
		r, _ := s.NewRun(t0, "p", nil)
		r.Finalize(t0.Add(time.Millisecond), 0, "succeeded")
	}
	// KeepRuns=4 alone would keep 4; KeepDays=30 alone would keep 3
	// (the 3 young runs). Intersection: keep min(4, 3) = 3.
	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 4, KeepDays: 30, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("pruned = %d, want 3 (both gates applied)", n)
	}
}

func TestPrune_All(t *testing.T) {
	dir := t.TempDir()
	s, _ := makeFinalizedRuns(t, dir, 3, time.Unix(1_700_000_000, 0))
	n, err := s.Prune(runstore.PruneOptions{All: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("pruned = %d, want 3", n)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(entries) != 0 {
		t.Errorf("dirs left = %d, want 0", len(entries))
	}
	// Index must be empty too.
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	if all := idx.All(); len(all) != 0 {
		t.Errorf("index has %d entries, want 0", len(all))
	}
}

func TestPrune_Empty(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 50, KeepDays: 30, Now: time.Now()})
	if err != nil {
		t.Fatalf("Prune on empty: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned = %d, want 0", n)
	}
}

func TestPrune_NoOpsWhenBothZeroAndAllFalse(t *testing.T) {
	dir := t.TempDir()
	s, _ := makeFinalizedRuns(t, dir, 3, time.Unix(1_700_000_000, 0))
	// KeepRuns=0 + KeepDays=0 + All=false → keep everything (no gates).
	n, err := s.Prune(runstore.PruneOptions{Now: time.Unix(1_800_000_000, 0)})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned = %d, want 0 (both gates off)", n)
	}
}

func TestPrune_InFlightImmune_FromCountGate(t *testing.T) {
	// Regression: count-based prune must not delete an in-flight
	// peer's run-dir while it's still being written.
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	// 3 finalized runs + 1 in-flight, KeepRuns=2 → drop the OLDEST
	// FINALIZED run only (keep 2 finalized + 1 in-flight = 3 total).
	for i := 0; i < 3; i++ {
		r, _ := s.NewRun(time.Unix(int64(1_700_000_000+i), 0), "p", nil)
		r.Finalize(time.Unix(int64(1_700_000_000+i)+1, 0), 0, "succeeded")
	}
	inflight, _ := s.NewRun(time.Unix(1_700_000_100, 0), "p", nil)
	_ = inflight // no Finalize call

	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 2, Now: time.Unix(1_700_000_200, 0)})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1 (in-flight immune from count gate)", n)
	}
	// In-flight run-dir must still exist.
	if _, err := os.Stat(filepath.Join(dir, "runs", string(inflight.ID))); err != nil {
		t.Errorf("in-flight run-dir gone after count prune: %v", err)
	}
}

func TestPrune_All_SkipsInFlight(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	r1, _ := s.NewRun(time.Unix(1_700_000_000, 0), "p", nil)
	r1.Finalize(time.Unix(1_700_000_001, 0), 0, "succeeded")
	inflight, _ := s.NewRun(time.Unix(1_700_000_002, 0), "p", nil)

	n, err := s.Prune(runstore.PruneOptions{All: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1 (in-flight survives --all)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", string(inflight.ID))); err != nil {
		t.Errorf("in-flight run-dir gone after --all prune: %v", err)
	}
}

func TestPrune_UnfinalizedRunsNotPrunedByAge(t *testing.T) {
	// A run that's still in-flight has EndedAt zero. Age-based prune
	// should NOT remove it (we can't know how old it is until it
	// finalizes), but count-based prune still can.
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-60 * 24 * time.Hour)
	s, _ := runstore.Open(dir, "v")
	rOld, _ := s.NewRun(old, "p", nil)
	rOld.Finalize(old.Add(time.Second), 0, "succeeded")
	// In-flight run (no Finalize call).
	rInflight, _ := s.NewRun(old, "p", nil)
	_ = rInflight

	n, err := s.Prune(runstore.PruneOptions{KeepRuns: 0, KeepDays: 30, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1 (in-flight run is age-immune)", n)
	}
}
