package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/validator"
	"github.com/spf13/cobra"
)

type validateResult struct {
	OK       bool     `json:"ok"`
	Pipeline string   `json:"pipeline"`
	Errors   []string `json:"errors"`
}

func emitValidateJSONResult(res validateResult) {
	if gf.output != "json" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}

func emitValidateJSONError(pipeline string, err error) {
	emitValidateJSONResult(validateResult{
		OK:       false,
		Pipeline: pipeline,
		Errors:   []string{err.Error()},
	})
}

func newValidateCmd() *cobra.Command {
	var (
		files  []string
		dir    string
		pipe   string
		params []string
	)
	cmd := &cobra.Command{
		Use:   "validate [file...]",
		Short: "Schema and DAG checks on a pipeline (no run)",
		Long: `Run schema and DAG checks on a Pipeline without executing anything.

Useful for AI agents and CI: a clean exit code 0 means the YAML is valid for
tkn-act; exit code 4 means the YAML was rejected. validate applies the same
resolver-aware checks as 'run' (--resolver-allow, --cluster-resolver-*,
--remote-resolver-context, --offline), so exit 0 here means 'run' will not
reject the pipeline at validation time.

Positional arguments are treated as additional -f files. Required pipeline
params are checked only when at least one --param is given (validate
cannot know which params a later 'run' will pass otherwise).`,
		Example: `  # Validate the discovered pipeline in the current dir
  tkn-act validate

  # Validate a specific file (positional form is equivalent to -f)
  tkn-act validate pipeline.yaml

  # Validate a specific file, JSON output for scripting
  tkn-act validate -f pipeline.yaml -o json`,
		RunE: func(_ *cobra.Command, args []string) error {
			files, err := resolveInputFiles(files, args, dir)
			if err != nil {
				emitValidateJSONError("", err)
				return err
			}
			b, err := loader.LoadFiles(files)
			if err != nil {
				emitValidateJSONError("", err)
				return exitcode.Wrap(exitcode.Validate, err)
			}
			pipe, err := selectPipeline(b, pipe)
			if err != nil {
				emitValidateJSONError("", err)
				return err
			}
			var provided map[string]bool
			if len(params) > 0 {
				_, provided, err = parseParams(params)
				if err != nil {
					emitValidateJSONError(pipe, err)
					return err
				}
			}
			diskCache := refresolver.NewDiskCache(resolveResolverCacheDir(gf.resolverCacheDir))
			errs := validator.ValidateWithOptions(b, pipe, provided, buildValidatorOptions(diskCache))
			msgs := make([]string, len(errs))
			for i, e := range errs {
				msgs[i] = e.Error()
			}
			if gf.output == "json" {
				emitValidateJSONResult(validateResult{OK: len(errs) == 0, Pipeline: pipe, Errors: msgs})
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
	cmd.Flags().StringArrayVar(&params, "param", nil, "param key=value (repeatable); when given, required pipeline params are checked")
	return cmd
}
