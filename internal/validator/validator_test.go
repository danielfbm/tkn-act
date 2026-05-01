package validator_test

import (
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/validator"
)

func TestRejectsUnknownTaskRef(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: doesnotexist}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("want error for unknown taskRef")
	}
	if !strings.Contains(errs[0].Error(), "doesnotexist") {
		t.Errorf("err: %v", errs[0])
	}
}

func TestRejectsCycle(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}, runAfter: [b]}
    - {name: b, taskRef: {name: t}, runAfter: [a]}
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "cycle") {
		t.Errorf("want cycle, got %v", errs)
	}
}

func TestRejectsMissingWorkspaceBinding(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  workspaces: [{name: src}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: a
      taskRef: {name: t}
      # no workspaces binding
`))
	if err != nil {
		t.Fatal(err)
	}
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatal("expected workspace error")
	}
}
