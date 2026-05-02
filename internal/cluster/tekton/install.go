// Package tekton installs the Tekton Pipelines controller into a Kubernetes
// cluster. Idempotent: skips apply if pipelines.tekton.dev CRD already exists.
package tekton

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Options struct {
	Kubeconfig string
	Runner     cmdrunner.Runner
	Apiext     apiextclient.Interface
	Kube       kubernetes.Interface
	Version    string // e.g. "v0.65.0"
	Timeout    time.Duration
}

type Installer struct {
	opt Options
}

func New(opt Options) *Installer {
	if opt.Version == "" {
		opt.Version = "v0.65.0"
	}
	if opt.Timeout == 0 {
		opt.Timeout = 180 * time.Second
	}
	if opt.Runner == nil {
		opt.Runner = cmdrunner.New()
	}
	return &Installer{opt: opt}
}

func (i *Installer) Install(ctx context.Context) error {
	if i.opt.Apiext != nil {
		_, err := i.opt.Apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "pipelines.tekton.dev", metav1.GetOptions{})
		if err == nil {
			return i.waitReady(ctx)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check tekton CRD: %w", err)
		}
	}
	url := fmt.Sprintf("https://storage.googleapis.com/tekton-releases/pipeline/previous/%s/release.yaml", i.opt.Version)
	if _, err := i.opt.Runner.Output(ctx, "kubectl", "--kubeconfig", i.opt.Kubeconfig, "apply", "-f", url); err != nil {
		return fmt.Errorf("apply tekton release: %w", err)
	}
	return i.waitReady(ctx)
}

func (i *Installer) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(i.opt.Timeout)
	deps := []string{"tekton-pipelines-controller", "tekton-pipelines-webhook"}
	for _, name := range deps {
		for {
			d, err := i.opt.Kube.AppsV1().Deployments("tekton-pipelines").Get(ctx, name, metav1.GetOptions{})
			if err == nil && d.Status.Replicas > 0 && d.Status.ReadyReplicas == d.Status.Replicas {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for deployment %q in tekton-pipelines", name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}
