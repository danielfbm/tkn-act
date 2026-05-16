package runstore_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielfbm/tkn-act/internal/runstore"
)

func TestIndex_AppendAndLookup(t *testing.T) {
	dir := t.TempDir()
	idx, err := runstore.OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	seq1, err := idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001", PipelineRef: "a"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	seq2, err := idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002", PipelineRef: "b"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq1 != 1 || seq2 != 2 {
		t.Errorf("seq: got (%d,%d), want (1,2)", seq1, seq2)
	}

	e, err := idx.BySeq(2)
	if err != nil {
		t.Fatalf("BySeq: %v", err)
	}
	if e.ULID != "01HQYZAB000000000000000002" {
		t.Errorf("BySeq(2).ULID = %q", e.ULID)
	}
}

func TestIndex_BySeq_NotFound(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	if _, err := idx.BySeq(99); err == nil {
		t.Errorf("BySeq(99) on empty index: want error")
	}
}

func TestIndex_ByULIDPrefix(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	idx.Append(runstore.IndexEntry{ULID: "01HQAAAAA0000000000000001A"})
	idx.Append(runstore.IndexEntry{ULID: "01HQBBBBB0000000000000002B"})
	idx.Append(runstore.IndexEntry{ULID: "01HQBBBBB0000000000000003C"})

	// unique prefix matches one
	e, err := idx.ByULIDPrefix("01HQAAAA")
	if err != nil {
		t.Fatalf("unique prefix: %v", err)
	}
	if e.ULID != "01HQAAAAA0000000000000001A" {
		t.Errorf("got %q", e.ULID)
	}
	// ambiguous prefix
	if _, err := idx.ByULIDPrefix("01HQB"); err == nil {
		t.Errorf("ambiguous prefix: want error")
	}
	// no match
	if _, err := idx.ByULIDPrefix("ZZZZ"); err == nil {
		t.Errorf("no match: want error")
	}
}

func TestIndex_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	seqs := make([]int, 20)
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx, err := runstore.OpenIndex(dir)
			if err != nil {
				errs[i] = err
				return
			}
			defer idx.Close()
			s, err := idx.Append(runstore.IndexEntry{ULID: ""})
			seqs[i] = s
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	seen := map[int]bool{}
	for _, s := range seqs {
		if seen[s] {
			t.Errorf("duplicate seq %d", s)
		}
		seen[s] = true
	}
	if len(seen) != 20 {
		t.Errorf("got %d unique seqs, want 20", len(seen))
	}
}

func TestIndex_Latest(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	if _, err := idx.Latest(); err == nil {
		t.Errorf("Latest on empty index should error")
	}
	idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001"})
	idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002"})
	e, err := idx.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if e.Seq != 2 {
		t.Errorf("Latest.Seq = %d, want 2", e.Seq)
	}
}

func TestIndex_Update(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	defer idx.Close()
	seq, _ := idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001", Status: ""})
	if err := idx.Update(seq, func(e *runstore.IndexEntry) {
		e.Status = "succeeded"
		e.ExitCode = 0
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	e, _ := idx.BySeq(seq)
	if e.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", e.Status)
	}
}

func TestReadIndexEntries_AbsentReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-existed")
	entries, err := runstore.ReadIndexEntries(dir)
	if err != nil {
		t.Errorf("absent dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %d, want 0", len(entries))
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("ReadIndexEntries created %s as side effect", dir)
	}
}

func TestReadIndexEntries_EmptyFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadIndexEntries(dir)
	if err != nil {
		t.Errorf("empty file: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %d, want 0", len(entries))
	}
}

func TestReadIndexEntries_RoundTripFromOpenIndex(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001", PipelineRef: "a.yaml"})
	idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002", PipelineRef: "b.yaml"})
	idx.Close()

	entries, err := runstore.ReadIndexEntries(dir)
	if err != nil {
		t.Fatalf("ReadIndexEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("entries = %d, want 2", len(entries))
	}
	if entries[1].PipelineRef != "b.yaml" {
		t.Errorf("second entry pipeline = %q", entries[1].PipelineRef)
	}
}

func TestReadIndexEntries_CorruptReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runstore.ReadIndexEntries(dir); err == nil {
		t.Errorf("want error on corrupt index")
	}
}

func TestIndex_FlushAtomic_NoLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	for i := 0; i < 5; i++ {
		idx.Append(runstore.IndexEntry{ULID: "01HQYZAB00000000000000000" + string(rune('A'+i))})
	}
	idx.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".index-") && strings.HasSuffix(name, ".tmp") {
			t.Errorf("leftover temp file in state-dir: %s", name)
		}
	}
}

func TestIndex_Persists(t *testing.T) {
	dir := t.TempDir()
	idx, _ := runstore.OpenIndex(dir)
	idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001"})
	idx.Close()
	// reopen and read
	idx2, _ := runstore.OpenIndex(dir)
	defer idx2.Close()
	if len(idx2.All()) != 1 {
		t.Errorf("entries after reopen = %d, want 1", len(idx2.All()))
	}
	seq, _ := idx2.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002"})
	if seq != 2 {
		t.Errorf("seq after reopen = %d, want 2", seq)
	}
}
