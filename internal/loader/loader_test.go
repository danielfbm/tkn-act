package loader_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
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

// TestRejectsUnsupportedV1Kind covers the `default` branch in loadOne's
// inner v1 switch: only ConfigMap and Secret are accepted at apiVersion
// v1; any other core kind (Pod, Service, etc.) must be rejected with a
// message that names the offending kind.
func TestRejectsUnsupportedV1Kind(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Pod
metadata: {name: p}
spec:
  containers:
    - {name: c, image: busybox}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil {
		t.Fatal("expected error for apiVersion=v1 kind=Pod")
	}
	if !strings.Contains(err.Error(), "unsupported v1 kind") {
		t.Errorf("err = %v, want it to mention 'unsupported v1 kind'", err)
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

func TestLoadConfigMap(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cm, ok := bundle.ConfigMaps["app-config"]
	if !ok {
		t.Fatalf("missing app-config ConfigMap; got %v", bundle.ConfigMaps)
	}
	if got := cm["greeting"]; string(got) != "hello-from-yaml" {
		t.Errorf("greeting = %q, want hello-from-yaml", string(got))
	}
	if got := cm["lang"]; string(got) != "en" {
		t.Errorf("lang = %q, want en", string(got))
	}
}

func TestRejectsConfigMapBinaryData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
binaryData:
  k: aGVsbG8=
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "binaryData") {
		t.Fatalf("err = %v, want binaryData error", err)
	}
}

func TestRejectsDuplicateConfigMap(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v1}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: c}
data: {k: v2}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate ConfigMap") {
		t.Fatalf("err = %v, want duplicate-ConfigMap error", err)
	}
}

func TestRejectsConfigMapMissingName(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: ConfigMap
data: {k: v}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatalf("err = %v, want metadata.name error", err)
	}
}

func TestLoadSecretData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
type: Opaque
data:
  token: aGVsbG8=
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["token"]; string(got) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", string(got))
	}
}

func TestLoadSecretStringData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData:
  raw: hello-plain
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sec, ok := b.Secrets["s"]
	if !ok {
		t.Fatalf("missing secret; got %v", b.Secrets)
	}
	if got := sec["raw"]; string(got) != "hello-plain" {
		t.Errorf("raw = %q, want hello-plain", string(got))
	}
}

func TestLoadSecretStringDataWinsOverData(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: dmFsLWZyb20tZGF0YQ==
stringData:
  k: val-from-stringData
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := b.Secrets["s"]["k"]; string(got) != "val-from-stringData" {
		t.Errorf("k = %q, want val-from-stringData (stringData wins)", string(got))
	}
}

func TestRejectsSecretMalformedBase64(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
data:
  k: "!!!not-base64!!!"
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("err = %v, want base64 decode error", err)
	}
}

func TestRejectsDuplicateSecret(t *testing.T) {
	yaml := []byte(`
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: a}
---
apiVersion: v1
kind: Secret
metadata: {name: s}
stringData: {k: b}
`)
	_, err := loader.LoadBytes(yaml)
	if err == nil || !strings.Contains(err.Error(), "duplicate Secret") {
		t.Fatalf("err = %v, want duplicate-Secret error", err)
	}
}

func TestLoadConfigMapAndSecretFromFile(t *testing.T) {
	bundle, err := loader.LoadFiles([]string{filepath.Join("testdata", "cm-and-secret.yaml")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := bundle.ConfigMaps["app-config"]; !ok {
		t.Errorf("missing app-config CM; got %v", bundle.ConfigMaps)
	}
	sec, ok := bundle.Secrets["app-secret"]
	if !ok {
		t.Fatalf("missing app-secret; got %v", bundle.Secrets)
	}
	if string(sec["token"]) != "hello" {
		t.Errorf("token = %q, want hello (base64-decoded)", sec["token"])
	}
	if string(sec["raw-token"]) != "hello-plain" {
		t.Errorf("raw-token = %q, want hello-plain", sec["raw-token"])
	}
}

// TestLoadPipelineWithResolverTaskRef confirms the loader populates
// the new TaskRef.Resolver / TaskRef.ResolverParams fields for a
// pipelineTask whose taskRef carries a resolver block.
func TestLoadPipelineWithResolverTaskRef(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef:
        resolver: git
        params:
          - {name: url, value: https://github.com/x/y}
          - {name: revision, value: main}
          - {name: pathInRepo, value: task.yaml}
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pl, ok := b.Pipelines["p"]
	if !ok {
		t.Fatal("pipeline p not loaded")
	}
	if len(pl.Spec.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(pl.Spec.Tasks))
	}
	pt := pl.Spec.Tasks[0]
	if pt.TaskRef == nil {
		t.Fatal("taskRef nil")
	}
	if pt.TaskRef.Resolver != "git" {
		t.Errorf("resolver = %q, want git", pt.TaskRef.Resolver)
	}
	if len(pt.TaskRef.ResolverParams) != 3 {
		t.Errorf("resolver params = %d, want 3", len(pt.TaskRef.ResolverParams))
	}
}

// TestHasUnresolvedRefs lists the resolver-backed taskRefs for diagnostic
// surfacing (used by validate -o json and --offline pre-flight).
func TestHasUnresolvedRefs(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {resolver: git, params: [{name: url, value: u}]}
    - name: lint
      taskRef: {name: lint-task}
  finally:
    - name: notify
      taskRef: {resolver: hub, params: [{name: name, value: notify}]}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: lint-task}
spec:
  steps:
    - {name: s, image: alpine, script: echo}
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := loader.HasUnresolvedRefs(b)
	if len(got) != 2 {
		t.Fatalf("unresolved = %d, want 2 (got %+v)", len(got), got)
	}
	// We don't pin order; just assert the two expected entries are present.
	resolvers := map[string]string{}
	for _, u := range got {
		resolvers[u.PipelineTask] = u.Resolver
	}
	if resolvers["build"] != "git" {
		t.Errorf("build resolver = %q, want git", resolvers["build"])
	}
	if resolvers["notify"] != "hub" {
		t.Errorf("notify resolver = %q, want hub", resolvers["notify"])
	}
}

// TestHasUnresolvedRefsTopLevelPipelineRef covers a PipelineRun whose
// top-level pipelineRef carries a resolver block. The diagnostic uses
// PipelineTask = "" to indicate "the top-level Pipeline reference".
func TestHasUnresolvedRefsTopLevelPipelineRef(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata: {name: my-run}
spec:
  pipelineRef:
    resolver: git
    params:
      - {name: url, value: u}
      - {name: pathInRepo, value: pipeline.yaml}
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := loader.HasUnresolvedRefs(b)
	if len(got) != 1 {
		t.Fatalf("unresolved = %d, want 1 (got %+v)", len(got), got)
	}
	if got[0].Kind != "Pipeline" {
		t.Errorf("kind = %q, want Pipeline", got[0].Kind)
	}
	if got[0].Resolver != "git" {
		t.Errorf("resolver = %q, want git", got[0].Resolver)
	}
	if got[0].PipelineTask != "" {
		t.Errorf("expected empty PipelineTask for top-level ref, got %q", got[0].PipelineTask)
	}
}

func TestLoadStepAction(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  image: alpine:3
  script: 'echo hi'
`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := b.StepActions["greet"]
	if !ok {
		t.Fatalf("StepAction not loaded; bundle = %+v", b.StepActions)
	}
	if got.Spec.Image != "alpine:3" {
		t.Errorf("image = %q", got.Spec.Image)
	}
}

func TestLoadStepActionDuplicate(t *testing.T) {
	_, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'a'}
---
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'b'}
`))
	if err == nil {
		t.Fatal("want duplicate error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate StepAction") {
		t.Errorf("err = %v, want duplicate-StepAction", err)
	}
}

func TestLoadV1Beta1UnknownKind(t *testing.T) {
	_, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: SomethingElse
metadata: {name: x}
spec: {}
`))
	if err == nil {
		t.Fatal("want unsupported-kind error, got nil")
	}
	if !strings.Contains(err.Error(), "v1beta1") {
		t.Errorf("err = %v, want unsupported-v1beta1", err)
	}
}

func TestLoadStepActionEmptyName(t *testing.T) {
	_, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {}
spec: {image: alpine:3, script: 'a'}
`))
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Errorf("want metadata-name-required error, got %v", err)
	}
}

func TestLoadStepActionMalformed(t *testing.T) {
	_, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: bad}
spec: 12345
`))
	if err == nil {
		t.Fatal("want StepAction-parse error, got nil")
	}
}

// LoadFiles reads from disk; exercise the success + duplicate-across-files
// paths in one shot. A duplicate StepAction across two files must error
// with "duplicate StepAction across files".
func TestLoadFilesAcrossDuplicateStepAction(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	if err := writeFile(a, `apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'a'}
`); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(dir, "b.yaml")
	if err := writeFile(b, `apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'b'}
`); err != nil {
		t.Fatal(err)
	}
	_, err := loader.LoadFiles([]string{a, b})
	if err == nil || !strings.Contains(err.Error(), "duplicate StepAction") {
		t.Errorf("want cross-file duplicate StepAction error, got %v", err)
	}
}

// Across multiple files a StepAction without a duplicate is merged in.
func TestLoadFilesMergesStepAction(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	if err := writeFile(a, `apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'a'}
`); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(dir, "b.yaml")
	if err := writeFile(b, `apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: greet}}
`); err != nil {
		t.Fatal(err)
	}
	bun, err := loader.LoadFiles([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bun.StepActions["greet"]; !ok {
		t.Errorf("StepAction not merged: %+v", bun.StepActions)
	}
	if _, ok := bun.Tasks["t"]; !ok {
		t.Errorf("Task not merged: %+v", bun.Tasks)
	}
	// The Task's raw YAML must round-trip into RawTasks for rule 17.
	if _, ok := bun.RawTasks["t"]; !ok {
		t.Errorf("RawTasks[t] missing: %+v", bun.RawTasks)
	}
}

// writeFile is a small helper used by the LoadFiles tests above; mirrors
// os.WriteFile but keeps the test imports minimal.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
