package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dfbmorinigo/tkn-act/internal/backend/docker"
	"github.com/dfbmorinigo/tkn-act/internal/discovery"
	"github.com/dfbmorinigo/tkn-act/internal/engine"
	"github.com/dfbmorinigo/tkn-act/internal/loader"
	"github.com/dfbmorinigo/tkn-act/internal/reporter"
	"github.com/dfbmorinigo/tkn-act/internal/tektontypes"
	"github.com/dfbmorinigo/tkn-act/internal/validator"
	"github.com/dfbmorinigo/tkn-act/internal/workspace"
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
			return err
		}
		files = disc
	}

	b, err := loader.LoadFiles(files)
	if err != nil {
		return err
	}

	// Pick pipeline.
	pipe := rf.pipeline
	if pipe == "" {
		switch len(b.Pipelines) {
		case 0:
			return fmt.Errorf("no Pipeline found in loaded files")
		case 1:
			for n := range b.Pipelines {
				pipe = n
			}
		default:
			names := make([]string, 0, len(b.Pipelines))
			for n := range b.Pipelines {
				names = append(names, n)
			}
			return fmt.Errorf("multiple pipelines loaded (%v); specify -p", names)
		}
	}

	// Validate.
	if errs := validator.Validate(b, pipe, nil); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "error:", e)
		}
		return fmt.Errorf("%d validation error(s)", len(errs))
	}

	// Parse params.
	paramsMap := map[string]tektontypes.ParamValue{}
	for _, kv := range rf.params {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--param expects key=value, got %q", kv)
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
			return fmt.Errorf("--workspace expects name=hostpath, got %q", kv)
		}
		userPaths[k] = v
	}
	for _, w := range b.Pipelines[pipe].Spec.Workspaces {
		host, err := mgr.Provision(w.Name, userPaths[w.Name])
		if err != nil {
			return err
		}
		wsHost[w.Name] = host
	}

	// Wire the engine's results-dir provisioner to the manager.
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	// Build reporter.
	var rep reporter.Reporter
	switch gf.output {
	case "json":
		rep = reporter.NewJSON(os.Stdout)
	default:
		rep = reporter.NewPretty(os.Stdout, !gf.noColor && isTerminal(os.Stdout))
	}

	// Build backend.
	if gf.cluster {
		return fmt.Errorf("--cluster (k3d) backend is not yet implemented in v1; coming in v1.x")
	}
	be, err := docker.New(docker.Options{})
	if err != nil {
		return fmt.Errorf("docker backend: %w", err)
	}

	// Cancel on signals.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := engine.New(be, rep, engine.Options{MaxParallel: gf.maxParallel}).RunPipeline(ctx, engine.PipelineInput{
		Bundle:     b,
		Name:       pipe,
		Params:     paramsMap,
		Workspaces: wsHost,
	})
	if err != nil {
		return err
	}

	if !gf.cleanup {
		fmt.Fprintf(os.Stderr, "workspace tmpdirs preserved at: %s\n", filepath.Join(cacheRoot, "run"))
	}
	if res.Status != "succeeded" {
		os.Exit(1)
	}
	return nil
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
