package runstore

import (
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
// All=true overrides both gates and deletes every recorded run.
//
// When both KeepRuns and KeepDays are positive, BOTH gates apply
// (intersection): a run is kept only if it's in the most-recent
// KeepRuns AND within the last KeepDays. This matches the spec's
// "Last N AND > D days, whichever hits first" policy.
//
// In-flight runs (EndedAt zero) are immune to age-based pruning —
// we can't know whether they're old or just started.
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
		if opts.KeepRuns > 0 && len(entries) > opts.KeepRuns {
			for _, e := range entries[:len(entries)-opts.KeepRuns] {
				keep[e.Seq] = false
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
		for _, e := range entries {
			keep[e.Seq] = false
		}
	}

	deleted := 0
	survivors := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		if keep[e.Seq] {
			survivors = append(survivors, e)
			continue
		}
		path := filepath.Join(s.dir, "runs", e.ULID)
		if err := os.RemoveAll(path); err != nil {
			return deleted, err
		}
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := idx.replaceEntries(survivors); err != nil {
		return deleted, err
	}
	return deleted, nil
}
