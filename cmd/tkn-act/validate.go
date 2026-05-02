package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/danielfbm/tkn-act/internal/discovery"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/validator"
	"github.com/spf13/cobra"
)

type validateResult struct {
	OK       bool     `json:"ok"`
	Pipeline string   `json:"pipeline"`
	Errors   []string `json:"errors"`
}

func newValidateCmd() *cobra.Command {
	var (
		files []string
		dir   string
		pipe  string
	)
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Schema and DAG checks on a pipeline (no run)",
		Long: `Run schema and DAG checks on a Pipeline without executing anything.

Useful for AI agents and CI: a clean exit code 0 means the YAML is valid for
tkn-act; exit code 4 means the YAML was rejected.`,
		Example: `  # Validate the discovered pipeline in the current dir
  tkn-act validate

  # Validate a specific file, JSON output for scripting
  tkn-act validate -f pipeline.yaml -o json`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if len(files) == 0 {
				if dir == "" {
					dir = "."
				}
				disc, err := discovery.Find(dir)
				if err != nil {
					return exitcode.Wrap(exitcode.Usage, err)
				}
				files = disc
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				return exitcode.Wrap(exitcode.Validate, err)
			}
			if pipe == "" {
				if len(b.Pipelines) != 1 {
					return exitcode.Wrap(exitcode.Usage, fmt.Errorf("multiple pipelines loaded; specify -p"))
				}
				for n := range b.Pipelines {
					pipe = n
				}
			}
			errs := validator.Validate(b, pipe, nil)
			msgs := make([]string, len(errs))
			for i, e := range errs {
				msgs[i] = e.Error()
			}
			if gf.output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(validateResult{OK: len(errs) == 0, Pipeline: pipe, Errors: msgs})
			} else {
				for _, m := range msgs {
					fmt.Println("error:", m)
				}
				if len(errs) == 0 {
					fmt.Println("ok")
				}
			}
			if len(errs) > 0 {
				return exitcode.Wrap(exitcode.Validate, fmt.Errorf("%d validation error(s)", len(errs)))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&files, "file", "f", nil, "file(s) to validate")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "directory to scan")
	cmd.Flags().StringVarP(&pipe, "pipeline", "p", "", "pipeline name")
	return cmd
}
