# `Task.spec.stepTemplate` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `Task.spec.stepTemplate` — a per-Task base template whose `image`, `command`, `args`, `workingDir`, `env`, and `imagePullPolicy` are inherited by every Step in the Task that doesn't set its own — on both the docker and cluster backends.

**Architecture:** Pure type addition + a single merge pass. We add `tektontypes.StepTemplate` and `TaskSpec.StepTemplate`, then add an `applyStepTemplate` step to the engine's resolver pipeline that merges the template into each Step *before* `substituteSpec` runs. Step-level fields always win; `env` is merged by name (Step value wins on conflict). Cluster backend is untouched at the inlining layer because `taskSpecToMap` just `json.Marshal`s the new field and Tekton's `EmbeddedTask` natively accepts `stepTemplate` — no field-level renaming needed.

**Tech Stack:** Go 1.25, no new dependencies. Reuses `internal/engine`, `internal/tektontypes`, `internal/validator`, the cross-backend `internal/e2e/fixtures` table, and the existing parity-check + tests-required CI gates.

---

## Track 1 #4 context

This closes Track 1 #4 of `docs/short-term-goals.md`. The Status column says: *"Not started. Pure type + merge logic at substitution time; small."*

## Tekton-upstream behavior we're matching

Tekton's `TaskSpec.StepTemplate` (v1) is a partial Step the controller merges into every Step in `TaskSpec.Steps` before scheduling pods. Concretely:

```yaml
spec:
  stepTemplate:
    image: alpine:3
    env:
      - name: SHARED
        value: hello
  steps:
    - name: a
      script: echo "$SHARED from a"        # inherits image=alpine:3
    - name: b
      image: alpine:3.20                   # overrides
      script: echo "$SHARED from b"
    - name: c
      env:
        - name: SHARED
          value: overridden                # Step env wins by name
        - name: EXTRA
          value: also
      script: echo "$SHARED $EXTRA"
```

Merge rules (per upstream, simplified to the fields tkn-act actually reads):

- **Scalar fields** (`image`, `workingDir`, `imagePullPolicy`): Step value wins if non-empty; otherwise inherit from `stepTemplate`.
- **Slice fields** (`command`, `args`): Step value wins if non-empty (whole slice replaces template; Tekton does NOT element-wise merge these).
- **Env**: union by `Name`; Step entry wins on conflict. Order: template entries first (in template order), then Step-only entries (in Step order).
- **Resources**: Step value wins (replace); we don't deep-merge requests/limits maps. Scoped to "if Step.Resources is nil, copy template's; else keep Step's."
- **`name`, `script`, `volumeMounts`, `results`, `onError`**: NOT inheritable per upstream — these are intrinsically per-Step.

Out of scope (don't infer from `stepTemplate`): `securityContext`, `lifecycle`, `livenessProbe`, etc. tkn-act doesn't read those today.

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `StepTemplate` struct + `StepTemplate *StepTemplate` on `TaskSpec` |
| `internal/tektontypes/types_test.go` | YAML round-trip for `stepTemplate` |
| `internal/engine/step_template.go` (new) | `applyStepTemplate(TaskSpec) TaskSpec` merge function |
| `internal/engine/step_template_test.go` (new) | Unit tests for every merge rule |
| `internal/engine/engine.go` | Call `applyStepTemplate` between `lookupTaskSpec` and `substituteSpec` (one-line plumbing); also feed `uniqueImages` from the merged view |
| `internal/engine/engine_test.go` (or new file) | Integration test that runs a Task whose Steps inherit image/env from stepTemplate |
| `internal/validator/validator.go` | If `stepTemplate.image` is empty AND any Step has empty image AND there's no template, error (existing rule) — extend to consult template |
| `internal/validator/validator_test.go` | Step-without-image-but-template-supplies-it must validate |
| `internal/backend/cluster/run.go` | No code change needed — `taskSpecToMap` already round-trips unknown fields, and Tekton's `EmbeddedTask` accepts `stepTemplate` natively |
| `internal/backend/cluster/runpipeline_test.go` | Add a unit test asserting `pipelineSpec.tasks[].taskSpec.stepTemplate` survives the inline pass intact |
| `internal/e2e/fixtures/fixtures.go` | Add a `step-template` fixture entry |
| `testdata/e2e/step-template/pipeline.yaml` | New cross-backend fixture exercising image, env, and override |
| `cmd/tkn-act/agentguide_data.md` | Note that `stepTemplate` is honored |
| `AGENTS.md` | Mirror agentguide_data.md |
| `docs/test-coverage.md` | List the new fixture |
| `docs/short-term-goals.md` | Mark Track 1 #4 done |
| `docs/feature-parity.md` | Flip the `Task.stepTemplate` row from `gap` → `shipped`, populate `e2e fixture` |

## Out of scope (don't do here)

- Sidecars (Track 1 #1, separate spec).
- `displayName` / `description` (Track 1 #7) — they don't intersect with merge logic and have their own row.
- StepActions (Track 1 #8) — `Step.ref` resolution is a separate, larger problem.
- `securityContext` / `lifecycle` / probes — tkn-act doesn't read those today; adding them is broader than this plan.
- Element-wise merging of `command` / `args` (Tekton itself doesn't do this).
- Volume / volumeMount inheritance from `stepTemplate` — Tekton allows it, but tkn-act's volume model is per-Step and adding inheritance touches the volume resolver. Out of scope; document as a gap.

---

### Task 1: Add `StepTemplate` to the type model

**Files:**
- Modify: `internal/tektontypes/types.go` (TaskSpec around line 36-46)
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tektontypes/types_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestUnmarshalTaskWith ./internal/tektontypes/...
```

Expected: FAIL with `Spec.StepTemplate undefined`.

- [ ] **Step 3: Add the type and field**

In `internal/tektontypes/types.go`, find the `TaskSpec` struct and add the field after `Volumes`:

```go
type TaskSpec struct {
	Params      []ParamSpec     `json:"params,omitempty"`
	Results     []ResultSpec    `json:"results,omitempty"`
	Workspaces  []WorkspaceDecl `json:"workspaces,omitempty"`
	Steps       []Step          `json:"steps"`
	Description string          `json:"description,omitempty"`
	// Timeout is a Go duration string (e.g. "30s", "5m"). Empty means no
	// task-level timeout.
	Timeout      string        `json:"timeout,omitempty"`
	Volumes      []Volume      `json:"volumes,omitempty"`
	StepTemplate *StepTemplate `json:"stepTemplate,omitempty"`
}
```

Then append a new struct after the `Step` struct (right before `// Volume is a Task-level volume.`):

```go
// StepTemplate is the partial-Step template merged into every Step in
// TaskSpec.Steps. Fields are inherited only when the Step doesn't set
// its own. Mirrors Tekton's StepTemplate (v1) for the subset of Step
// fields tkn-act reads. `name`, `script`, `volumeMounts`, `results`,
// and `onError` are NOT inheritable — they're intrinsically per-Step.
type StepTemplate struct {
	Image           string         `json:"image,omitempty"`
	Command         []string       `json:"command,omitempty"`
	Args            []string       `json:"args,omitempty"`
	Env             []EnvVar       `json:"env,omitempty"`
	WorkingDir      string         `json:"workingDir,omitempty"`
	Resources       *StepResources `json:"resources,omitempty"`
	ImagePullPolicy string         `json:"imagePullPolicy,omitempty"`
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestUnmarshalTaskWith ./internal/tektontypes/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add TaskSpec.StepTemplate (Tekton v1)"
```

---

### Task 2: Implement the merge function

**Files:**
- Create: `internal/engine/step_template.go`
- Test: `internal/engine/step_template_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/engine/step_template_test.go`:

```go
package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestApplyStepTemplateNil(t *testing.T) {
	spec := tektontypes.TaskSpec{
		Steps: []tektontypes.Step{{Name: "a", Image: "alpine:3"}},
	}
	got := applyStepTemplate(spec)
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("nil template should be a no-op, got %+v", got)
	}
}

func TestApplyStepTemplateInheritsImage(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{Image: "alpine:3"},
		Steps: []tektontypes.Step{
			{Name: "a"},                  // no image -> inherits
			{Name: "b", Image: "busybox"}, // overrides
		},
	}
	got := applyStepTemplate(spec)
	if got.Steps[0].Image != "alpine:3" {
		t.Errorf("step a image = %q, want alpine:3 (inherited)", got.Steps[0].Image)
	}
	if got.Steps[1].Image != "busybox" {
		t.Errorf("step b image = %q, want busybox (override)", got.Steps[1].Image)
	}
}

func TestApplyStepTemplateInheritsScalars(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Image:           "alpine:3",
			WorkingDir:      "/work",
			ImagePullPolicy: "IfNotPresent",
		},
		Steps: []tektontypes.Step{{Name: "a"}},
	}
	got := applyStepTemplate(spec).Steps[0]
	if got.Image != "alpine:3" || got.WorkingDir != "/work" || got.ImagePullPolicy != "IfNotPresent" {
		t.Errorf("step = %+v, want all three inherited", got)
	}
}

func TestApplyStepTemplateInheritsCommandArgsWhenStepEmpty(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{"echo hi"},
		},
		Steps: []tektontypes.Step{
			{Name: "a"},
			{Name: "b", Command: []string{"/bin/bash"}},
			{Name: "c", Args: []string{"echo bye"}},
		},
	}
	got := applyStepTemplate(spec).Steps
	if !reflect.DeepEqual(got[0].Command, []string{"/bin/sh", "-c"}) || !reflect.DeepEqual(got[0].Args, []string{"echo hi"}) {
		t.Errorf("a: command/args = %+v / %+v", got[0].Command, got[0].Args)
	}
	if !reflect.DeepEqual(got[1].Command, []string{"/bin/bash"}) {
		t.Errorf("b command = %+v, want override", got[1].Command)
	}
	if !reflect.DeepEqual(got[1].Args, []string{"echo hi"}) {
		t.Errorf("b args = %+v, want inherited (only Command was set)", got[1].Args)
	}
	if !reflect.DeepEqual(got[2].Args, []string{"echo bye"}) {
		t.Errorf("c args = %+v, want override", got[2].Args)
	}
}

func TestApplyStepTemplateMergesEnvByName(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Env: []tektontypes.EnvVar{
				{Name: "A", Value: "from-template"},
				{Name: "B", Value: "from-template"},
			},
		},
		Steps: []tektontypes.Step{
			{
				Name: "a",
				Env: []tektontypes.EnvVar{
					{Name: "B", Value: "from-step"}, // wins by name
					{Name: "C", Value: "step-only"},
				},
			},
		},
	}
	got := applyStepTemplate(spec).Steps[0].Env
	want := []tektontypes.EnvVar{
		{Name: "A", Value: "from-template"},
		{Name: "B", Value: "from-step"},
		{Name: "C", Value: "step-only"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %+v, want %+v", got, want)
	}
}

func TestApplyStepTemplateInheritsResources(t *testing.T) {
	spec := tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Resources: &tektontypes.StepResources{
				Limits: tektontypes.ResourceList{Memory: "128Mi"},
			},
		},
		Steps: []tektontypes.Step{
			{Name: "a"},
			{Name: "b", Resources: &tektontypes.StepResources{Limits: tektontypes.ResourceList{Memory: "256Mi"}}},
		},
	}
	got := applyStepTemplate(spec).Steps
	if got[0].Resources == nil || got[0].Resources.Limits.Memory != "128Mi" {
		t.Errorf("a resources = %+v, want inherited 128Mi", got[0].Resources)
	}
	if got[1].Resources.Limits.Memory != "256Mi" {
		t.Errorf("b resources = %+v, want override 256Mi", got[1].Resources)
	}
}

func TestApplyStepTemplateDoesNotMutateInput(t *testing.T) {
	tmpl := &tektontypes.StepTemplate{Env: []tektontypes.EnvVar{{Name: "A", Value: "1"}}}
	spec := tektontypes.TaskSpec{
		StepTemplate: tmpl,
		Steps:        []tektontypes.Step{{Name: "a", Env: []tektontypes.EnvVar{{Name: "B", Value: "2"}}}},
	}
	_ = applyStepTemplate(spec)
	if len(tmpl.Env) != 1 || tmpl.Env[0].Name != "A" {
		t.Errorf("template env mutated: %+v", tmpl.Env)
	}
	if len(spec.Steps[0].Env) != 1 || spec.Steps[0].Env[0].Name != "B" {
		t.Errorf("step env mutated: %+v", spec.Steps[0].Env)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run TestApplyStepTemplate ./internal/engine/...
```

Expected: FAIL with `applyStepTemplate undefined`.

- [ ] **Step 3: Implement the merge function**

Create `internal/engine/step_template.go`:

```go
package engine

import "github.com/danielfbm/tkn-act/internal/tektontypes"

// applyStepTemplate returns a new TaskSpec where each Step has fields
// inherited from spec.StepTemplate when the Step itself didn't set
// them. Returns spec unchanged when StepTemplate is nil. Never
// mutates the input — copies slices before merging.
//
// Merge rules (mirror Tekton v1):
//   - Scalars (image, workingDir, imagePullPolicy): Step value wins if non-empty
//   - Slices (command, args): Step value wins as a whole if non-empty;
//     no element-wise merge
//   - Env: union by Name; Step entry wins; order is template-then-step-only
//   - Resources: Step value wins (replace); no deep merge of limits/requests
//
// Fields that are intrinsically per-Step (Name, Script, VolumeMounts,
// Results, OnError) are not touched.
func applyStepTemplate(spec tektontypes.TaskSpec) tektontypes.TaskSpec {
	if spec.StepTemplate == nil {
		return spec
	}
	t := spec.StepTemplate
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		ns := st
		if ns.Image == "" {
			ns.Image = t.Image
		}
		if ns.WorkingDir == "" {
			ns.WorkingDir = t.WorkingDir
		}
		if ns.ImagePullPolicy == "" {
			ns.ImagePullPolicy = t.ImagePullPolicy
		}
		if len(ns.Command) == 0 && len(t.Command) > 0 {
			ns.Command = append([]string(nil), t.Command...)
		}
		if len(ns.Args) == 0 && len(t.Args) > 0 {
			ns.Args = append([]string(nil), t.Args...)
		}
		if ns.Resources == nil && t.Resources != nil {
			r := *t.Resources
			ns.Resources = &r
		}
		ns.Env = mergeEnv(t.Env, st.Env)
		out.Steps[i] = ns
	}
	return out
}

// mergeEnv unions tmpl and step envs by Name. Step entries override
// template entries with the same name. Order: every template entry in
// template order (with the value swapped if the step overrode it),
// followed by step-only entries in step order.
func mergeEnv(tmpl, step []tektontypes.EnvVar) []tektontypes.EnvVar {
	if len(tmpl) == 0 && len(step) == 0 {
		return nil
	}
	stepIdx := map[string]int{}
	for i, e := range step {
		stepIdx[e.Name] = i
	}
	out := make([]tektontypes.EnvVar, 0, len(tmpl)+len(step))
	emitted := map[string]bool{}
	for _, e := range tmpl {
		if i, ok := stepIdx[e.Name]; ok {
			out = append(out, step[i])
		} else {
			out = append(out, e)
		}
		emitted[e.Name] = true
	}
	for _, e := range step {
		if !emitted[e.Name] {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestApplyStepTemplate ./internal/engine/...
```

Expected: PASS (all 7).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/step_template.go internal/engine/step_template_test.go
git commit -m "feat(engine): applyStepTemplate merges StepTemplate into each Step"
```

---

### Task 3: Wire the merge into runOne and uniqueImages

**Files:**
- Modify: `internal/engine/engine.go` (around line 312 where `substituteSpec` is called, and around line 426 where `uniqueImages` is computed)
- Test: new file `internal/engine/step_template_engine_test.go` (uses `engine_test` package and reuses `recBackend` / `sliceSink` from `policy_test.go`)

- [ ] **Step 1: Write the failing test**

Create `internal/engine/step_template_engine_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

// captureBackend records the resolved Step every time RunTask fires,
// so tests can assert that StepTemplate inheritance happened *before*
// the backend got the spec.
type captureBackend struct {
	steps map[string][]backend.TaskInvocation
}

func (c *captureBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (c *captureBackend) Cleanup(_ context.Context) error                    { return nil }
func (c *captureBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if c.steps == nil {
		c.steps = map[string][]backend.TaskInvocation{}
	}
	c.steps[inv.TaskName] = append(c.steps[inv.TaskName], inv)
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

func TestStepTemplateAppliedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate:
    image: alpine:3
    env:
      - {name: SHARED, value: hello}
  steps:
    - {name: a, script: 'true'}
    - {name: b, image: busybox, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: t, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &captureBackend{}
	sink := &sliceSink{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	inv := be.steps["t"][0]
	if got := inv.Task.Steps[0].Image; got != "alpine:3" {
		t.Errorf("step a image = %q, want alpine:3 (inherited)", got)
	}
	if got := inv.Task.Steps[1].Image; got != "busybox" {
		t.Errorf("step b image = %q, want busybox (override)", got)
	}
	if len(inv.Task.Steps[0].Env) != 1 || inv.Task.Steps[0].Env[0].Value != "hello" {
		t.Errorf("step a env = %+v, want SHARED=hello inherited", inv.Task.Steps[0].Env)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -count=1 -run TestStepTemplateAppliedBeforeBackend ./internal/engine/...
```

Expected: FAIL — step a's image is empty (nothing was inherited yet).

- [ ] **Step 3: Wire `applyStepTemplate` into runOne**

In `internal/engine/engine.go`, find this block (around line 257-263):

```go
	// Resolve task spec.
	spec, err := lookupTaskSpec(in.Bundle, pt)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
```

Replace with:

```go
	// Resolve task spec, then merge StepTemplate into each Step
	// before any further substitution / validation runs.
	spec, err := lookupTaskSpec(in.Bundle, pt)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	spec = applyStepTemplate(spec)
```

- [ ] **Step 4: Wire `applyStepTemplate` into uniqueImages**

In `internal/engine/engine.go`, find `uniqueImages` (around line 426). Update the inner loop to merge the template before reading step images:

```go
func uniqueImages(b *loader.Bundle, pl tektontypes.Pipeline) []string {
	seen := map[string]struct{}{}
	for _, pt := range append(append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...), pl.Spec.Finally...) {
		var spec tektontypes.TaskSpec
		if pt.TaskRef != nil {
			if t, ok := b.Tasks[pt.TaskRef.Name]; ok {
				spec = t.Spec
			}
		} else if pt.TaskSpec != nil {
			spec = *pt.TaskSpec
		}
		// Merge so that steps inheriting an image from stepTemplate
		// are pre-pulled too.
		spec = applyStepTemplate(spec)
		for _, s := range spec.Steps {
			seen[s.Image] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for img := range seen {
		out = append(out, img)
	}
	return out
}
```

- [ ] **Step 5: Run tests**

```bash
go test -count=1 -race -run TestStepTemplate ./internal/engine/...
```

Expected: PASS.

- [ ] **Step 6: Run the full engine suite to confirm no regressions**

```bash
go test -race -count=1 ./internal/engine/...
```

Expected: every test OK — including `TestEngine*` (existing engine tests) and the v1.3 `TestPipelineLevelTimeoutTriggers` etc.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/engine.go internal/engine/step_template_engine_test.go
git commit -m "feat(engine): apply StepTemplate before substitution and image pre-pull"
```

---

### Task 4: Validator accepts step-without-image when stepTemplate.image is set

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

Current validator behavior is permissive about empty `Step.Image` — looking at `validator.go`, there's no explicit check that rejects an empty image. Verify; if there is such a rule in any of the 11 numbered checks, this task adapts it. If not, this task is a *negative* test only: assert that a Step with no image plus a `stepTemplate.image` validates clean.

- [ ] **Step 1: Verify whether validator currently rejects empty Step.Image**

```bash
grep -n 'Image\b' internal/validator/validator.go
```

Expected: no rule references `Step.Image`. If any rule rejects empty image, surface it in the next step; otherwise, only the additional positive test below is needed.

- [ ] **Step 2: Write the test**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateStepTemplateSuppliesImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate:
    image: alpine:3
  steps:
    - {name: s, script: "true"}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test -count=1 ./internal/validator/...
```

Expected: PASS. If FAIL with an error mentioning empty image, the validator currently rejects empty Step.Image; in that case, locate the rule and skip it when `spec.StepTemplate != nil && spec.StepTemplate.Image != ""`. Re-run.

- [ ] **Step 4: Commit**

```bash
git add internal/validator/validator_test.go internal/validator/validator.go
git commit -m "test(validator): step-without-image is valid when stepTemplate supplies it"
```

(If `internal/validator/validator.go` was unchanged, drop it from the `git add`.)

---

### Task 5: Cluster backend round-trips `stepTemplate` intact

**Files:**
- Test: `internal/backend/cluster/runpipeline_test.go`

The existing `taskSpecToMap` is a `json.Marshal` round-trip, so `stepTemplate` will pass through with no code change. We add a regression test to lock that in: if a future hand-written conversion is added, this test catches a drop.

- [ ] **Step 1: Write the test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunInlinesStepTemplate: when a referenced Task has
// stepTemplate, the cluster backend must inline it under
// pipelineSpec.tasks[].taskSpec.stepTemplate intact (Tekton's
// EmbeddedTask schema accepts stepTemplate natively).
func TestBuildPipelineRunInlinesStepTemplate(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Image: "alpine:3",
			Env:   []tektontypes.EnvVar{{Name: "SHARED", Value: "hello"}},
		},
		Steps: []tektontypes.Step{{Name: "s", Script: "true"}},
	}}
	tk.Metadata.Name = "t"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)

	st, found, err := unstructured.NestedMap(un.Object, "spec", "pipelineSpec", "tasks", "[0]", "taskSpec", "stepTemplate")
	// `[0]` syntax isn't supported by NestedMap; do it via NestedSlice.
	tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks slice = %d, want 1", len(tasks))
	}
	taskMap, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("tasks[0] not a map: %T", tasks[0])
	}
	taskSpec, ok := taskMap["taskSpec"].(map[string]any)
	if !ok {
		t.Fatalf("taskSpec missing under inlined task")
	}
	st, ok = taskSpec["stepTemplate"].(map[string]any)
	if !ok {
		t.Fatalf("stepTemplate missing on inlined taskSpec; got: %v / found=%v err=%v", taskSpec, found, err)
	}
	if got := st["image"]; got != "alpine:3" {
		t.Errorf("stepTemplate.image = %v, want alpine:3", got)
	}
}
```

- [ ] **Step 2: Run the test**

```bash
go test -count=1 -run TestBuildPipelineRunInlinesStepTemplate ./internal/backend/cluster/...
```

Expected: PASS (no production change required — `taskSpecToMap` already round-trips via `json.Marshal`/`json.Unmarshal`).

- [ ] **Step 3: Commit**

```bash
git add internal/backend/cluster/runpipeline_test.go
git commit -m "test(cluster): assert StepTemplate survives inlining into PipelineRun"
```

---

### Task 6: Cross-backend e2e fixture

**Files:**
- Create: `testdata/e2e/step-template/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/step-template/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: tmpl
spec:
  stepTemplate:
    image: alpine:3
    env:
      - name: SHARED
        value: from-template
  steps:
    - name: inherits-image-and-env
      script: |
        echo "image=$0; SHARED=$SHARED"
        test "$SHARED" = "from-template"
    - name: overrides-env
      env:
        - name: SHARED
          value: from-step
      script: |
        echo "image=$0; SHARED=$SHARED"
        test "$SHARED" = "from-step"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: step-template
spec:
  tasks:
    - name: t
      taskRef: { name: tmpl }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, inside the `All()` table, add this entry just after the last v1.3 timeout fixture:

```go
		{Dir: "step-template", Pipeline: "step-template", WantStatus: "succeeded"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/step-template -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/step-template/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): step-template fixture (cross-backend)"
```

---

### Task 7: Documentation convergence

**Files:**
- Modify: `cmd/tkn-act/agentguide_data.md`
- Modify: `AGENTS.md`
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`

- [ ] **Step 1: Note `stepTemplate` in `AGENTS.md`**

In `AGENTS.md`, find the existing "Timeout disambiguation" section. Right above it (after the failure-modes table and the merge-default block, but still before the existing first `## ...` after that), insert a new section:

```markdown
## `stepTemplate` (DRY for Steps)

`Task.spec.stepTemplate` lets a Task declare base values that every
Step in `spec.steps` inherits. Inheritance rules tkn-act follows:

| Field | Behavior |
|---|---|
| `image`, `workingDir`, `imagePullPolicy` | Step value wins if non-empty; otherwise inherit. |
| `command`, `args` | Step value wins as a whole if non-empty (no element-wise merge). |
| `env` | Union by `name`; Step entry overrides template entry with the same name. |
| `resources` | Step value wins (replace); no deep merge of `limits` / `requests`. |
| `name`, `script`, `volumeMounts`, `results`, `onError` | Per-Step only; never inherited. |

This matches Tekton v1 semantics for the subset of Step fields
tkn-act reads.

---

```

- [ ] **Step 2: Mirror `AGENTS.md` into `cmd/tkn-act/agentguide_data.md`**

```bash
go generate ./cmd/tkn-act/
diff cmd/tkn-act/agentguide_data.md AGENTS.md
```

Expected: no diff.

- [ ] **Step 3: Run the embedded-guide test**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: PASS.

- [ ] **Step 4: Update `docs/test-coverage.md`**

In `docs/test-coverage.md`, find the `### -tags integration` table. Insert a row right after `finally-timeout/`:

```markdown
| `step-template/`    | `Task.spec.stepTemplate` inheritance: image + env, with one Step overriding env |
```

- [ ] **Step 5: Mark Track 1 #4 done in `docs/short-term-goals.md`**

In the Track 1 table, change the row 4 Status cell from:

```
| Not started. Pure type + merge logic at substitution time; small. |
```

to:

```
| Done in v1.4 (PR for `feat: TaskSpec.StepTemplate`). Engine-side merge before substitution; cluster pass-through verified. |
```

- [ ] **Step 6: Flip the `feature-parity.md` row**

In `docs/feature-parity.md` under `### Task structure`, change:

```
| `Task.stepTemplate` | `TaskSpec.stepTemplate` | gap | both | none | none | docs/short-term-goals.md (Track 1 #4) |
```

to:

```
| `Task.stepTemplate` | `TaskSpec.stepTemplate` | shipped | both | step-template | none | docs/superpowers/plans/2026-05-03-step-template.md (Track 1 #4) |
```

- [ ] **Step 7: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 8: Commit**

```bash
git add cmd/tkn-act/agentguide_data.md AGENTS.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md
git commit -m "docs: document stepTemplate; flip Track 1 #4 to shipped"
```

---

### Task 8: Final verification and PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all exit 0, every test package OK or no-test-files.

- [ ] **Step 2: Push branch and open PR**

```bash
git push -u origin feat/step-template
gh pr create --title "feat: honor Task.spec.stepTemplate (Track 1 #4)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #4 of `docs/short-term-goals.md`. Honors Tekton's
`Task.spec.stepTemplate` on both backends:

- New `tektontypes.StepTemplate` type and `TaskSpec.StepTemplate`
  field. YAML round-trip tested.
- Engine merges the template into each Step *before*
  `substituteSpec`, so all later passes (param/result substitution,
  per-step env, image pre-pull) see the resolved Step.
- Merge rules: scalar Step value wins; slice (`command`, `args`)
  Step value wins as a whole; `env` is unioned by name with Step
  override; `resources` Step value wins on replace.
- Cluster backend round-trips `stepTemplate` intact via the existing
  JSON marshal path; Tekton's EmbeddedTask accepts the field
  natively. Regression test asserts that.
- One cross-backend fixture (`step-template`) exercises image
  inheritance + env override.

Implements `docs/superpowers/plans/2026-05-03-step-template.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] tests-required script
- [ ] docker-integration CI — runs the new fixture
- [ ] cluster-integration CI — same fixture on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Type model → Task 1. Merge function with rules for every Tekton-spec'd inheritance case → Task 2. Engine wiring (substitution + image pre-pull) → Task 3. Validator + cluster invariants → Tasks 4 + 5. Cross-backend fidelity → Task 6 (fixture lands in `fixtures.All()`, both harnesses iterate). Docs convergence → Task 7. Final ship → Task 8.
- **No placeholders:** every step has actual code or commands. Task 4 has a conditional (`If validator currently rejects empty image`) but with explicit fallback action; not a deferral.
- **Type consistency:** `applyStepTemplate(TaskSpec) TaskSpec` and `mergeEnv(tmpl, step []EnvVar) []EnvVar` are the only new package-internal symbols; both used uniformly across Tasks 2 and 3.
- **One-task non-regression:** Task 3 step 6 explicitly runs the full engine test suite (`TestEngine*`, `TestPipelineLevelTimeoutTriggers`, `TestApplyStepTemplate*`) so the merge insertion can't silently break the v1.3 timeout work or the existing per-task policy loop.
- **Cluster pass-through "free":** `taskSpecToMap` is a `json.Marshal` round-trip, so the new `StepTemplate` field appears under the inlined `taskSpec` automatically. Task 5 is a regression-locking test, not a new code path.
- **Docs are atomic with the code:** Task 7 lands AGENTS.md ↔ embedded guide convergence, test-coverage.md, short-term-goals.md, *and* the feature-parity row flip in one commit so the parity-check is satisfied at every commit boundary.
