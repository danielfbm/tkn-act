// Package cluster defines the local-Kubernetes driver abstraction. v1.1 ships
// with the k3d driver; future drivers (kind) implement the same interface.
package cluster

import "context"

// Driver manages the lifecycle of a local cluster.
type Driver interface {
	// Name returns a stable cluster name used by Ensure/Delete.
	Name() string

	// Ensure creates the cluster if missing. No-op if already present.
	// Writes kubeconfig to the path returned by Kubeconfig() once Ensure returns nil.
	Ensure(ctx context.Context) error

	// Status reports whether the cluster currently exists and is running.
	Status(ctx context.Context) (Status, error)

	// Delete removes the cluster. No-op if absent.
	Delete(ctx context.Context) error

	// Kubeconfig returns the path the driver writes the cluster's kubeconfig to.
	Kubeconfig() string
}

type Status struct {
	Exists  bool
	Running bool
	Detail  string
}
