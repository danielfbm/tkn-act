package k3d_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

const createCmd = "k3d cluster create tkn-act --image rancher/k3s:v1.34.12-k3s1 --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0"

// TestDefaultK3sImage pins the k3s node image so cluster mode doesn't
// drift with whatever k3s the installed k3d binary defaults to. Must
// stay within the Kubernetes range Tekton's default release tests
// (v1.15.5: 1.29–1.34).
func TestDefaultK3sImage(t *testing.T) {
	if k3d.DefaultK3sImage != "rancher/k3s:v1.34.12-k3s1" {
		t.Errorf("DefaultK3sImage = %q", k3d.DefaultK3sImage)
	}
}

// TestEnsureUsesCustomImage: Options.Image overrides the default.
func TestEnsureUsesCustomImage(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	want := "k3d cluster create tkn-act --image rancher/k3s:v1.33.13-k3s1 --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0"
	fake.Set(want, []byte(""), nil)
	d := k3d.New(k3d.Options{ClusterName: "tkn-act", Image: "rancher/k3s:v1.33.13-k3s1", Runner: fake.Runner()})
	if err := d.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	found := false
	for _, c := range fake.Calls() {
		found = found || c == want
	}
	if !found {
		t.Errorf("create not called with custom image; calls=%v", fake.Calls())
	}
}

func TestEnsureCreatesIfMissing(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	fake.Set(createCmd, []byte(""), nil)
	fake.Set("k3d kubeconfig get tkn-act", []byte("apiVersion: v1\nkind: Config\n"), nil)

	d := k3d.New(k3d.Options{
		ClusterName:    "tkn-act",
		KubeconfigPath: t.TempDir() + "/kubeconfig",
		Runner:         fake.Runner(),
	})
	if err := d.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

func TestEnsureNoopWhenPresent(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act"}]`), nil)
	fake.Set("k3d kubeconfig get tkn-act", []byte("apiVersion: v1\nkind: Config\n"), nil)

	d := k3d.New(k3d.Options{
		ClusterName:    "tkn-act",
		KubeconfigPath: t.TempDir() + "/kubeconfig",
		Runner:         fake.Runner(),
	})
	if err := d.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, c := range fake.Calls() {
		if c == createCmd {
			t.Errorf("create called when cluster already existed; calls=%v", fake.Calls())
		}
	}
}

func TestStatusRunning(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act","serversRunning":1,"agentsRunning":0,"servers":1,"agents":0}]`), nil)
	d := k3d.New(k3d.Options{ClusterName: "tkn-act", Runner: fake.Runner()})
	st, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || !st.Running {
		t.Errorf("status = %+v", st)
	}
}

func TestDelete(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte(`[{"name":"tkn-act"}]`), nil)
	fake.Set("k3d cluster delete tkn-act", []byte(""), nil)
	d := k3d.New(k3d.Options{ClusterName: "tkn-act", Runner: fake.Runner()})
	if err := d.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
}
