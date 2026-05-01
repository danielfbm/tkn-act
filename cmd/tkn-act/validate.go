package main

import (
	"fmt"

	"github.com/dfbmorinigo/tkn-act/internal/discovery"
	"github.com/dfbmorinigo/tkn-act/internal/loader"
	"github.com/dfbmorinigo/tkn-act/internal/validator"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var (
		files []string
		dir   string
		pipe  string
	)
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Schema and DAG checks on a pipeline",
		RunE: func(_ *cobra.Command, _ []string) error {
			if len(files) == 0 {
				if dir == "" {
					dir = "."
				}
				disc, err := discovery.Find(dir)
				if err != nil {
					return err
				}
				files = disc
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				return err
			}
			if pipe == "" {
				if len(b.Pipelines) != 1 {
					return fmt.Errorf("multiple pipelines loaded; specify -p")
				}
				for n := range b.Pipelines {
					pipe = n
				}
			}
			errs := validator.Validate(b, pipe, nil)
			for _, e := range errs {
				fmt.Println("error:", e)
			}
			if len(errs) > 0 {
				return fmt.Errorf("%d validation error(s)", len(errs))
			}
			fmt.Println("ok")
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&files, "file", "f", nil, "file(s) to validate")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "directory to scan")
	cmd.Flags().StringVarP(&pipe, "pipeline", "p", "", "pipeline name")
	return cmd
}
