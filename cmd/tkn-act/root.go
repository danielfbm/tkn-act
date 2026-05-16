package main

import (
	"time"

	"github.com/spf13/cobra"
)

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
	// Resolver scaffolding (Track 1 #9 Phase 1). Direct-mode resolver
	// dispatch hooks land here; concrete resolvers (git/hub/http/...)
	// land in Phase 2-4. Phase 1 only the inline+offline paths actually
	// do anything.
	resolverCacheDir          string
	resolverAllow             []string
	resolverConfig            string
	offline                   bool
	remoteResolverContext     string
	remoteResolverNamespace   string
	remoteResolverTimeout     time.Duration
	resolverAllowInsecureHTTP bool
	// Cluster resolver (Phase 4 of Track 1 #9). The resolver is OFF by
	// default; setting either of the two flags below opts the user in.
	// KUBECONFIG may point at production, so the security stance is
	// "explicit consent required."
	clusterResolverContext    string
	clusterResolverKubeconfig string
	// Sidecar pacing.
	sidecarStartGrace time.Duration
	sidecarStopGrace  time.Duration
	// Remote docker daemon. "auto" (default) compares the daemon's
	// Info.Name to the client hostname; "on" / "off" force the
	// decision. Phase 3 will switch the docker backend's staging
	// strategy when this resolves to "on".
	remoteDocker string
	// pauseImage overrides the per-Task pause / stager image used by
	// the docker backend. Empty falls back to $TKN_ACT_PAUSE_IMAGE and
	// then to the built-in default. Air-gap-friendly: an internal
	// mirror tag goes here once and both consumers pick it up.
	pauseImage string
	// dockerHost overrides the daemon address for this invocation.
	// Empty falls through to $DOCKER_HOST, then to the moby SDK
	// default unix socket. Lets a single CLI invocation target a
	// different daemon without mutating process-wide env.
	dockerHost string
	// stateDir overrides where tkn-act records per-run state
	// (events.jsonl + meta.json). Empty falls through to
	// $TKN_ACT_STATE_DIR, then $XDG_DATA_HOME/tkn-act, then
	// $HOME/.local/share/tkn-act.
	stateDir string
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
  - 'tkn-act agent-guide' prints the embedded AI-agent user guide (sourced from docs/agent-guide/).`,
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
	// Resolver scaffolding (Track 1 #9 Phase 1). Concrete resolvers
	// land in Phase 2-4; in Phase 1 only the inline+offline paths
	// actually do anything yet.
	cmd.PersistentFlags().StringVar(&gf.resolverCacheDir, "resolver-cache-dir", "", "directory for cached resolver bytes (default $XDG_CACHE_HOME/tkn-act/resolved)")
	cmd.PersistentFlags().StringSliceVar(&gf.resolverAllow, "resolver-allow", []string{"git", "hub", "http", "bundles"}, "comma-separated resolver names that may dispatch (security; cluster is opt-in)")
	cmd.PersistentFlags().StringVar(&gf.resolverConfig, "resolver-config", "", "path to a YAML/JSON file with per-resolver settings (auth tokens, mirror URLs, etc.)")
	cmd.PersistentFlags().BoolVar(&gf.offline, "offline", false, "reject any resolver cache miss; useful for hermetic CI")
	cmd.PersistentFlags().StringVar(&gf.remoteResolverContext, "remote-resolver-context", "", "kubeconfig context for Mode B (delegate resolution to a Tekton cluster); unset = direct mode")
	cmd.PersistentFlags().StringVar(&gf.remoteResolverNamespace, "remote-resolver-namespace", "default", "namespace for Mode B ResolutionRequest submissions (only meaningful with --remote-resolver-context)")
	cmd.PersistentFlags().DurationVar(&gf.remoteResolverTimeout, "remote-resolver-timeout", 60*time.Second, "per-request wait budget for Mode B ResolutionRequest reconcile (only meaningful with --remote-resolver-context)")
	cmd.PersistentFlags().BoolVar(&gf.resolverAllowInsecureHTTP, "resolver-allow-insecure-http", false, "allow plain http:// for the http and bundles resolvers (CI-only escape hatch; loopback always permitted)")
	cmd.PersistentFlags().StringVar(&gf.clusterResolverContext, "cluster-resolver-context", "", "kubeconfig context for the `cluster` resolver (off-by-default; setting this opts in)")
	cmd.PersistentFlags().StringVar(&gf.clusterResolverKubeconfig, "cluster-resolver-kubeconfig", "", "explicit kubeconfig path for the cluster resolver (default: KUBECONFIG / ~/.kube/config)")
	// Sidecar pacing.
	cmd.PersistentFlags().DurationVar(&gf.sidecarStartGrace, "sidecar-start-grace", 2*time.Second, "how long to wait after starting all sidecars before launching the first step")
	cmd.PersistentFlags().DurationVar(&gf.sidecarStopGrace, "sidecar-stop-grace", 30*time.Second, "SIGTERM-then-SIGKILL window when stopping sidecars at end of Task (matches upstream Tekton's terminationGracePeriodSeconds)")
	// Remote-docker daemon mode (env: TKN_ACT_REMOTE_DOCKER).
	cmd.PersistentFlags().StringVar(&gf.remoteDocker, "remote-docker", "auto", "remote docker daemon mode: auto | on | off (env: "+envRemoteDocker+")")
	// Pause/stager image override (env: TKN_ACT_PAUSE_IMAGE). Used by
	// the docker backend's per-Task netns owner and (Phase 3 onwards)
	// the remote-mode volume stager. Air-gap mirrors set this once.
	cmd.PersistentFlags().StringVar(&gf.pauseImage, "pause-image", "", "override the docker backend's pause/stager image (env: "+envPauseImage+"; default registry.k8s.io/pause:3.9)")
	// Per-invocation daemon-address override. Wins over $DOCKER_HOST
	// without mutating process env. Same scheme set as DOCKER_HOST:
	// unix:// | tcp:// | ssh://. Empty falls through to $DOCKER_HOST.
	cmd.PersistentFlags().StringVar(&gf.dockerHost, "docker-host", "", "override the docker daemon address for this invocation (overrides $DOCKER_HOST; same scheme set: unix:// | tcp:// | ssh://)")
	// State directory for per-run records (events.jsonl + meta.json).
	// Empty falls through to $TKN_ACT_STATE_DIR, then XDG_DATA_HOME.
	cmd.PersistentFlags().StringVar(&gf.stateDir, "state-dir", "", "override the directory where tkn-act stores run records (env: TKN_ACT_STATE_DIR; default $XDG_DATA_HOME/tkn-act or ~/.local/share/tkn-act)")

	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newListCmd())
	cmd.AddCommand(newValidateCmd())
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newClusterCmd())
	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newHelpJSONCmd())
	cmd.AddCommand(newAgentGuideCmd())
	cmd.AddCommand(newCacheCmd())
	cmd.AddCommand(newLogsCmd())
	cmd.AddCommand(newRunsCmd())
	return cmd
}
