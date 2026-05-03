// Package cluster implements backend.Backend by submitting PipelineRuns to a
// real Tekton install on a local Kubernetes cluster (k3d).
package cluster

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	"github.com/danielfbm/tkn-act/internal/volumes"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Options struct {
	CacheDir      string // base path for kubeconfig and other state
	Driver        cluster.Driver
	Runner        cmdrunner.Runner
	TektonVersion string
	// ConfigMaps and Secrets back the per-Task volumes the docker side
	// resolves locally. The cluster backend reads bytes from these stores
	// and projects them into ephemeral kube ConfigMap/Secret resources in
	// the run namespace before submitting the PipelineRun. Nil stores mean
	// "no volume sources configured" and a fixture that references one
	// will fail at submit time, mirroring docker behavior.
	ConfigMaps *volumes.Store
	Secrets    *volumes.Store
}

type ClientBundle struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	Apiext  apiextclient.Interface
}

type Backend struct {
	opt    Options
	client ClientBundle
}

// New is the production constructor: it does not connect — Prepare lazily
// ensures the cluster, installs Tekton, and builds the kube clients.
func New(opt Options) *Backend {
	if opt.CacheDir == "" {
		opt.CacheDir = ".cache/tkn-act"
	}
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	if opt.TektonVersion == "" {
		opt.TektonVersion = "v0.65.0"
	}
	return &Backend{opt: opt}
}

// NewWithClients is a test constructor that injects a pre-built ClientBundle.
func NewWithClients(cb ClientBundle) *Backend { return &Backend{client: cb} }

// NewWithClientsAndStores is the same, plus the configMap/secret stores
// the volumes-apply path needs. Production code uses New + Options.
func NewWithClientsAndStores(cb ClientBundle, cm, sec *volumes.Store) *Backend {
	return &Backend{client: cb, opt: Options{ConfigMaps: cm, Secrets: sec}}
}

// ApplyVolumeSourcesForTest re-exposes the package-private apply path so
// the volumes_test can inspect the resulting kube ConfigMap/Secret without
// driving the full RunPipeline watch loop.
func (b *Backend) ApplyVolumeSourcesForTest(ctx context.Context, in backend.PipelineRunInvocation, ns string) error {
	return b.applyVolumeSources(ctx, in, ns)
}

// CollectTaskOutcomesForTest re-exposes collectTaskOutcomes so the
// retries_test can drive the per-TaskRun walk against pre-seeded fake
// objects without going through Create+Watch.
func (b *Backend) CollectTaskOutcomesForTest(ctx context.Context, in backend.PipelineRunInvocation, ns string) map[string]backend.TaskOutcomeOnCluster {
	return b.collectTaskOutcomes(ctx, in, ns)
}

// Prepare lazily provisions the cluster + Tekton on first use.
func (b *Backend) Prepare(ctx context.Context, _ backend.RunSpec) error {
	if b.client.Dynamic != nil {
		return nil // injected (test path)
	}
	if b.opt.Driver == nil {
		return fmt.Errorf("cluster.Backend: no Driver configured")
	}
	if err := b.opt.Driver.Ensure(ctx); err != nil {
		return fmt.Errorf("cluster ensure: %w", err)
	}
	kubecfgPath := b.opt.Driver.Kubeconfig()
	if kubecfgPath == "" {
		kubecfgPath = filepath.Join(b.opt.CacheDir, "cluster", "kubeconfig")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubecfgPath)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	b.client.Dynamic, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	b.client.Kube, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	b.client.Apiext, err = apiextclient.NewForConfig(cfg)
	if err != nil {
		return err
	}

	inst := tekton.New(tekton.Options{
		Kubeconfig: kubecfgPath,
		Runner:     b.opt.Runner,
		Apiext:     b.client.Apiext,
		Kube:       b.client.Kube,
		Version:    b.opt.TektonVersion,
		Timeout:    3 * time.Minute,
	})
	if err := inst.Install(ctx); err != nil {
		return fmt.Errorf("tekton install: %w", err)
	}
	return nil
}

// Cleanup is a no-op: cluster + namespaces persist for inspection.
func (b *Backend) Cleanup(_ context.Context) error { return nil }

// RunTask delegates to RunPipeline by wrapping the single TaskRun call into a
// trivial one-task pipeline. The engine should prefer RunPipeline directly.
func (b *Backend) RunTask(ctx context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	return backend.TaskResult{}, fmt.Errorf("cluster backend: per-task RunTask not supported; call RunPipeline")
}

// BuildPipelineRunObject constructs the PipelineRun unstructured (exposed for
// unit-test inspection).
func (b *Backend) BuildPipelineRunObject(in backend.PipelineRunInvocation, namespace string) (any, error) {
	return buildPipelineRun(in, namespace), nil
}
