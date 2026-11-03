package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeIndex(t *testing.T, dir string, entries []IndexEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(indexFile{NextSeq: len(entries) + 1, Entries: entries})
	if err := os.WriteFile(filepath.Join(dir, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// E5: a crafted ULID in index.json must never become a path outside
// the state dir.
func TestPrune_RejectsTraversalULID(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "a", "b", "state")
	victim := filepath.Join(root, "a", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIndex(t, state, []IndexEntry{{
		Seq: 1, ULID: "../../victim", StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0), Status: "succeeded",
	}})
	s, _ := Open(state, "v")
	var warns []string
	n, err := s.Prune(PruneOptions{All: true, Warn: func(m string) { warns = append(warns, m) }})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d, want 0", n)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim directory outside state dir was deleted: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "invalid run id") {
		t.Errorf("warns = %v", warns)
	}
	if _, err := s.RunDir(IndexEntry{ULID: "../../victim"}); err == nil {
		t.Error("RunDir accepted traversal ULID")
	}
	if _, err := s.Resolve("latest"); err == nil {
		t.Error("Resolve returned a traversal entry")
	}
	// 26 chars of non-base32 (non-strict ulid.Parse would accept it).
	if _, err := ParseRunID("../../../../../../../vict/"); err == nil {
		t.Error("ParseRunID accepted 26-char traversal string")
	}
}

func withLiveness(t *testing.T, alive bool) {
	t.Helper()
	orig := processAlive
	processAlive = func(int) bool { return alive }
	t.Cleanup(func() { processAlive = orig })
}

// E6: crashed runs (never finalized, writer gone) are abandoned and
// prunable; zero end times are omitted from JSON.
func TestAbandonedRuns(t *testing.T) {
	now := time.Now()
	host := hostname()
	deadLocal := IndexEntry{ULID: string(NewRunID(now)), StartedAt: now, PID: 999999, Host: host}
	withLiveness(t, false)
	if !deadLocal.Abandoned(now) || deadLocal.InFlight(now) || deadLocal.EffectiveStatus(now) != StatusUnknown {
		t.Errorf("dead local writer should be abandoned")
	}
	withLiveness(t, true)
	if deadLocal.Abandoned(now) || deadLocal.EffectiveStatus(now) != StatusRunning {
		t.Errorf("live local writer should be running")
	}
	legacyOld := IndexEntry{StartedAt: now.Add(-2 * AbandonAfter)}
	legacyNew := IndexEntry{StartedAt: now.Add(-time.Minute)}
	otherHost := IndexEntry{StartedAt: now.Add(-time.Minute), PID: 1, Host: host + "-other"}
	if !legacyOld.Abandoned(now) || legacyNew.Abandoned(now) || otherHost.Abandoned(now) {
		t.Errorf("age fallback wrong")
	}
	fin := IndexEntry{EndedAt: now, Status: "failed"}
	if fin.EffectiveStatus(now) != "failed" {
		t.Errorf("finalized status must be recorded one")
	}

	data, _ := json.Marshal(IndexEntry{ULID: "x", StartedAt: now})
	if strings.Contains(string(data), "ended_at") {
		t.Errorf("zero ended_at serialized: %s", data)
	}
	mdata, _ := json.Marshal(Meta{RunID: "x"})
	if strings.Contains(string(mdata), "0001-01-01") {
		t.Errorf("zero time serialized in meta: %s", mdata)
	}
}

func TestPrune_AbandonedRunsArePrunable(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "v")
	now := time.Now()
	crashed, _ := s.NewRun(now.Add(-time.Hour), "p", nil) // records our PID
	fin, _ := s.NewRun(now, "p", nil)
	_ = fin.Finalize(now, 0, "succeeded")

	withLiveness(t, false) // crashed's writer is "gone"
	n, err := s.Prune(PruneOptions{All: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("--all pruned %d, want 2 (abandoned included)", n)
	}
	if _, err := os.Stat(crashed.Dir); !os.IsNotExist(err) {
		t.Errorf("abandoned run dir survived --all")
	}
}

func TestPrune_AbandonedCountsTowardKeepRunsAndAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)
	s, _ := Open(dir, "v")
	_, _ = s.NewRun(old, "p", nil)
	withLiveness(t, false)
	n, err := s.Prune(PruneOptions{KeepDays: 30, Now: now})
	if err != nil || n != 1 {
		t.Fatalf("age prune of abandoned: n=%d err=%v", n, err)
	}
	r1, _ := s.NewRun(now.Add(-2*time.Hour), "p", nil)
	r2, _ := s.NewRun(now.Add(-time.Hour), "p", nil)
	_ = r2.Finalize(now, 0, "succeeded")
	n, err = s.Prune(PruneOptions{KeepRuns: 1, Now: now})
	if err != nil || n != 1 {
		t.Fatalf("count prune: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(r1.Dir); !os.IsNotExist(err) {
		t.Errorf("oldest (abandoned) run should be the one pruned")
	}
}

func TestPrune_Reserve(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "v")
	for i := 0; i < 4; i++ {
		r, _ := s.NewRun(time.Unix(int64(1_700_000_000+i), 0), "p", nil)
		_ = r.Finalize(time.Unix(int64(1_700_000_000+i), 1), 0, "succeeded")
	}
	n, err := s.Prune(PruneOptions{KeepRuns: 1, Reserve: 1, Now: time.Unix(1_700_000_100, 0)})
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v, want all 4 pruned (KeepRuns 1 - Reserve 1)", n, err)
	}
}

// E10: ULID prefixes are case-insensitive; ambiguity lists candidates;
// read-only stores don't create index.lock.
func TestResolve_PrefixCaseAndCandidates(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "v")
	t0 := time.Unix(1_700_000_000, 0)
	r1, _ := s.NewRun(t0, "alpha", nil)
	_, _ = s.NewRun(t0, "beta", nil) // same ms → shared 10-char prefix
	_ = os.Remove(filepath.Join(dir, "index.lock"))

	ro, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ro.Resolve(strings.ToLower(string(r1.ID)))
	if err != nil || e.ULID != string(r1.ID) {
		t.Fatalf("lower-case full ULID: e=%v err=%v", e, err)
	}
	_, err = ro.Resolve(strings.ToLower(string(r1.ID)[:10]))
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ambiguous, got %v", err)
	}
	if !strings.Contains(err.Error(), "#1 ") || !strings.Contains(err.Error(), "#2 ") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("candidates missing from %q", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.lock")); !os.IsNotExist(err) {
		t.Errorf("read-only Resolve created index.lock")
	}
}
