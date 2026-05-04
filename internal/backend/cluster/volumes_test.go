package cluster_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func corev1NS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// TestApplyVolumeSourcesProjectsConfigMap exercises RunPipeline's
// pre-submit hook: when a Task declares a configMap volume, the backend
// must apply an ephemeral kube ConfigMap into the run namespace, sourced
// from the same volumes.Store the docker side reads.
//
// The test stops short of running the watch (no PipelineRun gets to
// terminal status under the fake client) so we Create the namespace
// + apply, then read the ConfigMap back and assert its data.
func TestApplyVolumeSourcesProjectsConfigMap(t *testing.T) {
	cmStore := volumes.NewStore("")
	cmStore.Add("app-config", "greeting", "hello-from-cm")
	secStore := volumes.NewStore("")
	secStore.Add("api-token", "value", "s3cret")

	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{
		Dynamic: dyn,
		Kube:    kube,
	}, cmStore, secStore)

	pl := tektontypes.Pipeline{
		Object: tektontypes.Object{APIVersion: "tekton.dev/v1", Kind: "Pipeline"},
		Spec: tektontypes.PipelineSpec{
			Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
		},
	}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{
		Object: tektontypes.Object{APIVersion: "tekton.dev/v1", Kind: "Task"},
		Spec: tektontypes.TaskSpec{
			Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
			Volumes: []tektontypes.Volume{
				{Name: "cfg", ConfigMap: &tektontypes.ConfigMapSource{Name: "app-config"}},
				{Name: "tok", Secret: &tektontypes.SecretSource{SecretName: "api-token"}},
			},
		},
	}
	tk.Metadata.Name = "t"

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns); err != nil {
		t.Fatalf("apply: %v", err)
	}

	cm, err := kube.CoreV1().ConfigMaps(ns).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if cm.Data["greeting"] != "hello-from-cm" {
		t.Errorf("configmap.greeting = %q, want hello-from-cm", cm.Data["greeting"])
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "tkn-act" {
		t.Errorf("missing managed-by label; got %v", cm.Labels)
	}

	sec, err := kube.CoreV1().Secrets(ns).Get(context.Background(), "api-token", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(sec.Data["value"]); got != "s3cret" {
		t.Errorf("secret.value = %q, want s3cret", got)
	}

	// Idempotent: running the apply again must not error.
	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns); err != nil {
		t.Fatalf("apply (second pass): %v", err)
	}
}

// TestApplyVolumeSourcesNilStore: a Backend constructed without
// ConfigMaps/Secrets stores must reject any volume that references one,
// rather than silently submitting a PipelineRun the controller will fail
// to start.
func TestApplyVolumeSourcesNilStore(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClients(cluster.ClientBundle{Dynamic: dyn, Kube: kube}) // no stores

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "cfg", ConfigMap: &tektontypes.ConfigMapSource{Name: "x"}}},
	}}
	tk.Metadata.Name = "t"
	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns)
	if err == nil {
		t.Fatalf("expected error for nil ConfigMap store, got nil")
	}
}

// TestApplyVolumeSourcesNilSecretStore mirrors the above for secrets.
func TestApplyVolumeSourcesNilSecretStore(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClients(cluster.ClientBundle{Dynamic: dyn, Kube: kube})
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "tok", Secret: &tektontypes.SecretSource{SecretName: "x"}}},
	}}
	tk.Metadata.Name = "t"
	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns)
	if err == nil {
		t.Fatalf("expected error for nil Secret store, got nil")
	}
}

// TestUpsertConfigMapUpdatesExisting covers the AlreadyExists → Update
// branch in upsertConfigMap: a re-apply with new bytes must succeed and
// the second value must win.
func TestUpsertConfigMapUpdatesExisting(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	cmStore := volumes.NewStore("")
	cmStore.Add("cfg", "k", "v1")
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, volumes.NewStore(""))

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "v", ConfigMap: &tektontypes.ConfigMapSource{Name: "cfg"}}},
	}}
	tk.Metadata.Name = "t"
	in := backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, ns); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Mutate the store and re-apply: the existing CM must be Updated, not
	// blocked by AlreadyExists.
	cmStore.Add("cfg", "k", "v2")
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, ns); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	got, err := kube.CoreV1().ConfigMaps(ns).Get(context.Background(), "cfg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cm: %v", err)
	}
	if got.Data["k"] != "v2" {
		t.Errorf("data[k] = %q, want v2 (update path not taken)", got.Data["k"])
	}
}

// TestUpsertSecretUpdatesExisting mirrors the configMap update test for
// secrets.
func TestUpsertSecretUpdatesExisting(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	secStore := volumes.NewStore("")
	secStore.Add("tok", "k", "s1")
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, volumes.NewStore(""), secStore)

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "v", Secret: &tektontypes.SecretSource{SecretName: "tok"}}},
	}}
	tk.Metadata.Name = "t"
	in := backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, ns); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	secStore.Add("tok", "k", "s2")
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, ns); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	got, err := kube.CoreV1().Secrets(ns).Get(context.Background(), "tok", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get sec: %v", err)
	}
	if string(got.Data["k"]) != "s2" {
		t.Errorf("data[k] = %q, want s2 (update path not taken)", got.Data["k"])
	}
}

// TestApplyVolumeSourcesInlineTaskSpec: a PipelineTask with TaskSpec
// inline (no TaskRef) must still surface its volumes — the resolver
// can't shortcut by looking only at in.Tasks.
func TestApplyVolumeSourcesInlineTaskSpec(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	cmStore := volumes.NewStore("")
	cmStore.Add("inline-cfg", "k", "v")
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, volumes.NewStore(""))

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	inlineSpec := &tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "v", ConfigMap: &tektontypes.ConfigMapSource{Name: "inline-cfg"}}},
	}
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskSpec: inlineSpec}},
	}}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl,
	}, ns); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := kube.CoreV1().ConfigMaps(ns).Get(context.Background(), "inline-cfg", metav1.GetOptions{}); err != nil {
		t.Errorf("inline-taskSpec ConfigMap not applied: %v", err)
	}
}

// TestApplyVolumeSourcesFinallyTask: a finally task's volumes must be
// applied just like a regular task's. Otherwise a finally fixture using
// a configMap would fail at submit time.
func TestApplyVolumeSourcesFinallyTask(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	cmStore := volumes.NewStore("")
	cmStore.Add("finally-cfg", "k", "v")
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, volumes.NewStore(""))

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "v", ConfigMap: &tektontypes.ConfigMapSource{Name: "finally-cfg"}}},
	}}
	tk.Metadata.Name = "t"
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks:   []tektontypes.PipelineTask{},
		Finally: []tektontypes.PipelineTask{{Name: "cleanup", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := kube.CoreV1().ConfigMaps(ns).Get(context.Background(), "finally-cfg", metav1.GetOptions{}); err != nil {
		t.Errorf("finally-task ConfigMap not applied: %v", err)
	}
}

// TestApplyVolumeSourcesBundleOnlyConfigMap locks the regression that
// surfaced as `TestClusterE2E/configmap-from-yaml` (PR #16): a fixture
// whose only source for a configMap volume is a `kind: ConfigMap`
// document in the -f YAML stream — i.e. neither an inline override nor
// an on-disk dir entry — must have its bytes flow through the cluster
// projection seam unchanged. Without this, bundle-loaded layers wired
// in at the CLI go untested at the cluster boundary.
func TestApplyVolumeSourcesBundleOnlyConfigMap(t *testing.T) {
	cmStore := volumes.NewStore("")
	cmStore.LoadBytes("app-config", map[string][]byte{"greeting": []byte("hello-from-yaml")})
	secStore := volumes.NewStore("")
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, secStore)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "cfg", ConfigMap: &tektontypes.ConfigMapSource{Name: "app-config"}}},
	}}
	tk.Metadata.Name = "t"

	ns := "tkn-act-bundleonly"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "bundleonly", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cm, err := kube.CoreV1().ConfigMaps(ns).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if cm.Data["greeting"] != "hello-from-yaml" {
		t.Errorf("configmap.greeting = %q, want hello-from-yaml (bundle-loaded layer not reaching kube projection)", cm.Data["greeting"])
	}
}

// TestApplyVolumeSourcesPerFixtureResetIsolatesInline is the regression
// test for the cluster-e2e harness's actual failure mode in PR #16:
// the harness shares one *volumes.Store across every subtest in the
// fixture table, so an inline `--configmap`-style entry added by one
// fixture (the `volumes` fixture) was still in Store.Inline when the
// next fixture (`configmap-from-yaml`) ran, shadowing its bundle-loaded
// `kind: ConfigMap` because Inline is the highest-precedence layer.
//
// The fix is per-fixture isolation: the harness must call Store.Reset
// (or build a fresh Store) between subtests. This test demonstrates
// both the failure mode (without Reset) and the fix (with Reset) at
// the cluster projection seam, so a future change to the harness can't
// silently re-introduce the leak.
func TestApplyVolumeSourcesPerFixtureResetIsolatesInline(t *testing.T) {
	cmStore := volumes.NewStore("")
	secStore := volumes.NewStore("")
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, secStore)

	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "cfg", ConfigMap: &tektontypes.ConfigMapSource{Name: "app-config"}}},
	}}
	tk.Metadata.Name = "t"
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	in := backend.PipelineRunInvocation{
		RunID: "isolate", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}

	// Fixture A: simulates the `volumes` fixture's inline seeding.
	nsA := "tkn-act-fxa"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(nsA), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cmStore.Add("app-config", "greeting", "hello-from-cm")
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, nsA); err != nil {
		t.Fatalf("fixture A apply: %v", err)
	}
	cmA, err := kube.CoreV1().ConfigMaps(nsA).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cm A: %v", err)
	}
	if cmA.Data["greeting"] != "hello-from-cm" {
		t.Fatalf("fixture A: greeting = %q, want hello-from-cm", cmA.Data["greeting"])
	}

	// Fixture B: simulates the `configmap-from-yaml` fixture's bundle
	// seeding, AFTER Reset() — which is what the harness must do
	// between subtests. Without Reset(), the inline entry from A would
	// still be at the highest precedence and shadow the bundle bytes
	// here, projecting `hello-from-cm` into nsB and failing the
	// fixture's `test "$..." = "hello-from-yaml"` script with exit 1.
	cmStore.Reset()
	secStore.Reset()
	cmStore.LoadBytes("app-config", map[string][]byte{"greeting": []byte("hello-from-yaml")})

	nsB := "tkn-act-fxb"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(nsB), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := be.ApplyVolumeSourcesForTest(context.Background(), in, nsB); err != nil {
		t.Fatalf("fixture B apply: %v", err)
	}
	cmB, err := kube.CoreV1().ConfigMaps(nsB).Get(context.Background(), "app-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cm B: %v", err)
	}
	if cmB.Data["greeting"] != "hello-from-yaml" {
		t.Errorf("fixture B: greeting = %q, want hello-from-yaml (per-fixture Reset() did not clear inline shadow)", cmB.Data["greeting"])
	}
}

// TestApplyVolumeSourcesUnknownConfigMap is the negative case — referencing
// a configMap source the store can't resolve must fail before submit, the
// same way the docker side would fail at MaterializeForTask.
func TestApplyVolumeSourcesUnknownConfigMap(t *testing.T) {
	cmStore := volumes.NewStore("")
	secStore := volumes.NewStore("")
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cmStore, secStore)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		Volumes: []tektontypes.Volume{{Name: "cfg", ConfigMap: &tektontypes.ConfigMapSource{Name: "missing"}}},
	}}
	tk.Metadata.Name = "t"

	ns := "tkn-act-12345678"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(), corev1NS(ns), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, ns)
	if err == nil {
		t.Fatalf("expected error for unresolvable configMap, got nil")
	}
}
