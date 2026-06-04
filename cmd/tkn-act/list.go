package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/danielfbm/tkn-act/internal/discovery"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/spf13/cobra"
)

type listResult struct {
	Pipelines []string `json:"pipelines"`
	Tasks     []string `json:"tasks"`
}

func newListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Pipelines and Tasks discovered in the project",
		Long: `Discover and list every Tekton Pipeline and Task tkn-act would load
from the given directory (default: cwd).`,
		Example: `  # List discovered Pipelines and Tasks
  tkn-act list

  # JSON output (stable shape, easy for AI agents to parse)
  tkn-act list -o json`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if dir == "" {
				dir = "."
			}
			files, err := discovery.Find(dir)
			if err != nil {
				// A directory with no Tekton YAML is a valid empty result for
				// `list` (a query), not a usage error — emit empty arrays and
				// exit 0. Any other discovery error (e.g. unreadable dir) is
				// still a usage error.
				if errors.Is(err, discovery.ErrNoneFound) {
					return emitListResult(listResult{Pipelines: []string{}, Tasks: []string{}})
				}
				return exitcode.Wrap(exitcode.Usage, err)
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				return exitcode.Wrap(exitcode.Validate, err)
			}
			pipes := make([]string, 0, len(b.Pipelines))
			for n := range b.Pipelines {
				pipes = append(pipes, n)
			}
			tasks := make([]string, 0, len(b.Tasks))
			for n := range b.Tasks {
				tasks = append(tasks, n)
			}
			sort.Strings(pipes)
			sort.Strings(tasks)
			return emitListResult(listResult{Pipelines: pipes, Tasks: tasks})
		},
	}
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "directory to scan (default: cwd)")
	return cmd
}

// emitListResult writes the discovered Pipelines/Tasks in the requested
// format. Empty slices serialize as [] (never null) so the JSON shape stays
// stable for agents even when nothing was discovered.
func emitListResult(res listResult) error {
	if gf.output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Println("Pipelines:")
	for _, n := range res.Pipelines {
		fmt.Printf("  - %s\n", n)
	}
	fmt.Println("Tasks:")
	for _, n := range res.Tasks {
		fmt.Printf("  - %s\n", n)
	}
	return nil
}
