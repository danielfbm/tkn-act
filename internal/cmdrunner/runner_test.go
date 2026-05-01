package cmdrunner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/cmdrunner"
)

func TestRealRunnerEcho(t *testing.T) {
	r := cmdrunner.New()
	out, err := r.Output(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("got %q", out)
	}
}

func TestFakeRunnerCapturesArgs(t *testing.T) {
	fake := cmdrunner.NewFake()
	fake.Set("k3d cluster list -o json", []byte("[]"), nil)
	r := fake.Runner()
	out, err := r.Output(context.Background(), "k3d", "cluster", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[]" {
		t.Errorf("got %q", out)
	}
	if got := fake.Calls(); len(got) != 1 || got[0] != "k3d cluster list -o json" {
		t.Errorf("calls: %v", got)
	}
}
