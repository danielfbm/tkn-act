package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// applyVolumeSources walks every Task referenced by the PipelineRun and
// projects each configMap/secret volume's bytes (read from the backend's
// volumes.Store) into an ephemeral kube ConfigMap/Secret in the run
// namespace. The Tekton controller then resolves the volume by name against
// the resources we just applied.
//
// Behavior mirrors the docker side: if a volume references a source the
// store can't resolve, that bubbles up here as a hard error before submit
// (the docker side fails the same way at MaterializeForTask time).
//
// Idempotent: namespaces are created once per RunID; applying the same
// resource twice in the same namespace is a "create or update" pattern.
func (b *Backend) applyVolumeSources(ctx context.Context, in backend.PipelineRunInvocation, ns string) error {
	cmNames, secNames := volumeSourceNames(in)
	for name := range cmNames {
		bytesByKey, err := b.resolveConfigMap(name)
		if err != nil {
			return err
		}
		if err := b.upsertConfigMap(ctx, ns, name, bytesByKey); err != nil {
			return err
		}
	}
	for name := range secNames {
		bytesByKey, err := b.resolveSecret(name)
		if err != nil {
			return err
		}
		if err := b.upsertSecret(ctx, ns, name, bytesByKey); err != nil {
			return err
		}
	}
	return nil
}

// volumeSourceNames collects the set of unique configMap and secret source
// names referenced by every Task in the PipelineRun (including finally).
// The resolved Task spec lives on in.Tasks; PipelineTask.taskSpec inline
// case is also handled.
func volumeSourceNames(in backend.PipelineRunInvocation) (cm, sec map[string]struct{}) {
	cm = map[string]struct{}{}
	sec = map[string]struct{}{}
	all := append([]tektontypes.PipelineTask{}, in.Pipeline.Spec.Tasks...)
	all = append(all, in.Pipeline.Spec.Finally...)
	for _, pt := range all {
		spec := resolveTaskSpec(in, pt)
		for _, v := range spec.Volumes {
			switch {
			case v.ConfigMap != nil && v.ConfigMap.Name != "":
				cm[v.ConfigMap.Name] = struct{}{}
			case v.Secret != nil && v.Secret.SecretName != "":
				sec[v.Secret.SecretName] = struct{}{}
			}
		}
	}
	return cm, sec
}

func resolveTaskSpec(in backend.PipelineRunInvocation, pt tektontypes.PipelineTask) tektontypes.TaskSpec {
	if pt.TaskSpec != nil {
		return *pt.TaskSpec
	}
	if pt.TaskRef != nil {
		if t, ok := in.Tasks[pt.TaskRef.Name]; ok {
			return t.Spec
		}
	}
	return tektontypes.TaskSpec{}
}

func (b *Backend) resolveConfigMap(name string) (map[string][]byte, error) {
	if b.opt.ConfigMaps == nil {
		return nil, fmt.Errorf("volume references configMap %q but no ConfigMap store is configured (pass --configmap or --configmap-dir)", name)
	}
	return b.opt.ConfigMaps.Resolve(name)
}

func (b *Backend) resolveSecret(name string) (map[string][]byte, error) {
	if b.opt.Secrets == nil {
		return nil, fmt.Errorf("volume references secret %q but no Secret store is configured (pass --secret or --secret-dir)", name)
	}
	return b.opt.Secrets.Resolve(name)
}

func (b *Backend) upsertConfigMap(ctx context.Context, ns, name string, data map[string][]byte) error {
	stringData := make(map[string]string, len(data))
	for k, v := range data {
		stringData[k] = string(v)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tkn-act"},
		},
		Data: stringData,
	}
	_, err := b.client.Kube.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create ConfigMap %s/%s: %w", ns, name, err)
	}
	_, err = b.client.Kube.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update ConfigMap %s/%s: %w", ns, name, err)
	}
	return nil
}

func (b *Backend) upsertSecret(ctx context.Context, ns, name string, data map[string][]byte) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tkn-act"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	_, err := b.client.Kube.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create Secret %s/%s: %w", ns, name, err)
	}
	_, err = b.client.Kube.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update Secret %s/%s: %w", ns, name, err)
	}
	return nil
}
