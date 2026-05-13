package agentguide

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Generate mirrors the user-guide files under src into dst, in
// canonical form: each destination file holds exactly the source's
// content trimmed to one trailing newline. Source files are
// validated against Order — every entry must be present, and no
// extra .md files may exist alongside.
//
// Any non-canonical entry already living under dst (stale .md files
// from a rename, non-markdown leftovers, subdirectories) is removed,
// so a clean regenerate produces a tree that mirrors Order exactly.
//
// Generate is the single implementation shared by `agentguide-gen`
// (the generator binary invoked from `go generate ./cmd/tkn-act/`)
// and the freshness test under cmd/tkn-act/, so the two cannot
// drift in behavior.
func Generate(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolve src: %w", err)
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve dst: %w", err)
	}

	srcMD, err := listMarkdown(srcAbs)
	if err != nil {
		return fmt.Errorf("list src: %w", err)
	}

	wanted := map[string]bool{}
	for _, section := range Order {
		wanted[FileName(section)] = true
	}

	if err := validateSources(srcAbs, wanted, srcMD); err != nil {
		return err
	}

	if err := os.MkdirAll(dstAbs, 0o755); err != nil {
		return fmt.Errorf("create dst: %w", err)
	}

	for _, section := range Order {
		name := FileName(section)
		if err := mirrorFile(filepath.Join(srcAbs, name), filepath.Join(dstAbs, name)); err != nil {
			return err
		}
	}

	return sweepStale(dstAbs, wanted)
}

func validateSources(srcAbs string, wanted map[string]bool, found map[string]bool) error {
	var missing, extra []string
	for name := range wanted {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	for name := range found {
		if !wanted[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		return fmt.Errorf("missing in %s: %s (every entry in agentguide.Order must have a matching .md file)", srcAbs, strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("extra in %s: %s (every .md file under docs/agent-guide/ must appear in agentguide.Order)", srcAbs, strings.Join(extra, ", "))
	}
	return nil
}

func mirrorFile(srcPath, dstPath string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	normalized := normalize(raw)

	existing, readErr := os.ReadFile(dstPath)
	if readErr == nil && bytes.Equal(existing, normalized) {
		return nil
	}
	if err := os.WriteFile(dstPath, normalized, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dstPath, err)
	}
	return nil
}

// sweepStale removes any entry under dst that isn't a wanted file.
// Subdirectories and non-markdown leftovers are removed too — the
// dst tree's only legitimate contents are the curated files.
func sweepStale(dstAbs string, wanted map[string]bool) error {
	entries, err := os.ReadDir(dstAbs)
	if err != nil {
		return fmt.Errorf("list dst: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && wanted[e.Name()] {
			continue
		}
		path := filepath.Join(dstAbs, e.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove stale %s: %w", path, err)
		}
	}
	return nil
}

func listMarkdown(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out[e.Name()] = true
	}
	return out, nil
}

// normalize trims trailing whitespace from each source file and
// guarantees exactly one trailing newline.
func normalize(raw []byte) []byte {
	trimmed := bytes.TrimRight(raw, " \t\r\n")
	out := make([]byte, 0, len(trimmed)+1)
	out = append(out, trimmed...)
	out = append(out, '\n')
	return out
}
