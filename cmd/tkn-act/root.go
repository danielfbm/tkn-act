package main

import "github.com/spf13/cobra"

type globalFlags struct {
	output       string
	debug        bool
	cleanup      bool
	maxParallel  int
	cluster      bool
	noColor      bool
	color        string // auto|always|never
	quiet        bool
	verbose      bool
	configMapDir string
	secretDir    string
	configMaps   []string // <name>=<k>=<v>[,<k>=<v>...]
	secrets      []string
}

var gf globalFlags

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tkn-act",
		Short: "Run Tekton Pipelines locally on Docker (Tekton's `act`)",
		Long: `tkn-act runs Tekton Tasks and Pipelines locally without a Kubernetes cluster.

Designed for both humans and AI agents:
  - Every command supports --output json with stable shapes.
  - Every error returns a documented, stable exit code (see 'tkn-act agent-guide').
  - 'tkn-act help-json' emits the full command+flag tree as JSON.
  - 'tkn-act doctor' verifies the local environment.
  - 'tkn-act agent-guide' prints the embedded AI-agent guide (AGENTS.md).`,
		Example: `  # Auto-discover and run a pipeline
  tkn-act

  # Run a specific file with JSON event output
  tkn-act run -f pipeline.yaml -o json

  # Verify the environment
  tkn-act doctor -o json

  # Introspect the CLI surface programmatically
  tkn-act help-json`,
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
	cmd.PersistentFlags().BoolVar(&gf.noColor, "no-color", false, "disable color output (alias for --color=never)")
	cmd.PersistentFlags().StringVar(&gf.color, "color", "auto", "color mode: auto | always | never")
	cmd.PersistentFlags().BoolVarP(&gf.quiet, "quiet", "q", false, "suppress step logs and pipeline header (pretty output)")
	cmd.PersistentFlags().BoolVarP(&gf.verbose, "verbose", "v", false, "show step boundaries in addition to step logs (pretty output)")
	cmd.PersistentFlags().StringVar(&gf.configMapDir, "configmap-dir", "", "directory to resolve configMap volumes from (default $XDG_CACHE_HOME/tkn-act/configmaps)")
	cmd.PersistentFlags().StringVar(&gf.secretDir, "secret-dir", "", "directory to resolve secret volumes from (default $XDG_CACHE_HOME/tkn-act/secrets)")
	cmd.PersistentFlags().StringArrayVar(&gf.configMaps, "configmap", nil, "inline configMap as <name>=<k>=<v>[,<k>=<v>...] (repeatable)")
	cmd.PersistentFlags().StringArrayVar(&gf.secrets, "secret", nil, "inline secret as <name>=<k>=<v>[,<k>=<v>...] (repeatable)")

	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newValidateCmd())
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newClusterCmd())
	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newHelpJSONCmd())
	cmd.AddCommand(newAgentGuideCmd())
	return cmd
}
