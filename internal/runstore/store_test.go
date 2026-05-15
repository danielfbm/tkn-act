package runstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/runstore"
)

func TestStore_NewRun(t *testing.T) {
	dir := t.TempDir()
	s, err := runstore.Open(dir, "tkn-act-test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r, err := s.NewRun(time.Now(), "pipeline.yaml", []string{"run", "-f", "pipeline.yaml"})
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if r.Seq != 1 {
		t.Errorf("Seq = %d, want 1", r.Seq)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs", string(r.ID))); err != nil {
		t.Errorf("run dir missing: %v", err)
	}
	if _, err := runstore.ReadMeta(filepath.Join(dir, "runs", string(r.ID), "meta.json")); err != nil {
		t.Errorf("meta.json missing: %v", err)
	}
}

func TestStore_NewRun_AssignsIncrementingSeq(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	r1, _ := s.NewRun(time.Unix(1_700_000_000, 0), "p", nil)
	r2, _ := s.NewRun(time.Unix(1_700_000_001, 0), "p", nil)
	r3, _ := s.NewRun(time.Unix(1_700_000_002, 0), "p", nil)
	if r1.Seq != 1 || r2.Seq != 2 || r3.Seq != 3 {
		t.Errorf("seqs = (%d,%d,%d), want (1,2,3)", r1.Seq, r2.Seq, r3.Seq)
	}
}

func TestStore_Finalize(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "tkn-act-test")
	start := time.Unix(1_700_000_000, 0)
	r, _ := s.NewRun(start, "pipeline.yaml", nil)
	end := time.Unix(1_700_000_010, 0)
	if err := r.Finalize(end, 0, "succeeded"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	m, _ := runstore.ReadMeta(filepath.Join(r.Dir, "meta.json"))
	if m.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", m.Status)
	}
	if m.EndedAt.IsZero() {
		t.Errorf("EndedAt is zero")
	}
	if !m.EndedAt.Equal(end) {
		t.Errorf("EndedAt = %v, want %v", m.EndedAt, end)
	}
	// index.json must mirror the finalize update.
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	e, _ := idx.BySeq(r.Seq)
	if e.Status != "succeeded" || !e.EndedAt.Equal(end) {
		t.Errorf("index entry not updated: %+v", e)
	}
}

func TestStore_EventsPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	r, _ := s.NewRun(time.Now(), "p", nil)
	want := filepath.Join(r.Dir, "events.jsonl")
	if r.EventsPath() != want {
		t.Errorf("EventsPath = %q, want %q", r.EventsPath(), want)
	}
}

func TestStore_Resolve(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	r1, _ := s.NewRun(time.Unix(1, 0), "a.yaml", nil)
	r2, _ := s.NewRun(time.Unix(2, 0), "b.yaml", nil)

	cases := []struct {
		in      string
		wantSeq int
	}{
		{"", r2.Seq},
		{"latest", r2.Seq},
		{"1", r1.Seq},
		{"2", r2.Seq},
		{string(r1.ID)[:8], r1.Seq},
	}
	for _, tc := range cases {
		e, err := s.Resolve(tc.in)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.in, err)
			continue
		}
		if e.Seq != tc.wantSeq {
			t.Errorf("Resolve(%q).Seq = %d, want %d", tc.in, e.Seq, tc.wantSeq)
		}
	}
}

func TestStore_Resolve_Errors(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	if _, err := s.Resolve("latest"); err == nil {
		t.Errorf("Resolve(latest) on empty: want error")
	}
	s.NewRun(time.Now(), "", nil)
	if _, err := s.Resolve("99"); err == nil {
		t.Errorf("Resolve(99): want not-found")
	}
}

func TestStore_RunDir(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	r, _ := s.NewRun(time.Now(), "p", nil)
	got := s.RunDir(runstore.IndexEntry{ULID: string(r.ID)})
	want := filepath.Join(dir, "runs", string(r.ID))
	if got != want {
		t.Errorf("RunDir = %q, want %q", got, want)
	}
}
