package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveTektonVersion(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"flag wins over env and default", "v1.9.9", "v1.12.3", "v1.9.9"},
		{"env wins when flag empty", "", "v1.12.3", "v1.12.3"},
		{"default when neither set", "", "", tekton.DefaultTektonVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envTektonVersion, tc.env)
			if got := resolveTektonVersion(tc.flag); got != tc.want {
				t.Errorf("resolveTektonVersion(%q) with env=%q = %q, want %q",
					tc.flag, tc.env, got, tc.want)
			}
		})
	}
}

// TestClusterBackendOptionsHonoursTektonVersionEnv: `run --cluster`
// must install the release named by $TKN_ACT_TEKTON_VERSION on a fresh
// machine, exactly as `cluster up` does.
func TestClusterBackendOptionsHonoursTektonVersionEnv(t *testing.T) {
	root := t.TempDir()

	t.Setenv(envTektonVersion, "v1.9.9")
	opt := clusterBackendOptions(root, nil, nil)
	if opt.TektonVersion != "v1.9.9" {
		t.Errorf("TektonVersion = %q, want v1.9.9", opt.TektonVersion)
	}
	if opt.CacheDir != root {
		t.Errorf("CacheDir = %q, want %q", opt.CacheDir, root)
	}
	if opt.Driver == nil {
		t.Fatal("Driver is nil")
	}
	if got, want := opt.Driver.Kubeconfig(), filepath.Join(root, "cluster", "kubeconfig"); got != want {
		t.Errorf("Kubeconfig = %q, want %q", got, want)
	}

	t.Setenv(envTektonVersion, "")
	if got := clusterBackendOptions(root, nil, nil).TektonVersion; got != tekton.DefaultTektonVersion {
		t.Errorf("TektonVersion with no env = %q, want %q", got, tekton.DefaultTektonVersion)
	}
}

// TestInstalledTektonVersion: `cluster up` prints what the cluster
// actually runs, not merely what was requested.
func TestInstalledTektonVersion(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pipelines-info", Namespace: "tekton-pipelines"},
		Data:       map[string]string{"version": "v1.12.3"},
	})
	if got := installedTektonVersion(ctx, kube, "v1.15.5"); got != "v1.12.3" {
		t.Errorf("installedTektonVersion = %q, want v1.12.3", got)
	}
	got := installedTektonVersion(ctx, fake.NewSimpleClientset(), "v1.15.5")
	if !strings.HasPrefix(got, "v1.15.5") || !strings.Contains(got, "requested") {
		t.Errorf("fallback = %q, want requested version marked as such", got)
	}
	if got := installedTektonVersion(ctx, nil, "v1.15.5"); !strings.Contains(got, "requested") {
		t.Errorf("nil client fallback = %q", got)
	}
}

func TestKubeClientForBadPath(t *testing.T) {
	if c := kubeClientFor(filepath.Join(t.TempDir(), "missing")); c != nil {
		t.Errorf("kubeClientFor(missing) = %v, want nil", c)
	}
}
