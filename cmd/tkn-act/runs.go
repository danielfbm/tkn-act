package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/runstore"
	"github.com/spf13/cobra"
)

func newRunsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Manage the local store of past tkn-act runs",
		Long: `Inspect and prune the on-disk store populated by 'tkn-act run'.

The state-dir defaults to $XDG_DATA_HOME/tkn-act (override via
--state-dir / TKN_ACT_STATE_DIR). Each ` + "`tkn-act run`" + ` adds a
record there; retention defaults keep the most recent 50 runs and
drop anything older than 30 days (configurable via
TKN_ACT_KEEP_RUNS / TKN_ACT_KEEP_DAYS).`,
	}
	cmd.AddCommand(newRunsListCmd(), newRunsShowCmd(), newRunsPruneCmd())
	return cmd
}

func newRunsListCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent stored runs (newest last)",
		Example: `  # 20 most-recent runs as a table
  tkn-act runs list

  # everything, as JSON
  tkn-act runs list --all -o json`,
		RunE: func(c *cobra.Command, _ []string) error {
			return runRunsList(os.Stdout, all)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "show every recorded run (default: most-recent 20)")
	return c
}

const defaultListLimit = 20

// runRunsList writes the index entries to w. When the state-dir
// doesn't exist yet the result is an empty list, not an error —
// "no runs recorded" is a valid state for a fresh user.
func runRunsList(w io.Writer, all bool) error {
	stateDir := runstore.ResolveStateDir(gf.stateDir)
	store, err := runstore.OpenReadOnly(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return emitRunsList(w, nil)
		}
		return exitcode.Wrap(exitcode.Env, fmt.Errorf("open state-dir: %w", err))
	}
	idx, err := runstore.OpenIndex(store.Dir())
	if err != nil {
		return exitcode.Wrap(exitcode.Env, err)
	}
	defer idx.Close()
	entries := idx.All()
	if !all && len(entries) > defaultListLimit {
		entries = entries[len(entries)-defaultListLimit:]
	}
	return emitRunsList(w, entries)
}

func emitRunsList(w io.Writer, entries []runstore.IndexEntry) error {
	if gf.output == "json" {
		if entries == nil {
			entries = []runstore.IndexEntry{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tULID\tpipeline\tstarted\tduration\texit\tstatus")
	for _, e := range entries {
		dur := "-"
		if !e.EndedAt.IsZero() && !e.StartedAt.IsZero() {
			dur = e.EndedAt.Sub(e.StartedAt).Round(time.Millisecond).String()
		}
		started := "-"
		if !e.StartedAt.IsZero() {
			started = e.StartedAt.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
			e.Seq, e.ULID, e.PipelineRef, started, dur, e.ExitCode, e.Status)
	}
	return tw.Flush()
}

func newRunsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show metadata for one stored run",
		Args:  cobra.ExactArgs(1),
		Example: `  tkn-act runs show 7
  tkn-act runs show latest -o json
  tkn-act runs show 01HQYZAB`,
		RunE: func(c *cobra.Command, args []string) error {
			return runRunsShow(os.Stdout, args[0])
		},
	}
}

func runRunsShow(w io.Writer, id string) error {
	stateDir := runstore.ResolveStateDir(gf.stateDir)
	store, err := runstore.OpenReadOnly(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return exitcode.Wrap(exitcode.Usage,
				fmt.Errorf("no runs recorded in %s", stateDir))
		}
		return exitcode.Wrap(exitcode.Env, err)
	}
	entry, err := store.Resolve(id)
	if err != nil {
		if errors.Is(err, runstore.ErrNoRuns) ||
			errors.Is(err, runstore.ErrNotFound) ||
			errors.Is(err, runstore.ErrAmbiguous) {
			return exitcode.Wrap(exitcode.Usage, err)
		}
		return exitcode.Wrap(exitcode.Env, err)
	}
	meta, err := runstore.ReadMeta(filepath.Join(store.RunDir(entry), "meta.json"))
	if err != nil {
		return exitcode.Wrap(exitcode.Env, err)
	}
	if gf.output == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(meta)
	}
	fmt.Fprintf(w, "seq:           %d\n", meta.Seq)
	fmt.Fprintf(w, "ulid:          %s\n", meta.ULID)
	fmt.Fprintf(w, "pipeline_ref:  %s\n", meta.PipelineRef)
	fmt.Fprintf(w, "started_at:    %s\n", meta.StartedAt.Local())
	if !meta.EndedAt.IsZero() {
		fmt.Fprintf(w, "ended_at:      %s\n", meta.EndedAt.Local())
	}
	fmt.Fprintf(w, "exit_code:     %d\n", meta.ExitCode)
	fmt.Fprintf(w, "status:        %s\n", meta.Status)
	fmt.Fprintf(w, "writer:        %s\n", meta.WriterVersion)
	if len(meta.Args) > 0 {
		fmt.Fprintf(w, "args:          %v\n", meta.Args)
	}
	return nil
}

func newRunsPruneCmd() *cobra.Command {
	var all, yes bool
	c := &cobra.Command{
		Use:   "prune",
		Short: "Apply retention policy now (or delete every run with --all)",
		Long: `Prune stored runs. With no flags, applies the same retention policy
'tkn-act run' applies automatically: keep the most recent
TKN_ACT_KEEP_RUNS (default 50) AND drop runs older than
TKN_ACT_KEEP_DAYS (default 30) days.

--all wipes every recorded run; --yes/-y is required to confirm.`,
		RunE: func(c *cobra.Command, _ []string) error {
			return runRunsPrune(os.Stdout, all, yes)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "delete every stored run")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation gate that --all requires")
	return c
}

func runRunsPrune(w io.Writer, all, yes bool) error {
	stateDir := runstore.ResolveStateDir(gf.stateDir)
	if all && !yes {
		return exitcode.Wrap(exitcode.Usage,
			errors.New("--all is destructive; pass --yes/-y to confirm"))
	}
	store, err := runstore.OpenReadOnly(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "No state-dir at %s; nothing to prune.\n", stateDir)
			return nil
		}
		return exitcode.Wrap(exitcode.Env, err)
	}
	opts := retentionOpts()
	if all {
		opts.All = true
	}
	n, err := store.Prune(opts)
	if err != nil {
		return exitcode.Wrap(exitcode.Env, err)
	}
	fmt.Fprintf(w, "Pruned %d run(s) from %s\n", n, stateDir)
	return nil
}
