package tekton_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestNewDefaultVersion: tekton.New with empty Options.Version picks up
// DefaultTektonVersion. Guards against accidental drift between the
// exported constant and the in-binary default.
func TestNewDefaultVersion(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	expected := "kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/" + tekton.DefaultTektonVersion + "/release.yaml"
	runner.Set(expected, []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		// Version intentionally left empty — must default.
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	var found bool
	for _, c := range runner.Calls() {
		if c == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("installer did not call kubectl apply with DefaultTektonVersion (%q); calls=%v",
			tekton.DefaultTektonVersion, runner.Calls())
	}
}

func TestSkipsIfCRDPresent(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset(&apiext.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pipelines.tekton.dev"},
	})
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment(), pipelinesInfo(tekton.DefaultTektonVersion))
	runner := cmdrunner.NewFake()
	// no canned `kubectl apply` — if installer tries to call it, the test fails
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    tekton.DefaultTektonVersion,
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range runner.Calls() {
		if len(c) >= 6 && c[:6] == "kubect" {
			t.Errorf("apply called when CRD already present: %v", runner.Calls())
		}
	}
}

func TestAppliesIfCRDMissing(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/"+tekton.DefaultTektonVersion+"/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    tekton.DefaultTektonVersion,
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
}

func TestApplyFailureBubbles(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset()
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/"+tekton.DefaultTektonVersion+"/release.yaml", nil, errors.New("boom"))
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    tekton.DefaultTektonVersion,
	})
	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// TestEnablesStepActionsFlag: after Install completes, the
// feature-flags ConfigMap in tekton-pipelines must have
// `enable-step-actions: "true"`. Step results (the e2e step-results
// fixture and the v1.2 step-results feature) require this flag, but
// upstream Tekton ships it disabled by default.
func TestEnablesStepActionsFlag(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset(
		readyControllerDeployment(), readyWebhookDeployment(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "feature-flags", Namespace: "tekton-pipelines"},
			Data:       map[string]string{},
		},
	)
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/"+tekton.DefaultTektonVersion+"/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    tekton.DefaultTektonVersion,
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	cm, err := kube.CoreV1().ConfigMaps("tekton-pipelines").Get(context.Background(), "feature-flags", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get feature-flags: %v", err)
	}
	if got := cm.Data["enable-step-actions"]; got != "true" {
		t.Errorf("enable-step-actions = %q, want \"true\"", got)
	}
	if got := cm.Data["enable-api-fields"]; got != "alpha" {
		t.Errorf("enable-api-fields = %q, want \"alpha\" (matrix-result [*] aggregation needs alpha api fields)", got)
	}
}

// TestEnablesStepActionsFlagCreatesIfMissing: a fresh cluster where
// the controller is up but the feature-flags ConfigMap hasn't been
// reconciled yet must still get the flag set (Install creates it).
func TestEnablesStepActionsFlagCreatesIfMissing(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/"+tekton.DefaultTektonVersion+"/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    tekton.DefaultTektonVersion,
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	cm, err := kube.CoreV1().ConfigMaps("tekton-pipelines").Get(context.Background(), "feature-flags", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get feature-flags: %v", err)
	}
	if got := cm.Data["enable-step-actions"]; got != "true" {
		t.Errorf("enable-step-actions = %q, want \"true\"", got)
	}
	if got := cm.Data["enable-api-fields"]; got != "alpha" {
		t.Errorf("enable-api-fields = %q, want \"alpha\"", got)
	}
}

// TestDefaultTektonVersionIsLatestLTSPatch pins the default to the
// newest declared Tekton LTS line's latest patch (v1.15.x). Bump in
// lock-step with .github/workflows/cluster-integration.yml.
func TestDefaultTektonVersionIsLatestLTSPatch(t *testing.T) {
	if tekton.DefaultTektonVersion != "v1.15.5" {
		t.Errorf("DefaultTektonVersion = %q, want v1.15.5", tekton.DefaultTektonVersion)
	}
}

// applyRunner is a cmdrunner.Runner that, on `kubectl apply`, simulates
// the release.yaml landing by rewriting pipelines-info to the applied
// version and (re)creating ready controller/webhook deployments.
type applyRunner struct {
	kube    *fake.Clientset
	version string
	calls   []string
}

func (r *applyRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	if name == "kubectl" && strings.Contains(line, " apply -f ") {
		_ = r.kube.CoreV1().ConfigMaps("tekton-pipelines").Delete(ctx, "pipelines-info", metav1.DeleteOptions{})
		_, _ = r.kube.CoreV1().ConfigMaps("tekton-pipelines").Create(ctx, pipelinesInfo(r.version), metav1.CreateOptions{})
		for _, d := range []*appsv1.Deployment{readyControllerDeployment(), readyWebhookDeployment()} {
			_ = r.kube.AppsV1().Deployments("tekton-pipelines").Delete(ctx, d.Name, metav1.DeleteOptions{})
			_, _ = r.kube.AppsV1().Deployments("tekton-pipelines").Create(ctx, d, metav1.CreateOptions{})
		}
	}
	return []byte("ok"), nil
}

func (r *applyRunner) Run(ctx context.Context, stdout, _ io.Writer, name string, args ...string) error {
	out, err := r.Output(ctx, name, args...)
	if stdout != nil {
		_, _ = stdout.Write(out)
	}
	return err
}

func (r *applyRunner) applied() bool {
	for _, c := range r.calls {
		if strings.Contains(c, " apply -f ") {
			return true
		}
	}
	return false
}

func pipelinesCRD() *apiext.CustomResourceDefinition {
	return &apiext.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "pipelines.tekton.dev"}}
}

func pipelinesInfo(version string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pipelines-info", Namespace: "tekton-pipelines"},
		Data:       map[string]string{"version": version},
	}
}

func newTestInstaller(kube *fake.Clientset, r *applyRunner, version string) *tekton.Installer {
	return tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     r,
		Apiext:     apiextfake.NewSimpleClientset(pipelinesCRD()),
		Kube:       kube,
		Version:    version,
	})
}

// TestSkipsApplyWhenInstalledVersionMatches: CRD present, deployments
// present, pipelines-info reports the requested version — no apply.
func TestSkipsApplyWhenInstalledVersionMatches(t *testing.T) {
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment(), pipelinesInfo("v1.15.5"))
	r := &applyRunner{kube: kube, version: "v1.15.5"}
	inst := newTestInstaller(kube, r, "v1.15.5")
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if r.applied() {
		t.Errorf("apply called although installed version matches: %v", r.calls)
	}
	if got := inst.InstalledVersion(); got != "v1.15.5" {
		t.Errorf("InstalledVersion = %q, want v1.15.5", got)
	}
}

// TestReappliesOnVersionMismatch: an existing cluster on an older
// Tekton must be upgraded when a different version is requested.
func TestReappliesOnVersionMismatch(t *testing.T) {
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment(), pipelinesInfo("v1.12.0"))
	r := &applyRunner{kube: kube, version: "v1.15.5"}
	inst := newTestInstaller(kube, r, "v1.15.5")
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := "kubectl --kubeconfig /tmp/kc apply -f https://github.com/tektoncd/pipeline/releases/download/v1.15.5/release.yaml"
	if len(r.calls) != 1 || r.calls[0] != want {
		t.Errorf("calls = %v, want [%s]", r.calls, want)
	}
	if got := inst.InstalledVersion(); got != "v1.15.5" {
		t.Errorf("InstalledVersion = %q, want v1.15.5", got)
	}
	// Feature flags are re-asserted after the apply (release.yaml
	// resets feature-flags to upstream defaults).
	cm, err := kube.CoreV1().ConfigMaps("tekton-pipelines").Get(context.Background(), "feature-flags", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get feature-flags: %v", err)
	}
	if cm.Data["enable-api-fields"] != "alpha" {
		t.Errorf("enable-api-fields = %q, want alpha", cm.Data["enable-api-fields"])
	}
}

// TestVersionCompareIgnoresVPrefix: "1.15.5" and "v1.15.5" are the
// same release; no re-apply.
func TestVersionCompareIgnoresVPrefix(t *testing.T) {
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment(), pipelinesInfo("1.15.5"))
	r := &applyRunner{kube: kube, version: "v1.15.5"}
	inst := newTestInstaller(kube, r, "v1.15.5")
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if r.applied() {
		t.Errorf("apply called for equivalent version: %v", r.calls)
	}
}

// TestReappliesWhenDeploymentsMissing: an interrupted first install
// leaves the CRD behind without the controller/webhook. Install must
// re-apply instead of timing out forever.
func TestReappliesWhenDeploymentsMissing(t *testing.T) {
	kube := fake.NewSimpleClientset(pipelinesInfo("v1.15.5"))
	r := &applyRunner{kube: kube, version: "v1.15.5"}
	inst := newTestInstaller(kube, r, "v1.15.5")
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.applied() {
		t.Errorf("apply not called although deployments are missing")
	}
}

// TestReappliesWhenPipelinesInfoMissing: CRD + deployments but no
// pipelines-info ConfigMap means the install is incomplete (or the
// version is unknowable) — re-apply the requested release.
func TestReappliesWhenPipelinesInfoMissing(t *testing.T) {
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	r := &applyRunner{kube: kube, version: "v1.15.5"}
	inst := newTestInstaller(kube, r, "v1.15.5")
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.applied() {
		t.Errorf("apply not called although pipelines-info is missing")
	}
}

func TestReadInstalledVersion(t *testing.T) {
	ctx := context.Background()
	if _, err := tekton.ReadInstalledVersion(ctx, fake.NewSimpleClientset()); err == nil {
		t.Error("expected error when pipelines-info is missing")
	}
	got, err := tekton.ReadInstalledVersion(ctx, fake.NewSimpleClientset(pipelinesInfo("v1.9.9")))
	if err != nil || got != "v1.9.9" {
		t.Errorf("ReadInstalledVersion = %q, %v; want v1.9.9", got, err)
	}
	if _, err := tekton.ReadInstalledVersion(ctx, fake.NewSimpleClientset(pipelinesInfo(""))); err == nil {
		t.Error("expected error when data.version is empty")
	}
}

// TestWaitReadyWaitsForRollout: a deployment whose new generation has
// not been observed yet (mid-upgrade) is not ready even if the old
// replicas are.
func TestWaitReadyWaitsForRollout(t *testing.T) {
	ctrl := readyControllerDeployment()
	ctrl.Generation = 2
	ctrl.Status.ObservedGeneration = 1
	kube := fake.NewSimpleClientset(ctrl, readyWebhookDeployment(), pipelinesInfo("v1.15.5"))
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     &applyRunner{kube: kube, version: "v1.15.5"},
		Apiext:     apiextfake.NewSimpleClientset(pipelinesCRD()),
		Kube:       kube,
		Version:    "v1.15.5",
		Timeout:    time.Millisecond,
	})
	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected timeout while rollout is pending")
	}
}

func readyControllerDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "tekton-pipelines-controller", Namespace: "tekton-pipelines"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
	}
}
func readyWebhookDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "tekton-pipelines-webhook", Namespace: "tekton-pipelines"},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
	}
}
