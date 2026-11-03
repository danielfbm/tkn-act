package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// envTektonVersion is the environment variable that overrides the
// built-in Tekton install version when the user hasn't passed
// `--tekton-version`. The CI cluster-integration matrix sets this per
// matrix leg; on-prem operators can set it once in their shell.
const envTektonVersion = "TKN_ACT_TEKTON_VERSION"

// resolveTektonVersion picks the Tekton version to install on
// `tkn-act cluster up`. Precedence (highest first): the explicit flag,
// then $TKN_ACT_TEKTON_VERSION, then the in-binary default.
func resolveTektonVersion(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envTektonVersion); v != "" {
		return v
	}
	return tekton.DefaultTektonVersion
}

// clusterBackendOptions builds the cluster backend options used by
// `run --cluster`: the k3d driver rooted under cacheRoot, the
// ConfigMap/Secret stores, and the Tekton version resolved from
// $TKN_ACT_TEKTON_VERSION (or the in-binary default), so a fresh
// machine's first `run --cluster` installs the same release
// `cluster up` would.
func clusterBackendOptions(cacheRoot string, cm, sec *volumes.Store) clusterbe.Options {
	return clusterbe.Options{
		CacheDir: cacheRoot,
		Driver: k3d.New(k3d.Options{
			ClusterName:    "tkn-act",
			KubeconfigPath: filepath.Join(cacheRoot, "cluster", "kubeconfig"),
		}),
		TektonVersion: resolveTektonVersion(""),
		ConfigMaps:    cm,
		Secrets:       sec,
	}
}

// installedTektonVersion returns the version the cluster's
// tekton-pipelines/pipelines-info ConfigMap reports, falling back to
// the requested version (marked as such) when it can't be read.
func installedTektonVersion(ctx context.Context, kube kubernetes.Interface, requested string) string {
	if kube != nil {
		if v, err := tekton.ReadInstalledVersion(ctx, kube); err == nil {
			return v
		}
	}
	return requested + " (requested; installed version could not be read)"
}

// kubeClientFor builds a typed client from a kubeconfig path; nil on error.
func kubeClientFor(kubeconfig string) kubernetes.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	return c
}

type clusterStatus struct {
	Name       string `json:"name"`
	Exists     bool   `json:"exists"`
	Running    bool   `json:"running"`
	Detail     string `json:"detail,omitempty"`
	Kubeconfig string `json:"kubeconfig"`
}

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage the local k3d cluster used by --cluster",
		Args:  cobra.NoArgs,
		Long: `Manage the ephemeral k3d cluster used by 'tkn-act run --cluster'.

Requires k3d and kubectl on PATH; run 'tkn-act doctor' to verify.`,
		Example: `  tkn-act cluster up
  tkn-act cluster status -o json
  tkn-act cluster down -y`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newClusterUpCmd(), newClusterDownCmd(), newClusterStatusCmd())
	return cmd
}

func newClusterUpCmd() *cobra.Command {
	var tektonVersion string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create the local cluster and install Tekton (idempotent)",
		Example: `  # install the default Tekton LTS
  tkn-act cluster up

  # pin to a specific Tekton release (also reads $TKN_ACT_TEKTON_VERSION)
  tkn-act cluster up --tekton-version v1.9.9`,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := context.Background()
			drv := newDriver()
			version := resolveTektonVersion(tektonVersion)
			cb := clusterbe.New(clusterbe.Options{
				CacheDir:      cacheDir(),
				Driver:        drv,
				TektonVersion: version,
			})
			if err := cb.Prepare(ctx, backend.RunSpec{}); err != nil {
				return exitcode.Wrap(exitcode.Env, err)
			}
			fmt.Println("cluster ready:   ", drv.Name())
			fmt.Println("kubeconfig:      ", drv.Kubeconfig())
			fmt.Println("tekton version:  ", installedTektonVersion(ctx, kubeClientFor(drv.Kubeconfig()), version))
			return nil
		},
	}
	cmd.Flags().StringVar(&tektonVersion, "tekton-version", "",
		"Tekton Pipelines version to install (env: "+envTektonVersion+"; default: "+tekton.DefaultTektonVersion+")")
	return cmd
}

func newClusterDownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Delete the local cluster",
		Example: `  # interactive confirmation
  tkn-act cluster down

  # non-interactive (for AI agents and CI)
  tkn-act cluster down -y`,
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if !yes {
				fmt.Print("delete cluster ", drv.Name(), "? [y/N] ")
				r := bufio.NewReader(os.Stdin)
				ans, _ := r.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) != "y" {
					return exitcode.Wrap(exitcode.Usage, fmt.Errorf("cancelled"))
				}
			}
			if err := drv.Delete(context.Background()); err != nil {
				return exitcode.Wrap(exitcode.Env, err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show cluster + Tekton status",
		Example: `  tkn-act cluster status -o json`,
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			st, err := drv.Status(context.Background())
			if err != nil {
				return exitcode.Wrap(exitcode.Env, err)
			}
			out := clusterStatus{
				Name:       drv.Name(),
				Exists:     st.Exists,
				Running:    st.Running,
				Detail:     st.Detail,
				Kubeconfig: drv.Kubeconfig(),
			}
			if gf.output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Println("cluster:    ", out.Name)
			fmt.Println("exists:     ", out.Exists)
			fmt.Println("running:    ", out.Running)
			if out.Detail != "" {
				fmt.Println("detail:     ", out.Detail)
			}
			fmt.Println("kubeconfig: ", out.Kubeconfig)
			return nil
		},
	}
}

func newDriver() *k3d.Driver {
	kubecfg := filepath.Join(cacheDir(), "cluster", "kubeconfig")
	return k3d.New(k3d.Options{ClusterName: "tkn-act", KubeconfigPath: kubecfg})
}
