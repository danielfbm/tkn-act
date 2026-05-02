package k3d_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

func TestEnsureCreatesIfMissing(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	fake.Set("k3d cluster create tkn-act --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0", []byte(""), nil)
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
		if c == "k3d cluster create tkn-act --no-lb --wait --timeout 120s --k3s-arg --disable=traefik@server:0" {
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
