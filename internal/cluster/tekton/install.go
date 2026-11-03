// Package tekton installs the Tekton Pipelines controller into a Kubernetes
// cluster. Idempotent: skips the `kubectl apply` of release.yaml when the
// pipelines.tekton.dev CRD, the controller + webhook deployments, and a
// `tekton-pipelines/pipelines-info` ConfigMap reporting the requested
// version are all present. Any mismatch (different installed version,
// interrupted install) re-applies the requested release.
package tekton

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DefaultTektonVersion is the Tekton Pipelines release that
// `tkn-act cluster up` installs when neither `--tekton-version` nor
// `TKN_ACT_TEKTON_VERSION` is set. Bump in lock-step with the
// cluster-integration CI matrix in
// `.github/workflows/cluster-integration.yml`. See
// `docs/superpowers/specs/2026-05-13-tekton-bump-and-matrix-design.md`
// for the LTS-pinning policy.
const DefaultTektonVersion = "v1.15.5"

const (
	tektonNamespace   = "tekton-pipelines"
	pipelinesInfoName = "pipelines-info"
)

// tektonDeployments are the Deployments every Tekton Pipelines release
// ships in tektonNamespace (names unchanged from v1.9 through v1.15).
var tektonDeployments = []string{"tekton-pipelines-controller", "tekton-pipelines-webhook"}

type Options struct {
	Kubeconfig string
	Runner     cmdrunner.Runner
	Apiext     apiextclient.Interface
	Kube       kubernetes.Interface
	Version    string // e.g. "v1.15.5"; empty defaults to DefaultTektonVersion
	Timeout    time.Duration
}

type Installer struct {
	opt Options
	// installed is the version reported by pipelines-info after the
	// last successful Install (empty when it could not be read).
	installed string
}

// ReadInstalledVersion returns `data.version` of the
// `tekton-pipelines/pipelines-info` ConfigMap that every Tekton
// Pipelines release ships (config/config-info.yaml).
func ReadInstalledVersion(ctx context.Context, kube kubernetes.Interface) (string, error) {
	cm, err := kube.CoreV1().ConfigMaps(tektonNamespace).Get(ctx, pipelinesInfoName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s/%s: %w", tektonNamespace, pipelinesInfoName, err)
	}
	v := strings.TrimSpace(cm.Data["version"])
	if v == "" {
		return "", fmt.Errorf("%s/%s has no data.version", tektonNamespace, pipelinesInfoName)
	}
	return v, nil
}

// InstalledVersion is the Tekton version the cluster reported after the
// last successful Install. Empty if it could not be determined.
func (i *Installer) InstalledVersion() string { return i.installed }

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

func New(opt Options) *Installer {
	if opt.Version == "" {
		opt.Version = DefaultTektonVersion
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
	need, err := i.needsApply(ctx)
	if err != nil {
		return err
	}
	if need {
		// GitHub Releases carries both v0.x and v1.x release.yaml assets.
		// The legacy `storage.googleapis.com/tekton-releases/pipeline/previous/`
		// bucket does not populate v1.x, so we use GitHub uniformly.
		url := fmt.Sprintf("https://github.com/tektoncd/pipeline/releases/download/%s/release.yaml", i.opt.Version)
		if _, err := i.opt.Runner.Output(ctx, "kubectl", "--kubeconfig", i.opt.Kubeconfig, "apply", "-f", url); err != nil {
			return fmt.Errorf("apply tekton release: %w", err)
		}
	}
	if err := i.waitReady(ctx); err != nil {
		return err
	}
	if i.opt.Kube != nil {
		if v, err := ReadInstalledVersion(ctx, i.opt.Kube); err == nil {
			i.installed = v
		}
	}
	return i.enableFeatureFlags(ctx)
}

// needsApply reports whether release.yaml must be (re-)applied: the
// CRD is missing (fresh cluster), the controller or webhook deployment
// is missing (interrupted install), or pipelines-info is missing or
// reports a version other than the requested one (upgrade/downgrade).
// With no kube clients injected it conservatively applies.
func (i *Installer) needsApply(ctx context.Context) (bool, error) {
	if i.opt.Apiext == nil {
		return true, nil
	}
	_, err := i.opt.Apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "pipelines.tekton.dev", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check tekton CRD: %w", err)
	}
	if i.opt.Kube == nil {
		return false, nil
	}
	for _, name := range tektonDeployments {
		_, err := i.opt.Kube.AppsV1().Deployments(tektonNamespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("check deployment %s/%s: %w", tektonNamespace, name, err)
		}
	}
	cm, err := i.opt.Kube.CoreV1().ConfigMaps(tektonNamespace).Get(ctx, pipelinesInfoName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get %s/%s: %w", tektonNamespace, pipelinesInfoName, err)
	}
	return !sameVersion(cm.Data["version"], i.opt.Version), nil
}

// enableFeatureFlags turns on Tekton features that tkn-act fixtures
// rely on but upstream ships disabled (or gated) by default. Currently:
//   - enable-api-fields=alpha: pipeline-result `[*]` aggregation over a
//     matrix-fanned task.
//   - enable-step-actions=true: kept for older installs. StepActions
//     (and Step.results) went GA in Tekton v1.0 and the flag has been
//     a no-op since at least v1.9 (the oldest supported LTS); setting
//     it is harmless.
//
// The feature-flags ConfigMap is created by the Tekton release.yaml
// (and reset to upstream defaults by every re-apply); we Update it in
// place. If it's missing (e.g. fresh kube without the release
// reconciled yet), we Create it.
func (i *Installer) enableFeatureFlags(ctx context.Context) error {
	if i.opt.Kube == nil {
		return nil
	}
	const (
		ns     = "tekton-pipelines"
		cmName = "feature-flags"
	)
	// Feature flags tkn-act enables on the local Tekton install:
	//   enable-step-actions  — Track 1 #8 (StepAction step references).
	//                          No-op since v1.9 (StepActions are GA);
	//                          kept so pinning an older release still works.
	//   enable-api-fields    — `alpha` widens result-reference syntax,
	//                          including the matrix-result `[*]` pipeline-
	//                          level aggregation (Track 1 #3). Tekton ships
	//                          matrix as GA but pipeline-result `[*]` over
	//                          a matrix-fanned task gated on `alpha` through
	//                          at least v1.15. Future LTS versions may
	//                          graduate this; the flag is harmless when set.
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
	if i.opt.Kube == nil {
		return nil
	}
	deadline := time.Now().Add(i.opt.Timeout)
	for _, name := range tektonDeployments {
		for {
			d, err := i.opt.Kube.AppsV1().Deployments(tektonNamespace).Get(ctx, name, metav1.GetOptions{})
			if err == nil && deploymentRolledOut(d) {
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

// deploymentRolledOut mirrors `kubectl rollout status`: the controller
// has observed the latest spec generation and every replica is updated
// and ready. Checking generation matters after a re-apply — the old
// ReplicaSet's pods are still Ready until the new rollout replaces them.
func deploymentRolledOut(d *appsv1.Deployment) bool {
	s := d.Status
	return s.ObservedGeneration >= d.Generation &&
		s.Replicas > 0 &&
		s.ReadyReplicas == s.Replicas &&
		s.UpdatedReplicas == s.Replicas
}
