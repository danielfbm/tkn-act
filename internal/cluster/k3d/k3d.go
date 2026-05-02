// Package k3d implements cluster.Driver by shelling out to the k3d binary.
package k3d

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danielfbm/tkn-act/internal/cluster"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

type Options struct {
	ClusterName    string
	KubeconfigPath string
	Runner         cmdrunner.Runner
}

type Driver struct {
	name    string
	kubecfg string
	runner  cmdrunner.Runner
}

func New(opt Options) *Driver {
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	if opt.ClusterName == "" {
		opt.ClusterName = "tkn-act"
	}
	return &Driver{name: opt.ClusterName, kubecfg: opt.KubeconfigPath, runner: opt.Runner}
}

func (d *Driver) Name() string       { return d.name }
func (d *Driver) Kubeconfig() string { return d.kubecfg }

type clusterListEntry struct {
	Name           string `json:"name"`
	ServersRunning int    `json:"serversRunning"`
	AgentsRunning  int    `json:"agentsRunning"`
	Servers        int    `json:"servers"`
	Agents         int    `json:"agents"`
}

func (d *Driver) listClusters(ctx context.Context) ([]clusterListEntry, error) {
	out, err := d.runner.Output(ctx, "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("k3d cluster list: %w", err)
	}
	var entries []clusterListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse k3d cluster list: %w", err)
	}
	return entries, nil
}

func (d *Driver) Ensure(ctx context.Context) error {
	entries, err := d.listClusters(ctx)
	if err != nil {
		return err
	}
	exists := false
	for _, e := range entries {
		if e.Name == d.name {
			exists = true
			break
		}
	}
	if !exists {
		_, err := d.runner.Output(ctx, "k3d", "cluster", "create", d.name,
			"--no-lb", "--wait", "--timeout", "120s",
			"--k3s-arg", "--disable=traefik@server:0",
		)
		if err != nil {
			return fmt.Errorf("k3d cluster create: %w", err)
		}
	}
	if d.kubecfg != "" {
		out, err := d.runner.Output(ctx, "k3d", "kubeconfig", "get", d.name)
		if err != nil {
			return fmt.Errorf("k3d kubeconfig get: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(d.kubecfg), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(d.kubecfg, out, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) Status(ctx context.Context) (cluster.Status, error) {
	entries, err := d.listClusters(ctx)
	if err != nil {
		return cluster.Status{}, err
	}
	for _, e := range entries {
		if e.Name == d.name {
			running := e.ServersRunning >= 1
			detail := fmt.Sprintf("servers=%d/%d agents=%d/%d", e.ServersRunning, e.Servers, e.AgentsRunning, e.Agents)
			return cluster.Status{Exists: true, Running: running, Detail: detail}, nil
		}
	}
	return cluster.Status{Exists: false}, nil
}

func (d *Driver) Delete(ctx context.Context) error {
	st, err := d.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Exists {
		return nil
	}
	if _, err := d.runner.Output(ctx, "k3d", "cluster", "delete", d.name); err != nil {
		return fmt.Errorf("k3d cluster delete: %w", err)
	}
	if d.kubecfg != "" {
		_ = os.Remove(d.kubecfg)
	}
	return nil
}
