package cluster_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// TestWaitForDefaultServiceAccountReady: the helper returns immediately
// when the default SA is already present in the namespace.
func TestWaitForDefaultServiceAccountReady(t *testing.T) {
	kube := kubefake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "tkn-act-abc"},
	})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClients(cluster.ClientBundle{Kube: kube, Dynamic: dyn})

	if err := be.WaitForDefaultServiceAccountForTest(context.Background(), "tkn-act-abc", time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestWaitForDefaultServiceAccountAppearsLater: a NotFound response on
// the first Get is retried; once the SA appears in the fake clientset,
// the wait returns nil.
func TestWaitForDefaultServiceAccountAppearsLater(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClients(cluster.ClientBundle{Kube: kube, Dynamic: dyn})

	// Inject the SA after a short delay to simulate the SA controller
	// catching up.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = kube.CoreV1().ServiceAccounts("tkn-act-abc").Create(context.Background(), &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "tkn-act-abc"},
		}, metav1.CreateOptions{})
	}()

	if err := be.WaitForDefaultServiceAccountForTest(context.Background(), "tkn-act-abc", time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestWaitForDefaultServiceAccountTimeout: when the SA never appears,
// the wait returns a wrapped error mentioning the namespace and timeout.
func TestWaitForDefaultServiceAccountTimeout(t *testing.T) {
	kube := kubefake.NewSimpleClientset()
	// Make every Get return NotFound (the fake clientset already does
	// this, but we set a reactor explicitly so the test is robust to
	// future fake-client behavior changes).
	kube.PrependReactor("get", "serviceaccounts", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		gvr := schema.GroupResource{Group: "", Resource: "serviceaccounts"}
		return true, nil, apierrors.NewNotFound(gvr, "default")
	})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	be := cluster.NewWithClients(cluster.ClientBundle{Kube: kube, Dynamic: dyn})

	err := be.WaitForDefaultServiceAccountForTest(context.Background(), "tkn-act-abc", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	want := `default ServiceAccount not provisioned in namespace "tkn-act-abc"`
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("error %q does not contain %q", got, want)
	}
}
