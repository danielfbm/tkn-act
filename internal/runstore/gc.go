package runstore

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// PruneOptions controls retention.
//
// KeepRuns=0 disables count-based pruning (keeps everything by count).
// KeepDays=0 disables age-based pruning (keeps everything by age).
// Now defaults to time.Now() when zero.
// All=true overrides both gates and deletes every recorded run
// EXCEPT in-flight ones — wiping an in-flight peer's directory
// mid-write would corrupt its events.jsonl. Abandoned runs (never
// finalized, writer gone; see IndexEntry.Abandoned) are not in flight
// and are deleted.
//
// When both KeepRuns and KeepDays are positive, BOTH gates apply
// (intersection): a run is kept only if it's in the most-recent
// KeepRuns AND within the last KeepDays. This matches the spec's
// "Last N AND > D days, whichever hits first" policy.
//
// In-flight runs are immune to BOTH gates. Abandoned runs take part
// in both: they count toward KeepRuns and age by StartedAt.
//
// Reserve is the number of KeepRuns slots held back for runs about to
// be created: `tkn-act run` prunes with Reserve=1 right before it
// records itself so the store ends up with exactly KeepRuns runs, not
// KeepRuns+1.
//
// Warn, when non-nil, receives one message per index entry that was
// skipped because its ULID is malformed (a hand-edited or corrupt
// index.json); such entries are never turned into filesystem paths.
type PruneOptions struct {
	KeepRuns int
	KeepDays int
	All      bool
	Now      time.Time
	Reserve  int
	Warn     func(string)
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

	// Entries whose ULID doesn't parse are skipped entirely: they are
	// kept in the index (we don't own them) and never joined into a
	// path (a crafted "../../victim" would escape the state dir).
	valid := make([]IndexEntry, 0, len(entries))
	for _, e := range entries {
		if _, perr := ParseRunID(e.ULID); perr != nil {
			if opts.Warn != nil {
				opts.Warn(fmt.Sprintf("skipping index entry seq %d: %v", e.Seq, perr))
			}
			continue
		}
		valid = append(valid, e)
	}

	// Candidates for deletion: everything not in flight.
	prunable := make([]IndexEntry, 0, len(valid))
	for _, e := range valid {
		if !e.InFlight(opts.Now) {
			prunable = append(prunable, e)
		}
	}

	if opts.All {
		for _, e := range prunable {
			keep[e.Seq] = false
		}
	} else {
		if opts.KeepRuns > 0 {
			n := opts.KeepRuns - opts.Reserve
			if n < 0 {
				n = 0
			}
			if len(prunable) > n {
				for _, e := range prunable[:len(prunable)-n] {
					keep[e.Seq] = false
				}
			}
		}
		if opts.KeepDays > 0 {
			threshold := opts.Now.Add(-time.Duration(opts.KeepDays) * 24 * time.Hour)
			for _, e := range prunable {
				ref := e.EndedAt
				if ref.IsZero() {
					ref = e.StartedAt // abandoned: age by start
				}
				if ref.Before(threshold) {
					keep[e.Seq] = false
				}
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
		path, perr := s.RunDir(e)
		if perr != nil { // unreachable: invalid entries are always kept
			survivors = append(survivors, e)
			continue
		}
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
