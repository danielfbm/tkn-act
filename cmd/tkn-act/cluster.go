package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/spf13/cobra"
)

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
		Long: `Manage the ephemeral k3d cluster used by 'tkn-act run --cluster'.

Requires k3d and kubectl on PATH; run 'tkn-act doctor' to verify.`,
		Example: `  tkn-act cluster up
  tkn-act cluster status -o json
  tkn-act cluster down -y`,
	}
	cmd.AddCommand(newClusterUpCmd(), newClusterDownCmd(), newClusterStatusCmd())
	return cmd
}

func newClusterUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "up",
		Short:   "Create the local cluster and install Tekton (idempotent)",
		Example: `  tkn-act cluster up`,
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if err := drv.Ensure(context.Background()); err != nil {
				return exitcode.Wrap(exitcode.Env, err)
			}
			fmt.Println("cluster ready:", drv.Name())
			fmt.Println("kubeconfig:", drv.Kubeconfig())
			return nil
		},
	}
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
