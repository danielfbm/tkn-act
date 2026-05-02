package main

import (
	"encoding/json"
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
		RunE: func(c *cobra.Command, _ []string) error {
			if dir == "" {
				dir = "."
			}
			files, err := discovery.Find(dir)
			if err != nil {
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

			if gf.output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(listResult{Pipelines: pipes, Tasks: tasks})
			}
			fmt.Println("Pipelines:")
			for _, n := range pipes {
				fmt.Printf("  - %s\n", n)
			}
			fmt.Println("Tasks:")
			for _, n := range tasks {
				fmt.Printf("  - %s\n", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "directory to scan (default: cwd)")
	return cmd
}
