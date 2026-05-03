package tektontypes

import (
	"testing"

	"sigs.k8s.io/yaml"
)

func TestUnmarshalTask(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: hello
spec:
  params:
    - name: who
      type: string
      default: world
  steps:
    - name: greet
      image: alpine:3
      script: |
        echo "hello $(params.who)"
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != "Task" {
		t.Errorf("kind = %q, want Task", got.Kind)
	}
	if got.Metadata.Name != "hello" {
		t.Errorf("name = %q, want hello", got.Metadata.Name)
	}
	if len(got.Spec.Params) != 1 || got.Spec.Params[0].Name != "who" {
		t.Errorf("params = %+v", got.Spec.Params)
	}
	if got.Spec.Params[0].Default == nil || got.Spec.Params[0].Default.StringVal != "world" {
		t.Errorf("default = %+v", got.Spec.Params[0].Default)
	}
	if len(got.Spec.Steps) != 1 || got.Spec.Steps[0].Image != "alpine:3" {
		t.Errorf("steps = %+v", got.Spec.Steps)
	}
}

func TestUnmarshalPipeline(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: build-and-test
spec:
  params:
    - name: revision
      type: string
  workspaces:
    - name: source
  tasks:
    - name: fetch
      taskRef:
        name: git-clone
      params:
        - name: revision
          value: $(params.revision)
      workspaces:
        - name: output
          workspace: source
    - name: test
      runAfter: [fetch]
      taskRef:
        name: go-test
      workspaces:
        - name: source
          workspace: source
  finally:
    - name: notify
      taskRef:
        name: slack-msg
`)
	var got Pipeline
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Metadata.Name != "build-and-test" {
		t.Errorf("name = %q", got.Metadata.Name)
	}
	if len(got.Spec.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(got.Spec.Tasks))
	}
	if got.Spec.Tasks[1].RunAfter[0] != "fetch" {
		t.Errorf("runAfter = %v", got.Spec.Tasks[1].RunAfter)
	}
	if len(got.Spec.Finally) != 1 {
		t.Errorf("finally = %d, want 1", len(got.Spec.Finally))
	}
}

func TestUnmarshalPipelineWithTimeouts(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts:
    pipeline: "1h"
    tasks: "55m"
    finally: "5m"
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	var p Pipeline
	if err := yaml.Unmarshal(in, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Timeouts == nil {
		t.Fatalf("Timeouts is nil")
	}
	if got, want := p.Spec.Timeouts.Pipeline, "1h"; got != want {
		t.Errorf("Pipeline = %q, want %q", got, want)
	}
	if got, want := p.Spec.Timeouts.Tasks, "55m"; got != want {
		t.Errorf("Tasks = %q, want %q", got, want)
	}
	if got, want := p.Spec.Timeouts.Finally, "5m"; got != want {
		t.Errorf("Finally = %q, want %q", got, want)
	}
}

func TestUnmarshalPipelineWithoutTimeouts(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	var p Pipeline
	if err := yaml.Unmarshal(in, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Timeouts != nil {
		t.Errorf("Timeouts = %+v, want nil", p.Spec.Timeouts)
	}
}

func TestParamValueScalarAndArray(t *testing.T) {
	in := []byte(`
- name: scalar
  value: hello
- name: array
  value: [a, b, c]
- name: object
  value:
    key: val
`)
	var got []Param
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got[0].Value.StringVal != "hello" || got[0].Value.Type != ParamTypeString {
		t.Errorf("scalar = %+v", got[0].Value)
	}
	if got[1].Value.Type != ParamTypeArray || len(got[1].Value.ArrayVal) != 3 {
		t.Errorf("array = %+v", got[1].Value)
	}
	if got[2].Value.Type != ParamTypeObject || got[2].Value.ObjectVal["key"] != "val" {
		t.Errorf("object = %+v", got[2].Value)
	}
}
