// Package tekton installs the Tekton Pipelines controller into a Kubernetes
// cluster. Idempotent: skips apply if pipelines.tekton.dev CRD already exists.
package tekton

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	corev1 "k8s.io/api/core/v1"
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
			if err := i.waitReady(ctx); err != nil {
				return err
			}
			return i.enableFeatureFlags(ctx)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("check tekton CRD: %w", err)
		}
	}
	url := fmt.Sprintf("https://storage.googleapis.com/tekton-releases/pipeline/previous/%s/release.yaml", i.opt.Version)
	if _, err := i.opt.Runner.Output(ctx, "kubectl", "--kubeconfig", i.opt.Kubeconfig, "apply", "-f", url); err != nil {
		return fmt.Errorf("apply tekton release: %w", err)
	}
	if err := i.waitReady(ctx); err != nil {
		return err
	}
	return i.enableFeatureFlags(ctx)
}

// enableFeatureFlags turns on Tekton features that tkn-act fixtures
// rely on but upstream ships disabled by default. Currently:
//   - enable-step-actions: required for `Step.results` (per-step
//     results, used by the v1.2 step-results e2e fixture).
//
// The feature-flags ConfigMap is created by the Tekton release.yaml;
// we Update it in place. If it's missing (e.g. fresh kube without the
// release reconciled yet), we Create it.
func (i *Installer) enableFeatureFlags(ctx context.Context) error {
	if i.opt.Kube == nil {
		return nil
	}
	const (
		ns     = "tekton-pipelines"
		cmName = "feature-flags"
	)
	// Feature flags tkn-act enables on the local Tekton install:
	//   enable-step-actions  — Track 1 #8 (StepAction step references)
	//   enable-api-fields    — `alpha` widens result-reference syntax,
	//                          including the matrix-result `[*]` pipeline-
	//                          level aggregation (Track 1 #3). v0.65 ships
	//                          matrix as GA but pipeline-result `[*]` over
	//                          a matrix-fanned task still gates on `alpha`.
	flags := map[string]string{
		"enable-step-actions": "true",
		"enable-api-fields":   "alpha",
	}
	cm, err := i.opt.Kube.CoreV1().ConfigMaps(ns).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = i.opt.Kube.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns},
			Data:       flags,
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create %s/%s: %w", ns, cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", ns, cmName, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	dirty := false
	for k, v := range flags {
		if cm.Data[k] != v {
			cm.Data[k] = v
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	if _, err := i.opt.Kube.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s/%s: %w", ns, cmName, err)
	}
	return nil
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
