package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/runstore"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs [id|latest|<seq>|<ulid-prefix>]",
		Short: "Replay a previously recorded tkn-act run",
		Long: `Replay the JSON event stream of a previously recorded run.

By default tkn-act records every run on disk under
$XDG_DATA_HOME/tkn-act (override with --state-dir / TKN_ACT_STATE_DIR).
Use 'tkn-act runs list' (Phase 3) or 'ls $XDG_DATA_HOME/tkn-act/runs/'
to see recorded ULIDs.

Identifiers:
  (no argument or "latest")  the most recent run
  <N>                        run with sequence number N (1, 2, 3, ...)
  <ulid-prefix>              any unique prefix of a ULID

Output respects the same --output / --color / --quiet / --verbose flags
as 'tkn-act run'.`,
		Example: `  # replay the most recent run, pretty output
  tkn-act logs

  # replay run #7 as JSON
  tkn-act logs 7 -o json

  # replay by ULID prefix
  tkn-act logs 01HQYZAB`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return runLogs(os.Stdout, id)
		},
	}
	return cmd
}

// runLogs is the io.Writer-friendly entry point for tests.
func runLogs(w io.Writer, id string) error {
	stateDir := runstore.ResolveStateDir(gf.stateDir)
	store, err := runstore.Open(stateDir, version)
	if err != nil {
		return exitcode.Wrap(exitcode.Env, fmt.Errorf("open state-dir: %w", err))
	}
	entry, err := store.Resolve(id)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	rep, err := buildReporter(w)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	defer rep.Close()
	eventsPath := filepath.Join(store.RunDir(entry), "events.jsonl")
	if err := runstore.Replay(eventsPath, rep); err != nil {
		return exitcode.Wrap(exitcode.Env, err)
	}
	return nil
}
