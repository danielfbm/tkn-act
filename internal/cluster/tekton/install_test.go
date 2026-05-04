package tekton_test

import (
	"context"
	"errors"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSkipsIfCRDPresent(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset(&apiext.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pipelines.tekton.dev"},
	})
	kube := fake.NewSimpleClientset(readyControllerDeployment(), readyWebhookDeployment())
	runner := cmdrunner.NewFake()
	// no canned `kubectl apply` — if installer tries to call it, the test fails
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
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
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
	})
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
}

func TestApplyFailureBubbles(t *testing.T) {
	apiextCli := apiextfake.NewSimpleClientset()
	kube := fake.NewSimpleClientset()
	runner := cmdrunner.NewFake()
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", nil, errors.New("boom"))
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
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
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
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
	runner.Set("kubectl --kubeconfig /tmp/kc apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml", []byte("ok"), nil)
	inst := tekton.New(tekton.Options{
		Kubeconfig: "/tmp/kc",
		Runner:     runner.Runner(),
		Apiext:     apiextCli,
		Kube:       kube,
		Version:    "v0.65.0",
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
