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

func TestUnmarshalTaskWithStepTemplate(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate:
    image: alpine:3
    env:
      - name: SHARED
        value: hello
  steps:
    - name: a
      script: 'echo $SHARED'
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.StepTemplate == nil {
		t.Fatalf("StepTemplate is nil")
	}
	if got.Spec.StepTemplate.Image != "alpine:3" {
		t.Errorf("StepTemplate.Image = %q, want alpine:3", got.Spec.StepTemplate.Image)
	}
	if len(got.Spec.StepTemplate.Env) != 1 || got.Spec.StepTemplate.Env[0].Name != "SHARED" {
		t.Errorf("StepTemplate.Env = %+v", got.Spec.StepTemplate.Env)
	}
}

func TestUnmarshalTaskWithoutStepTemplate(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: a
      image: alpine:3
      script: 'true'
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.StepTemplate != nil {
		t.Errorf("StepTemplate = %+v, want nil", got.Spec.StepTemplate)
	}
}

func TestUnmarshalPipelineWithDisplayNameAndDescription(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  displayName: "Build & test"
  description: "Build then test."
  tasks:
    - name: t
      displayName: "Compile binary"
      taskRef: {name: t}
  finally:
    - name: f
      displayName: "Notify"
      taskRef: {name: t}
`)
	var got Pipeline
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DisplayName != "Build & test" {
		t.Errorf("Pipeline.Spec.DisplayName = %q", got.Spec.DisplayName)
	}
	if got.Spec.Description != "Build then test." {
		t.Errorf("Pipeline.Spec.Description = %q", got.Spec.Description)
	}
	if got.Spec.Tasks[0].DisplayName != "Compile binary" {
		t.Errorf("Tasks[0].DisplayName = %q", got.Spec.Tasks[0].DisplayName)
	}
	if got.Spec.Finally[0].DisplayName != "Notify" {
		t.Errorf("Finally[0].DisplayName = %q", got.Spec.Finally[0].DisplayName)
	}
}

func TestUnmarshalTaskWithDisplayNameAndStepFields(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  displayName: "Unit-test runner"
  description: "Runs go test."
  steps:
    - name: s
      displayName: "Compile"
      description: "Compile the test binary."
      image: golang:1.25
      script: 'true'
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DisplayName != "Unit-test runner" {
		t.Errorf("Task.Spec.DisplayName = %q", got.Spec.DisplayName)
	}
	if got.Spec.Steps[0].DisplayName != "Compile" {
		t.Errorf("Step.DisplayName = %q", got.Spec.Steps[0].DisplayName)
	}
	if got.Spec.Steps[0].Description != "Compile the test binary." {
		t.Errorf("Step.Description = %q", got.Spec.Steps[0].Description)
	}
}

func TestUnmarshalTaskWithSidecars(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - name: redis
      image: redis:7-alpine
      env:
        - {name: TZ, value: UTC}
    - name: mock
      image: mock:1
      script: 'serve --port 8080'
      volumeMounts:
        - {name: shared, mountPath: /data}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Sidecars) != 2 {
		t.Fatalf("Sidecars = %d, want 2", len(got.Spec.Sidecars))
	}
	if got.Spec.Sidecars[0].Name != "redis" || got.Spec.Sidecars[0].Image != "redis:7-alpine" {
		t.Errorf("sidecar[0] = %+v", got.Spec.Sidecars[0])
	}
	if got.Spec.Sidecars[0].Env[0].Name != "TZ" {
		t.Errorf("sidecar[0].Env = %+v", got.Spec.Sidecars[0].Env)
	}
	if got.Spec.Sidecars[1].Script == "" {
		t.Errorf("sidecar[1].Script empty")
	}
	if len(got.Spec.Sidecars[1].VolumeMounts) != 1 || got.Spec.Sidecars[1].VolumeMounts[0].MountPath != "/data" {
		t.Errorf("sidecar[1].VolumeMounts = %+v", got.Spec.Sidecars[1].VolumeMounts)
	}
}

func TestUnmarshalTaskWithoutSidecars(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: s, image: alpine:3, script: 'true'}
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Sidecars) != 0 {
		t.Errorf("Sidecars = %+v, want empty", got.Spec.Sidecars)
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

func TestUnmarshalPipelineTaskWithMatrix(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: build}
      matrix:
        params:
          - name: os
            value: [linux, darwin]
          - name: goversion
            value: ["1.21", "1.22"]
        include:
          - name: arm-extra
            params:
              - {name: os, value: linux}
              - {name: goversion, value: "1.22"}
              - {name: arch, value: arm64}
`)
	var got Pipeline
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(got.Spec.Tasks))
	}
	m := got.Spec.Tasks[0].Matrix
	if m == nil {
		t.Fatalf("Matrix is nil")
	}
	if len(m.Params) != 2 {
		t.Fatalf("Params = %d, want 2", len(m.Params))
	}
	if m.Params[0].Name != "os" || len(m.Params[0].Value) != 2 || m.Params[0].Value[0] != "linux" {
		t.Errorf("Params[0] = %+v, want os=[linux,darwin]", m.Params[0])
	}
	if m.Params[1].Name != "goversion" || m.Params[1].Value[1] != "1.22" {
		t.Errorf("Params[1] = %+v", m.Params[1])
	}
	if len(m.Include) != 1 || m.Include[0].Name != "arm-extra" {
		t.Errorf("Include = %+v, want one arm-extra row", m.Include)
	}
	if len(m.Include[0].Params) != 3 || m.Include[0].Params[2].Name != "arch" {
		t.Errorf("Include[0].Params = %+v", m.Include[0].Params)
	}
	if m.Include[0].Params[2].Value.StringVal != "arm64" {
		t.Errorf("Include[0].Params[2].Value = %+v", m.Include[0].Params[2].Value)
	}
}

func TestUnmarshalPipelineTaskWithoutMatrix(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: build, taskRef: {name: build}}
`)
	var got Pipeline
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Tasks[0].Matrix != nil {
		t.Errorf("Matrix = %+v, want nil", got.Spec.Tasks[0].Matrix)
	}
}

// TestUnmarshalTaskRefWithResolver pins the YAML round-trip for a
// resolver-backed taskRef. The Phase 1 spec adds Resolver+ResolverParams
// as optional fields on TaskRef; agents should be able to load a
// PipelineTask whose taskRef references a `git` resolver with the
// usual `params: [{name, value}]` block nested inside.
func TestUnmarshalTaskRefWithResolver(t *testing.T) {
	in := []byte(`
name: build
taskRef:
  resolver: git
  params:
    - name: url
      value: https://github.com/tektoncd/catalog
    - name: revision
      value: main
    - name: pathInRepo
      value: task/golang-build/0.4/golang-build.yaml
`)
	var got PipelineTask
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TaskRef == nil {
		t.Fatal("TaskRef nil")
	}
	if got.TaskRef.Resolver != "git" {
		t.Errorf("resolver = %q, want git", got.TaskRef.Resolver)
	}
	if got.TaskRef.Name != "" {
		t.Errorf("expected empty Name with resolver set, got %q", got.TaskRef.Name)
	}
	if len(got.TaskRef.ResolverParams) != 3 {
		t.Fatalf("resolver params = %d, want 3", len(got.TaskRef.ResolverParams))
	}
	if got.TaskRef.ResolverParams[0].Name != "url" ||
		got.TaskRef.ResolverParams[0].Value.StringVal != "https://github.com/tektoncd/catalog" {
		t.Errorf("params[0] = %+v", got.TaskRef.ResolverParams[0])
	}
	if got.TaskRef.ResolverParams[2].Name != "pathInRepo" {
		t.Errorf("params[2].name = %q", got.TaskRef.ResolverParams[2].Name)
	}
}

// TestUnmarshalPipelineRefWithResolver pins the round-trip for a
// resolver-backed pipelineRef on a PipelineRun.
func TestUnmarshalPipelineRefWithResolver(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: my-run
spec:
  pipelineRef:
    resolver: hub
    params:
      - name: catalog
        value: tekton
      - name: kind
        value: pipeline
      - name: name
        value: ci
      - name: version
        value: "0.4"
`)
	var got PipelineRun
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Spec.PipelineRef == nil {
		t.Fatal("PipelineRef nil")
	}
	if got.Spec.PipelineRef.Resolver != "hub" {
		t.Errorf("resolver = %q, want hub", got.Spec.PipelineRef.Resolver)
	}
	if got.Spec.PipelineRef.Name != "" {
		t.Errorf("expected empty Name, got %q", got.Spec.PipelineRef.Name)
	}
	if len(got.Spec.PipelineRef.ResolverParams) != 4 {
		t.Fatalf("resolver params = %d, want 4", len(got.Spec.PipelineRef.ResolverParams))
	}
	if got.Spec.PipelineRef.ResolverParams[3].Name != "version" ||
		got.Spec.PipelineRef.ResolverParams[3].Value.StringVal != "0.4" {
		t.Errorf("params[3] = %+v", got.Spec.PipelineRef.ResolverParams[3])
	}
}

func TestUnmarshalStepActionDoc(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params:
    - name: who
      default: world
  results:
    - name: greeting
  image: alpine:3
  script: 'echo hello $(params.who)'
`)
	var got StepAction
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "StepAction" || got.APIVersion != "tekton.dev/v1beta1" {
		t.Errorf("envelope = %+v", got.Object)
	}
	if got.Spec.Image != "alpine:3" {
		t.Errorf("image = %q", got.Spec.Image)
	}
	if len(got.Spec.Params) != 1 || got.Spec.Params[0].Name != "who" {
		t.Errorf("params = %+v", got.Spec.Params)
	}
	if len(got.Spec.Results) != 1 || got.Spec.Results[0].Name != "greeting" {
		t.Errorf("results = %+v", got.Spec.Results)
	}
}

func TestUnmarshalStepWithRef(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: clone
      ref: {name: git-clone}
      params:
        - name: url
          value: https://example.com/repo
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Steps[0].Ref == nil || got.Spec.Steps[0].Ref.Name != "git-clone" {
		t.Errorf("ref = %+v", got.Spec.Steps[0].Ref)
	}
	if got.Spec.Steps[0].Image != "" {
		t.Errorf("image = %q (want empty when ref is set)", got.Spec.Steps[0].Image)
	}
	if len(got.Spec.Steps[0].Params) != 1 || got.Spec.Steps[0].Params[0].Name != "url" {
		t.Errorf("params = %+v", got.Spec.Steps[0].Params)
	}
}
