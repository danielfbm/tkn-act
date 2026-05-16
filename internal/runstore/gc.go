package runstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PruneOptions controls retention.
//
// KeepRuns=0 disables count-based pruning (keeps everything by count).
// KeepDays=0 disables age-based pruning (keeps everything by age).
// Now defaults to time.Now() when zero.
// All=true overrides both gates and deletes every recorded run
// EXCEPT in-flight ones (EndedAt zero) — wiping an in-flight peer's
// directory mid-write would corrupt its events.jsonl. Callers who
// really want to nuke everything should wait for in-flight runs to
// finalize first.
//
// When both KeepRuns and KeepDays are positive, BOTH gates apply
// (intersection): a run is kept only if it's in the most-recent
// KeepRuns AND within the last KeepDays. This matches the spec's
// "Last N AND > D days, whichever hits first" policy.
//
// In-flight runs (EndedAt zero) are immune to BOTH gates — we can't
// know whether they're old or just started, and we can't delete
// their run-dirs without racing the writing process.
type PruneOptions struct {
	KeepRuns int
	KeepDays int
	All      bool
	Now      time.Time
}

// Prune deletes runs from disk according to opts and rewrites the
// index. Returns the number of run directories deleted.
func (s *Store) Prune(opts PruneOptions) (int, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	idx, err := OpenIndex(s.dir)
	if err != nil {
		return 0, err
	}
	defer idx.Close()
	entries := idx.All()
	// Sort oldest-first by Seq so "most recent KeepRuns" is the
	// tail of the slice.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })

	keep := make(map[int]bool, len(entries))
	for _, e := range entries {
		keep[e.Seq] = true
	}

	if !opts.All {
		if opts.KeepRuns > 0 {
			// Apply count-based prune to the FINALIZED subset only.
			// In-flight runs (EndedAt zero) are immune: their run-dirs
			// are being written by concurrent `tkn-act run` processes
			// and deleting them would corrupt events.jsonl mid-stream.
			finalized := make([]IndexEntry, 0, len(entries))
			for _, e := range entries {
				if !e.EndedAt.IsZero() {
					finalized = append(finalized, e)
				}
			}
			if len(finalized) > opts.KeepRuns {
				for _, e := range finalized[:len(finalized)-opts.KeepRuns] {
					keep[e.Seq] = false
				}
			}
		}
		if opts.KeepDays > 0 {
			threshold := opts.Now.Add(-time.Duration(opts.KeepDays) * 24 * time.Hour)
			for _, e := range entries {
				// In-flight runs have EndedAt zero — age is undefined,
				// so we never age-prune them.
				if !e.EndedAt.IsZero() && e.EndedAt.Before(threshold) {
					keep[e.Seq] = false
				}
			}
		}
	} else {
		// --all wipes every FINALIZED run. In-flight ones survive
		// because deleting them races the writing process; the
		// caller can re-run prune --all once the in-flight runs
		// complete.
		for _, e := range entries {
			if !e.EndedAt.IsZero() {
				keep[e.Seq] = false
			}
		}
	}

	// Walk in oldest-first order; collect remove errors so a
	// partial failure still flushes the index for whatever did
	// get deleted (eventually consistent rather than orphaned
	// entries pointing at missing dirs).
	deleted := 0
	survivors := make([]IndexEntry, 0, len(entries))
	var rmErrs []error
	for _, e := range entries {
		if keep[e.Seq] {
			survivors = append(survivors, e)
			continue
		}
		path := filepath.Join(s.dir, "runs", e.ULID)
		if err := os.RemoveAll(path); err != nil {
			rmErrs = append(rmErrs, fmt.Errorf("remove %s: %w", e.ULID, err))
			survivors = append(survivors, e) // keep the index entry; dir lingered
			continue
		}
		deleted++
	}
	if deleted > 0 {
		if err := idx.replaceEntries(survivors); err != nil {
			rmErrs = append(rmErrs, fmt.Errorf("rewrite index: %w", err))
		}
	}
	if len(rmErrs) > 0 {
		return deleted, errors.Join(rmErrs...)
	}
	return deleted, nil
}
