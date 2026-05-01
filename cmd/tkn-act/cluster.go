package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/spf13/cobra"
)

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage the local k3d cluster used by --cluster",
	}
	cmd.AddCommand(newClusterUpCmd(), newClusterDownCmd(), newClusterStatusCmd())
	return cmd
}

func newClusterUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create the local cluster and install Tekton (idempotent)",
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if err := drv.Ensure(context.Background()); err != nil {
				return err
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
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			if !yes {
				fmt.Print("delete cluster ", drv.Name(), "? [y/N] ")
				r := bufio.NewReader(os.Stdin)
				ans, _ := r.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(ans)) != "y" {
					return fmt.Errorf("cancelled")
				}
			}
			return drv.Delete(context.Background())
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cluster + Tekton status",
		RunE: func(_ *cobra.Command, _ []string) error {
			drv := newDriver()
			st, err := drv.Status(context.Background())
			if err != nil {
				return err
			}
			fmt.Println("cluster:    ", drv.Name())
			fmt.Println("exists:     ", st.Exists)
			fmt.Println("running:    ", st.Running)
			if st.Detail != "" {
				fmt.Println("detail:     ", st.Detail)
			}
			fmt.Println("kubeconfig: ", drv.Kubeconfig())
			return nil
		},
	}
}

func newDriver() *k3d.Driver {
	kubecfg := filepath.Join(cacheDir(), "cluster", "kubeconfig")
	return k3d.New(k3d.Options{ClusterName: "tkn-act", KubeconfigPath: kubecfg})
}
