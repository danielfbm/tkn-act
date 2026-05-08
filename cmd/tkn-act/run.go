package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/discovery"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/validator"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
	"github.com/spf13/cobra"
)

type runFlags struct {
	files      []string
	dir        string
	pipeline   string
	params     []string
	workspaces []string
}

func newRunCmd() *cobra.Command {
	var rf runFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a pipeline on the local backend",
		Long: `Run a Tekton Pipeline on the local Docker (or k3d) backend.

If -f is not given, tkn-act discovers Tekton YAML in the current directory
(pipeline.yaml or .tekton/). If multiple Pipelines are loaded, -p is required.`,
		Example: `  # Auto-discover and run the only pipeline in the current dir
  tkn-act run

  # Run a specific file with a parameter and a workspace
  tkn-act run -f pipeline.yaml --param revision=main -w shared=./build

  # Emit machine-readable JSON events to stdout (for AI agents / scripts)
  tkn-act run -f pipeline.yaml -o json

  # Run on the ephemeral k3d cluster instead of plain Docker
  tkn-act run --cluster -f pipeline.yaml`,
		RunE: func(c *cobra.Command, args []string) error {
			return runWith(rf)
		},
	}
	cmd.Flags().StringSliceVarP(&rf.files, "file", "f", nil, "Tekton YAML file(s)")
	cmd.Flags().StringVarP(&rf.dir, "dir", "C", "", "directory to scan when -f is not given")
	cmd.Flags().StringVarP(&rf.pipeline, "pipeline", "p", "", "pipeline name (when multiple are loaded)")
	cmd.Flags().StringArrayVar(&rf.params, "param", nil, "param key=value (repeatable)")
	cmd.Flags().StringArrayVarP(&rf.workspaces, "workspace", "w", nil, "workspace name=hostpath (repeatable)")
	return cmd
}

// runDefault is invoked by the bare `tkn-act` form (no subcommand).
func runDefault(_ *cobra.Command, _ []string) error {
	return runWith(runFlags{})
}

func runWith(rf runFlags) error {
	files := rf.files
	dir := rf.dir
	if dir == "" {
		dir = "."
	}
	if len(files) == 0 {
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

	// Pick pipeline.
	pipe := rf.pipeline
	if pipe == "" {
		switch len(b.Pipelines) {
		case 0:
			return exitcode.Wrap(exitcode.Usage, fmt.Errorf("no Pipeline found in loaded files"))
		case 1:
			for n := range b.Pipelines {
				pipe = n
			}
		default:
			names := make([]string, 0, len(b.Pipelines))
			for n := range b.Pipelines {
				names = append(names, n)
			}
			return exitcode.Wrap(exitcode.Usage, fmt.Errorf("multiple pipelines loaded (%v); specify -p", names))
		}
	}

	// Validate (resolver-aware). The CacheCheck callback wires the
	// validator's --offline pre-flight to the on-disk resolver cache;
	// when --offline is set, every resolver-backed ref must already be
	// in the cache by content-hash, otherwise the run aborts before
	// any task starts (exit 4).
	resolverCacheRoot := resolveResolverCacheDir(gf.resolverCacheDir)
	diskCache := refresolver.NewDiskCache(resolverCacheRoot)
	vopts := validator.Options{
		Offline:               gf.offline,
		RegisteredResolvers:   gf.resolverAllow,
		RemoteResolverEnabled: gf.remoteResolverContext != "",
		CacheCheck: func(ref validator.UnresolvedRef) bool {
			return diskCache.Has(refresolver.Request{
				Resolver: ref.Resolver,
				Params:   ref.Params,
			})
		},
	}
	if errs := validator.ValidateWithOptions(b, pipe, nil, vopts); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "error:", e)
		}
		return exitcode.Wrap(exitcode.Validate, fmt.Errorf("%d validation error(s)", len(errs)))
	}

	// Parse params.
	paramsMap := map[string]tektontypes.ParamValue{}
	for _, kv := range rf.params {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return exitcode.Wrap(exitcode.Usage, fmt.Errorf("--param expects key=value, got %q", kv))
		}
		paramsMap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	// Workspaces: parse user paths and provision tmpdirs for the rest.
	cacheRoot := cacheDir()
	mgr := workspace.NewManager(cacheRoot, "run")
	defer func() {
		if gf.cleanup {
			_ = mgr.Cleanup()
		}
	}()

	wsHost := map[string]string{}
	userPaths := map[string]string{}
	for _, kv := range rf.workspaces {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return exitcode.Wrap(exitcode.Usage, fmt.Errorf("--workspace expects name=hostpath, got %q", kv))
		}
		userPaths[k] = v
	}
	for _, w := range b.Pipelines[pipe].Spec.Workspaces {
		host, err := mgr.Provision(w.Name, userPaths[w.Name])
		if err != nil {
			return exitcode.Wrap(exitcode.Env, err)
		}
		wsHost[w.Name] = host
	}

	// Wire the engine's results-dir provisioner to the manager.
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	// ConfigMap / Secret stores for volumes. Layered precedence:
	// inline flags > --*-dir > kind: ConfigMap/Secret loaded from -f.
	cmStore, secStore, err := buildVolumeStores(cacheRoot, b)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	volResolver := func(taskName string, vs []tektontypes.Volume) (map[string]string, error) {
		volBase, perr := mgr.ProvisionVolumesDir(taskName)
		if perr != nil {
			return nil, perr
		}
		return volumes.MaterializeForTask(taskName, vs, volBase, cmStore, secStore)
	}

	// Build reporter.
	rep, err := buildReporter(os.Stdout)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}

	// Build backend.
	var be backend.Backend
	if gf.cluster {
		be = clusterbe.New(clusterbe.Options{
			CacheDir: cacheRoot,
			Driver: k3d.New(k3d.Options{
				ClusterName:    "tkn-act",
				KubeconfigPath: filepath.Join(cacheRoot, "cluster", "kubeconfig"),
			}),
			ConfigMaps: cmStore,
			Secrets:    secStore,
		})
	} else {
		dockerBe, err := docker.New(docker.Options{
			SidecarStartGrace: gf.sidecarStartGrace,
			SidecarStopGrace:  gf.sidecarStopGrace,
		})
		if err != nil {
			return exitcode.Wrap(exitcode.Env, fmt.Errorf("docker backend: %w", err))
		}
		be = dockerBe
	}

	// Cancel on signals.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the refresolver registry. The default registry pre-registers
	// inline + git + hub + http + bundles; cluster is OFF by default and
	// only registers when --cluster-resolver-context is set OR `cluster`
	// is in --resolver-allow (KUBECONFIG may point at production).
	allowCluster := gf.clusterResolverContext != ""
	reg := refresolver.NewDefaultRegistry(refresolver.Options{
		Allow:                     gf.resolverAllow,
		CacheDir:                  resolverCacheRoot,
		Offline:                   gf.offline,
		AllowInsecureHTTP:         gf.resolverAllowInsecureHTTP,
		AllowCluster:              allowCluster,
		ClusterResolverContext:    gf.clusterResolverContext,
		ClusterResolverKubeconfig: gf.clusterResolverKubeconfig,
	})
	// Reuse the same DiskCache instance for run-time hits as the one
	// the validator used at load time (ensures Has → Get consistency).
	reg.SetCache(diskCache)

	// Mode B: when --remote-resolver-context is set, every resolver
	// dispatch goes through the remote driver instead of the direct
	// allow-list. SECURITY: no credentials cross the tkn-act boundary
	// — the user's kubeconfig identity is what the remote cluster
	// sees. Construction failure is exit code 3 (environment).
	remote, err := buildRemoteResolver()
	if err != nil {
		return exitcode.Wrap(exitcode.Env, fmt.Errorf("remote resolver: %w", err))
	}
	if remote != nil {
		reg.SetRemote(remote)
	}

	res, err := engine.New(be, rep, engine.Options{
		MaxParallel:    gf.maxParallel,
		VolumeResolver: volResolver,
		Refresolver:    reg,
	}).RunPipeline(ctx, engine.PipelineInput{
		Bundle:     b,
		Name:       pipe,
		Params:     paramsMap,
		Workspaces: wsHost,
	})
	if err != nil {
		if ctx.Err() != nil {
			return exitcode.Wrap(exitcode.Cancelled, err)
		}
		return exitcode.Wrap(exitcode.Pipeline, err)
	}

	if !gf.cleanup {
		fmt.Fprintf(os.Stderr, "workspace tmpdirs preserved at: %s\n", filepath.Join(cacheRoot, "run"))
	}
	switch res.Status {
	case "succeeded":
		return nil
	case "timeout":
		return exitcode.Wrap(exitcode.Timeout, fmt.Errorf("pipeline %q %s", pipe, res.Status))
	default:
		return exitcode.Wrap(exitcode.Pipeline, fmt.Errorf("pipeline %q %s", pipe, res.Status))
	}
}

// buildRemoteResolver returns a configured Mode B resolver iff
// --remote-resolver-context is non-empty; otherwise (nil, nil) so the
// registry stays in direct mode. Failures (kubeconfig load, dynamic
// client construction) translate to exit code 3 in the caller.
func buildRemoteResolver() (*refresolver.RemoteResolver, error) {
	if gf.remoteResolverContext == "" {
		return nil, nil
	}
	return refresolver.NewRemoteResolver(refresolver.RemoteResolverOptions{
		Context:   gf.remoteResolverContext,
		Namespace: gf.remoteResolverNamespace,
		Timeout:   gf.remoteResolverTimeout,
	})
}

// resolveResolverCacheDir returns the on-disk cache dir for resolved
// bytes. An explicit --resolver-cache-dir wins; otherwise we default
// to $XDG_CACHE_HOME/tkn-act/resolved (or $HOME/.cache/tkn-act/resolved
// when XDG_CACHE_HOME isn't set). Phase 1 doesn't yet read or write
// from this dir; Phase 6 wires it.
func resolveResolverCacheDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(cacheDir(), "resolved")
}

func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "tkn-act")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".cache", "tkn-act")
	}
	return os.TempDir()
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

// buildReporter constructs the reporter for the current global flags. Output
// "json" always returns a JSON reporter (no color, no verbosity — its shape is
// the agent contract). Otherwise we resolve color and verbosity from the
// flags and the environment.
func buildReporter(out *os.File) (reporter.Reporter, error) {
	if gf.output == "json" {
		return reporter.NewJSON(out), nil
	}
	if gf.quiet && gf.verbose {
		return nil, fmt.Errorf("--quiet and --verbose are mutually exclusive")
	}
	mode, err := reporter.ParseColorMode(gf.color)
	if err != nil {
		return nil, err
	}
	if gf.noColor {
		// --no-color is a hard override (for backwards compatibility).
		mode = reporter.ColorNever
	}
	color := reporter.ResolveColor(mode, isTerminal(out), os.LookupEnv)

	verb := reporter.Normal
	switch {
	case gf.quiet:
		verb = reporter.Quiet
	case gf.verbose:
		verb = reporter.Verbose
	}
	return reporter.NewPretty(out, reporter.PrettyOptions{Color: color, Verbosity: verb}), nil
}

// buildVolumeStores assembles the configMap / secret stores from the
// global flags AND from any kind: ConfigMap / kind: Secret resources
// loaded from the -f YAML stream (b may be nil).
//
// Layered precedence inside each Store (highest first):
//  1. inline --configmap / --secret entries
//  2. on-disk --configmap-dir / --secret-dir layout
//  3. -f-loaded `kind: ConfigMap` / `kind: Secret` resources
//
// See internal/volumes/store.go for the layer mechanics.
func buildVolumeStores(cacheRoot string, b *loader.Bundle) (cm *volumes.Store, sec *volumes.Store, err error) {
	cmDir := gf.configMapDir
	if cmDir == "" {
		cmDir = filepath.Join(cacheRoot, "configmaps")
	}
	secDir := gf.secretDir
	if secDir == "" {
		secDir = filepath.Join(cacheRoot, "secrets")
	}
	cm = volumes.NewStore(cmDir)
	sec = volumes.NewStore(secDir)

	// 3. Bundle-loaded resources first (lowest layer; they get shadowed
	// by anything in dir or inline that names the same key).
	if b != nil {
		for name, bytesByKey := range b.ConfigMaps {
			cm.LoadBytes(name, bytesByKey)
		}
		for name, bytesByKey := range b.Secrets {
			sec.LoadBytes(name, bytesByKey)
		}
	}
	// Order of Add vs LoadBytes calls here doesn't matter; Store.Resolve
	// composes layers so Inline overwrites Dir overwrites Bundle.
	if err := parseInlineFlags(cm, gf.configMaps, "configmap"); err != nil {
		return nil, nil, err
	}
	if err := parseInlineFlags(sec, gf.secrets, "secret"); err != nil {
		return nil, nil, err
	}
	return cm, sec, nil
}

// parseInlineFlags accepts entries shaped <name>=<k1>=<v1>[,<k2>=<v2>...] and
// records them on the store. Whitespace around the inner key/value pairs is
// trimmed; values are taken verbatim.
func parseInlineFlags(s *volumes.Store, entries []string, kind string) error {
	for _, entry := range entries {
		name, kvs, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return fmt.Errorf("--%s expects <name>=<k>=<v>[,...], got %q", kind, entry)
		}
		for _, kv := range strings.Split(kvs, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("--%s %q: each entry must be key=value, got %q", kind, name, kv)
			}
			s.Add(name, strings.TrimSpace(k), v)
		}
	}
	return nil
}
