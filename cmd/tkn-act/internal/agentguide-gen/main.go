// Command agentguide-gen copies the curated `docs/agent-guide/` tree
// into the embeddable `cmd/tkn-act/agentguide_data/` directory so that
// `tkn-act agent-guide` can read it via `embed.FS`. The actual mirror
// logic lives in `internal/agentguide.Generate`; this binary is a thin
// flag-parsing wrapper so that the same code path runs from
// `go generate ./cmd/tkn-act/` and from the `agentguide-freshness`
// test (which calls `Generate` directly, without shelling out).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/danielfbm/tkn-act/cmd/tkn-act/internal/agentguide"
)

func main() {
	src := flag.String("src", "../../docs/agent-guide", "source directory holding the user-guide markdown files")
	dst := flag.String("dst", "./agentguide_data", "destination directory under cmd/tkn-act/ for the embedded copy")
	flag.Parse()

	if err := agentguide.Generate(*src, *dst); err != nil {
		fmt.Fprintln(os.Stderr, "agentguide-gen:", err)
		os.Exit(1)
	}
}
