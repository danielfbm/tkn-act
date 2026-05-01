package main

import "github.com/spf13/cobra"

type globalFlags struct {
	output      string
	debug       bool
	cleanup     bool
	maxParallel int
	cluster     bool
	noColor     bool
}

var gf globalFlags

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tkn-act",
		Short: "Run Tekton Pipelines locally on Docker",
		Long:  "tkn-act runs Tekton Tasks and Pipelines locally without a Kubernetes cluster.",
		// default behavior: same as `run` with no args
		RunE: func(c *cobra.Command, args []string) error {
			return runDefault(c, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVarP(&gf.output, "output", "o", "pretty", "output format: pretty | json")
	cmd.PersistentFlags().BoolVar(&gf.debug, "debug", false, "verbose internal logs")
	cmd.PersistentFlags().BoolVar(&gf.cleanup, "cleanup", false, "remove workspace tmpdirs on success and failure")
	cmd.PersistentFlags().IntVar(&gf.maxParallel, "max-parallel", 4, "max concurrent tasks at the same DAG level")
	cmd.PersistentFlags().BoolVar(&gf.cluster, "cluster", false, "use ephemeral k3d cluster instead of Docker")
	cmd.PersistentFlags().BoolVar(&gf.noColor, "no-color", false, "disable color output")

	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newValidateCmd())
	cmd.AddCommand(newVersionCmd())
	return cmd
}
