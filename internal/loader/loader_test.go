package loader_test

import (
	"path/filepath"
	"testing"

	"github.com/dfbmorinigo/tkn-act/internal/loader"
)

func TestLoadFiles(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "multi.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(bundle.Tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(bundle.Tasks))
	}
	if _, ok := bundle.Tasks["greet"]; !ok {
		t.Errorf("missing greet task; got %v", bundle.Tasks)
	}
	if _, ok := bundle.Pipelines["chain"]; !ok {
		t.Errorf("missing chain pipeline")
	}
}

func TestRejectsUnknownKind(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: NotARealKind
metadata: {name: x}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRejectsWrongAPIVersion(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1beta1
kind: Task
metadata: {name: old}
spec:
  steps: []
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for v1beta1")
	}
}
