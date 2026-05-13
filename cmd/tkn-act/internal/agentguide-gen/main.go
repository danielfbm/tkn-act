// Command agentguide-gen copies the curated `docs/agent-guide/` tree
// into the embeddable `cmd/tkn-act/agentguide_data/` directory so that
// `tkn-act agent-guide` can read it via `embed.FS`. It validates that
// the source files match the curated order in
// `cmd/tkn-act/internal/agentguide/order.go` exactly — no missing
// files, no extras.
//
// Run via `go generate ./cmd/tkn-act/` (or `make agentguide`). The
// `agentguide-freshness` test re-runs this tool into a tempdir and
// fails CI on any drift between the checked-in tree and a clean
// regeneration.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danielfbm/tkn-act/cmd/tkn-act/internal/agentguide"
)

func main() {
	src := flag.String("src", "../../docs/agent-guide", "source directory holding the user-guide markdown files")
	dst := flag.String("dst", "./agentguide_data", "destination directory under cmd/tkn-act/ for the embedded copy")
	flag.Parse()

	if err := run(*src, *dst); err != nil {
		fmt.Fprintln(os.Stderr, "agentguide-gen:", err)
		os.Exit(1)
	}
}

func run(src, dst string) error {
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
	for _, section := range agentguide.Order {
		wanted[agentguide.FileName(section)] = true
	}

	var missing, extra []string
	for name := range wanted {
		if _, ok := srcMD[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range srcMD {
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

	if err := os.MkdirAll(dstAbs, 0o755); err != nil {
		return fmt.Errorf("create dst: %w", err)
	}

	for _, section := range agentguide.Order {
		name := agentguide.FileName(section)
		srcPath := filepath.Join(srcAbs, name)
		dstPath := filepath.Join(dstAbs, name)

		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", srcPath, err)
		}
		normalized := normalize(raw)

		existing, readErr := os.ReadFile(dstPath)
		if readErr == nil && bytes.Equal(existing, normalized) {
			continue
		}
		if err := os.WriteFile(dstPath, normalized, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
	}

	dstMD, err := listMarkdown(dstAbs)
	if err != nil {
		return fmt.Errorf("list dst: %w", err)
	}
	for name := range dstMD {
		if wanted[name] {
			continue
		}
		stale := filepath.Join(dstAbs, name)
		if err := os.Remove(stale); err != nil {
			return fmt.Errorf("remove stale %s: %w", stale, err)
		}
	}

	return nil
}

// listMarkdown returns the *.md basenames immediately under dir (no
// recursion — the guide tree is intentionally flat).
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
// guarantees exactly one trailing newline. Keeps interior bytes intact
// so the concatenated output mirrors the source layout.
func normalize(raw []byte) []byte {
	trimmed := bytes.TrimRight(raw, " \t\r\n")
	out := make([]byte, 0, len(trimmed)+1)
	out = append(out, trimmed...)
	out = append(out, '\n')
	return out
}
