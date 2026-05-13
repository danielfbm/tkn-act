# StepActions (`tekton.dev/v1beta1`) — Implementation Plan

> **Migration note (2026-05-13):** This plan was authored before the
> agent-guide folder split (spec `2026-05-13-agent-guide-folder-design.md`).
> Wherever it says "update `AGENTS.md` / mirror to `agentguide_data.md`",
> read that as: edit the matching file in `docs/agent-guide/` and re-run
> `go generate ./cmd/tkn-act/` (or `make agentguide`) so
> `cmd/tkn-act/agentguide_data/` mirrors it. AGENTS.md is now the
> contributor guide and is no longer mirrored into the binary.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `StepAction` (`apiVersion: tekton.dev/v1beta1`,
`kind: StepAction`) — a referenceable Step shape — on both the docker
and cluster backends, with local-files-only resolution (no resolvers in
v1; those land with Track 1 #9).

**Architecture:** Type addition + a new `resolveStepActions` engine
pass that runs **before** `applyStepTemplate`. Loader gains an arm for
`tekton.dev/v1beta1`. Validator gains four rules (12–15). Cluster
backend is untouched at the inlining layer because expansion is
client-side: `taskSpecToMap` serializes the same expanded
`spec.steps[]` it always has.

**Tech Stack:** Go 1.25, no new dependencies. Reuses
`internal/engine`, `internal/loader`, `internal/tektontypes`,
`internal/validator`, `internal/resolver`, the cross-backend
`internal/e2e/fixtures` table, and the existing `tests-required`,
`coverage`, and `parity-check` CI gates.

---

## Track 1 #8 context

This closes Track 1 #8 of `docs/short-term-goals.md`. The Status column
says: *"Not started. Needs design (resolution + caching)."* Caching is
not needed for v1 (StepActions live in the bundle, rebuilt per run);
resolver-based fetching stays gap-listed under Track 1 #9.

## Tekton-upstream behavior we're matching

See `docs/superpowers/specs/2026-05-04-step-actions-design.md` § 2 for
the full table. Summary of what this plan implements:

- Loader recognizes `apiVersion: tekton.dev/v1beta1`, `kind: StepAction`
  and parses it into `Bundle.StepActions[name]`.
- Engine resolves every `Step.Ref` into a concrete inlined Step
  *before* `applyStepTemplate` (and thus *before* `substituteSpec`).
- StepAction `params` declarations supply names + defaults; calling
  `Step.params` supply values; `$(params.X)` inside the StepAction
  body resolves against this StepAction-scoped param view.
- StepAction `results` are copied onto the resolved Step's `Results`,
  which the existing per-step results dir machinery in the docker
  backend already knows how to handle.
- A Step that sets both `ref:` and any inline body field (image /
  script / etc.) is invalid (validator rule 13).
- A StepAction body cannot itself contain `ref:` (validator rule 15).

## Pre-flight check (zero-cost sanity)

Confirm there is no stale `testdata/limitations/step-actions/`
directory to delete when flipping the parity row from `gap` to
`shipped`:

```bash
ls testdata/limitations/ | grep -F step-actions || echo "no step-actions/ limitations dir; nothing to remove"
```

Expected: the `echo` runs (no match). If a directory does exist,
remove it as part of Task 8's parity flip.

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `StepAction`, `StepActionSpec`, `StepActionRef` types; add `Step.Ref` and `Step.Params` fields; relax `Step.Image` JSON tag from required to `omitempty`. |
| `internal/tektontypes/types_test.go` | YAML round-trips: `kind: StepAction`, Step with `ref:` + `params:`. |
| `internal/loader/loader.go` | Add `tekton.dev/v1beta1` arm + `loadStepAction` helper; `Bundle.StepActions` map + duplicate check; merge across files. |
| `internal/loader/loader_test.go` | (a) StepAction lands in `Bundle.StepActions`; (b) duplicate name across docs → error; (c) unknown v1beta1 kind → error. |
| `internal/resolver/resolver.go` | Widen `SubstituteAllowStepRefs` / `SubstituteArgsAllowStepRefs` to also defer unknown `params.<name>`, `workspaces.<name>.path`, `context.<name>`, and `tasks.<name>.results.<name>` to the outer pass. Plain `Substitute` / `SubstituteArgs` keep strict error semantics. |
| `internal/resolver/step_test.go` | New test (`TestSubstituteAllowStepRefsLeavesEveryOuterScope`) that pins every outer-scope token survives the AllowStepRefs pass and that a known inner `params.X` is still resolved. |
| `internal/engine/step_action.go` (new) | `resolveStepActions(spec TaskSpec, b *loader.Bundle) (TaskSpec, error)` — the new pass. Uses `resolver.SubstituteAllowStepRefs` / `resolver.SubstituteArgsAllowStepRefs` (NOT plain `Substitute`) so outer-scope tokens survive into the outer `substituteSpec` pass. |
| `internal/engine/step_action_test.go` (new) | Unit tests for every resolution / error path, including outer-scope-token survival and caller-forwarded outer-ref-as-literal. |
| `internal/engine/engine.go` | Call `resolveStepActions` between `lookupTaskSpec` and `applyStepTemplate` in `runOne`; same call inside `uniqueImages` so referenced images are pre-pulled. Both call sites must propagate the error as `TaskOutcome{Status:"failed"}` consistent with the existing `lookupTaskSpec` error pattern. |
| `internal/engine/step_action_engine_test.go` (new) | Integration test that runs a Pipeline whose Task uses `ref:` and asserts the captureBackend received the inlined Step (image + script from StepAction; identity + onError from the calling Step). |
| `internal/validator/validator.go` | Rules 12 (unknown ref), 13 (ref+inline mutually exclusive), 14 (required StepAction params bound), 15 (no nested refs — structural via `StepActionSpec` schema, no runtime check), 16 (inline Step requires image after stepTemplate inheritance, paired with `Step.Image omitempty`), 17 (resolver-form `ref:` rejected with clear message), 18 (StepAction ParamSpec.Default array/object rejected). |
| `internal/validator/validator_test.go` | One test per rule + a positive test (valid ref-Step) + Rule 16 positive (image inherited from stepTemplate). |
| `internal/backend/cluster/runpipeline_test.go` | Regression: `BuildPipelineRunObject` for a Task using `ref:` produces an inlined Step and no `ref:` field under `taskSpec.steps[].`. |
| `internal/e2e/fixtures/fixtures.go` | Add `step-actions` fixture entry. |
| `testdata/e2e/step-actions/pipeline.yaml` | New cross-backend fixture exercising param defaults + override + result-via-`$(steps.X.results.Y)`. |
| `cmd/tkn-act/agentguide_data.md` | Add a "StepActions" section mirroring `stepTemplate`'s section. |
| `AGENTS.md` | Same content as agentguide_data.md (kept in sync via `go generate`). |
| `docs/test-coverage.md` | New row under the integration-tag table. |
| `docs/short-term-goals.md` | Mark Track 1 #8 done. |
| `docs/feature-parity.md` | Flip the `StepActions` row from `gap` → `shipped`; populate `e2e fixture`; link to this plan. |

## Out of scope (don't do here)

- **Resolver-based `ref:` (`hub`, `git`, `cluster`, `bundles`).** Track
  1 #9, separate spec.
- **`apiVersion: tekton.dev/v1` form of StepAction.** Add when upstream
  graduates and a real fixture asks for it.
- **Cross-Task / cluster-side StepAction lookup.** The bundle is the
  only source.
- **Calling-side body overrides** (Step adding to StepAction's `env`,
  overriding `image`, etc.). Strict "ref is the body" stance per the
  spec § 9 open question 1; revisit only when a fixture asks.
- **Nested StepActions.** Schema-rejected (rule 15) + not modeled on
  `StepActionSpec`.

---

### Task 1: Add `StepAction`, `StepActionRef`, and `Step.Ref` / `Step.Params` to the type model

**Files:**
- Modify: `internal/tektontypes/types.go`
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tektontypes/types_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — expect FAIL** (`StepAction undefined`, `Step.Ref undefined`).

```bash
go test -run 'TestUnmarshalStepAction|TestUnmarshalStepWithRef' ./internal/tektontypes/...
```

- [ ] **Step 3: Add the types**

In `internal/tektontypes/types.go`, after the existing `StepTemplate`
struct, add:

```go
// StepAction is a referenceable Step shape (apiVersion tekton.dev/v1beta1).
// Lives in the loader bundle alongside Tasks and Pipelines; resolved into
// concrete Steps by the engine before stepTemplate merge / substitution.
type StepAction struct {
	Object `json:",inline"`
	Spec   StepActionSpec `json:"spec"`
}

// StepActionSpec is the body of a StepAction. It overlaps with Step but
// is intentionally a separate type so that fields that don't make sense
// on a referenceable shape (Name, Ref, OnError) are absent.
type StepActionSpec struct {
	Description     string         `json:"description,omitempty"`
	Params          []ParamSpec    `json:"params,omitempty"`
	Results         []ResultSpec   `json:"results,omitempty"`
	Image           string         `json:"image"`
	Command         []string       `json:"command,omitempty"`
	Args            []string       `json:"args,omitempty"`
	Script          string         `json:"script,omitempty"`
	Env             []EnvVar       `json:"env,omitempty"`
	WorkingDir      string         `json:"workingDir,omitempty"`
	ImagePullPolicy string         `json:"imagePullPolicy,omitempty"`
	Resources       *StepResources `json:"resources,omitempty"`
	VolumeMounts    []VolumeMount  `json:"volumeMounts,omitempty"`
}

// StepActionRef is the reference written under Step.ref. Only `name`
// is honored in v1; resolver-based forms (hub / git / cluster /
// bundles) are deferred to Track 1 #9.
type StepActionRef struct {
	Name string `json:"name"`
}
```

Then in `Step`, **two** edits:

1. Change the `Image` JSON tag from required to omitempty (Steps with
   `Ref:` legitimately have no image):

```go
	Image           string         `json:"image,omitempty"`
```

2. Insert `Ref` and `Params` immediately after `Name`:

```go
type Step struct {
	Name string `json:"name"`
	// Ref, when set, points to a StepAction whose body is inlined into
	// this Step before substitution. Mutually exclusive with Image /
	// Command / Args / Script / Env / Results: a Step is either inline
	// or a reference. See docs/superpowers/specs/2026-05-04-step-actions-design.md.
	Ref *StepActionRef `json:"ref,omitempty"`
	// Params is the list of values bound into the referenced StepAction's
	// declared params. Ignored when Ref is nil.
	Params []Param `json:"params,omitempty"`

	Image           string         `json:"image,omitempty"`
	// ... existing fields unchanged ...
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test -run 'TestUnmarshalStepAction|TestUnmarshalStepWithRef' ./internal/tektontypes/...
```

- [ ] **Step 5: Run the full tektontypes suite (no regressions)**

```bash
go test -count=1 ./internal/tektontypes/...
```

Expected: every existing test still PASS (including the existing
`TestUnmarshalTaskWithStepTemplate`).

- [ ] **Step 6: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add StepAction + Step.Ref/Params (Tekton v1beta1)"
```

---

### Task 2: Loader recognizes `kind: StepAction`

**Files:**
- Modify: `internal/loader/loader.go`
- Test: `internal/loader/loader_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/loader/loader_test.go`:

```go
func TestLoadStepAction(t *testing.T) {
	b, err := LoadBytes([]byte(`
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
	_, err := LoadBytes([]byte(`
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
}

func TestLoadV1Beta1UnknownKind(t *testing.T) {
	_, err := LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: SomethingElse
metadata: {name: x}
spec: {}
`))
	if err == nil {
		t.Fatal("want unsupported-kind error, got nil")
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
go test -run 'TestLoadStepAction|TestLoadV1Beta1' ./internal/loader/...
```

- [ ] **Step 3: Wire the loader**

In `internal/loader/loader.go`:

1. Add `StepActions` to `Bundle`:

```go
type Bundle struct {
	Tasks        map[string]tektontypes.Task
	Pipelines    map[string]tektontypes.Pipeline
	PipelineRuns []tektontypes.PipelineRun
	TaskRuns     []tektontypes.TaskRun
	StepActions  map[string]tektontypes.StepAction // (apiVersion tekton.dev/v1beta1)
	ConfigMaps   map[string]map[string][]byte
	Secrets      map[string]map[string][]byte
}
```

2. Initialise the new map in both `LoadFiles` and `LoadBytes` (same
shape as the existing `Tasks: map[string]tektontypes.Task{}` lines).

3. In `loadOne`, extend the **first** switch (apiVersion gate) to add
   a `tekton.dev/v1beta1` arm. **Important:** the existing code uses
   *two* switches (apiVersion gate, then a separate `switch head.Kind`
   below for the v1 Tekton kinds). Go's `switch` does NOT fall
   through, so the v1beta1 arm must `return` directly — it cannot
   reach the second switch. The second switch is left untouched.

   Before (today's first switch, lines 86–101 of `loader.go`):

   ```go
   switch head.APIVersion {
   case "tekton.dev/v1":
       // fall through to the Tekton-kind switch below
   case "v1":
       // Core Kubernetes kinds we accept: ConfigMap, Secret.
       switch head.Kind {
       case "ConfigMap":
           return loadConfigMap(out, data)
       case "Secret":
           return loadSecret(out, data)
       default:
           return fmt.Errorf("unsupported v1 kind %q (only ConfigMap and Secret accepted at apiVersion v1)", head.Kind)
       }
   default:
       return fmt.Errorf("unsupported apiVersion %q (only tekton.dev/v1, or v1 for ConfigMap/Secret)", head.APIVersion)
   }
   // (followed by) `switch head.Kind { case "Task": ... case "Pipeline": ... }`
   ```

   After (insert a new `case "tekton.dev/v1beta1":` arm; do NOT
   remove or alter the existing `case "tekton.dev/v1":` fall-through
   comment — the second switch below still handles `Task` /
   `Pipeline` / `PipelineRun` / `TaskRun` for v1):

   ```go
   switch head.APIVersion {
   case "tekton.dev/v1":
       // fall through to the Tekton-kind switch below (handles Task,
       // Pipeline, PipelineRun, TaskRun)
   case "tekton.dev/v1beta1":
       // v1beta1 is StepAction-only in this release. Returns directly:
       // the second switch below is skipped because Go's switch does
       // not fall through.
       switch head.Kind {
       case "StepAction":
           return loadStepAction(out, data)
       default:
           return fmt.Errorf("unsupported tekton.dev/v1beta1 kind %q (only StepAction)", head.Kind)
       }
   case "v1":
       // Core Kubernetes kinds we accept: ConfigMap, Secret.
       switch head.Kind {
       case "ConfigMap":
           return loadConfigMap(out, data)
       case "Secret":
           return loadSecret(out, data)
       default:
           return fmt.Errorf("unsupported v1 kind %q (only ConfigMap and Secret accepted at apiVersion v1)", head.Kind)
       }
   default:
       return fmt.Errorf("unsupported apiVersion %q (only tekton.dev/v1, tekton.dev/v1beta1 for StepAction, or v1 for ConfigMap/Secret)", head.APIVersion)
   }
   // The second switch below is unchanged. It only runs for the
   // tekton.dev/v1 case (the only branch that does not return).
   ```

4. Add the helper:

```go
func loadStepAction(out *Bundle, data []byte) error {
	var sa tektontypes.StepAction
	if err := yaml.Unmarshal(data, &sa); err != nil {
		return fmt.Errorf("StepAction: %w", err)
	}
	if sa.Metadata.Name == "" {
		return fmt.Errorf("StepAction: metadata.name is required")
	}
	if _, dup := out.StepActions[sa.Metadata.Name]; dup {
		return fmt.Errorf("duplicate StepAction %q", sa.Metadata.Name)
	}
	out.StepActions[sa.Metadata.Name] = sa
	return nil
}
```

5. Extend `merge` to copy StepActions across files. Concretely, in
   the existing `merge(dst, src *Bundle) error` (or wherever
   per-file bundles are folded together), add an iteration mirroring
   the existing Tasks loop:

   ```go
   for name, sa := range src.StepActions {
       if _, dup := dst.StepActions[name]; dup {
           return fmt.Errorf("duplicate StepAction %q across -f files", name)
       }
       if dst.StepActions == nil {
           dst.StepActions = map[string]tektontypes.StepAction{}
       }
       dst.StepActions[name] = sa
   }
   ```

   Place this block immediately after the equivalent Tasks loop. If
   `merge` is not the function name in the current tree (the loader
   has been refactored before), match it to whatever the existing
   Tasks-merge call site is — the structural pattern is what
   matters, not the function name.

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test -count=1 -run 'TestLoadStepAction|TestLoadV1Beta1' ./internal/loader/...
```

- [ ] **Step 5: Run full loader suite**

```bash
go test -count=1 ./internal/loader/...
```

Expected: every existing test still PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/loader/loader.go internal/loader/loader_test.go
git commit -m "feat(loader): accept tekton.dev/v1beta1 kind: StepAction"
```

---

### Task 3: Implement `resolveStepActions`

**Files:**
- Create: `internal/engine/step_action.go`
- Test: `internal/engine/step_action_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/step_action_test.go`:

```go
package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func newBundleWithStepActions(actions ...tektontypes.StepAction) *loader.Bundle {
	b := &loader.Bundle{
		Tasks:       map[string]tektontypes.Task{},
		Pipelines:   map[string]tektontypes.Pipeline{},
		StepActions: map[string]tektontypes.StepAction{},
	}
	for _, a := range actions {
		b.StepActions[a.Metadata.Name] = a
	}
	return b
}

func mkAction(name, image, script string, params []tektontypes.ParamSpec, results []tektontypes.ResultSpec) tektontypes.StepAction {
	a := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{Image: image, Script: script, Params: params, Results: results},
	}
	a.Metadata.Name = name
	a.APIVersion = "tekton.dev/v1beta1"
	a.Kind = "StepAction"
	return a
}

func TestResolveStepActionsNoOpWhenNoRefs(t *testing.T) {
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "a", Image: "alpine:3", Script: "echo hi"}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("no-op got %+v", got)
	}
}

func TestResolveStepActionsInlinesBody(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hello $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		[]tektontypes.ResultSpec{{Name: "greeting"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	st := got.Steps[0]
	if st.Name != "g" {
		t.Errorf("name = %q (must be from caller)", st.Name)
	}
	if st.Image != "alpine:3" {
		t.Errorf("image = %q (must be from action)", st.Image)
	}
	if st.Script != "echo hello world" {
		t.Errorf("script = %q (default `who=world` should be applied)", st.Script)
	}
	if len(st.Results) != 1 || st.Results[0].Name != "greeting" {
		t.Errorf("results = %+v (must be carried from action)", st.Results)
	}
	if st.Ref != nil {
		t.Errorf("ref must be cleared after expansion")
	}
}

func TestResolveStepActionsAppliesCallerParams(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hello $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:   "g",
		Ref:    &tektontypes.StepActionRef{Name: "greet"},
		Params: []tektontypes.Param{{Name: "who", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "tekton"}}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Script != "echo hello tekton" {
		t.Errorf("script = %q (caller param should have overridden default)", got.Steps[0].Script)
	}
}

func TestResolveStepActionsRequiredParamMissingDefersToOuter(t *testing.T) {
	// resolveStepActions itself only enforces ref-vs-inline + ref-exists;
	// missing required params is the validator's job (rule 14). This
	// test pins resolveStepActions' contract: with the widened
	// SubstituteAllowStepRefs, an unbound $(params.who) is DEFERRED
	// (left intact) rather than erroring at the inner pass. The outer
	// substituteSpec pass would then surface it as unknown if the
	// validator hadn't already caught it via rule 14.
	action := mkAction("greet", "alpine:3", "echo $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who"}}, // no default
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatalf("inner pass should defer, not error: %v", err)
	}
	if got.Steps[0].Script != "echo $(params.who)" {
		t.Errorf("script = %q, want literal-deferred `echo $(params.who)`", got.Steps[0].Script)
	}
}

func TestResolveStepActionsUnknownRefError(t *testing.T) {
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "g", Ref: &tektontypes.StepActionRef{Name: "nope"}}}}
	_, err := resolveStepActions(spec, newBundleWithStepActions())
	if err == nil {
		t.Fatal("want unknown-ref error, got nil")
	}
}

func TestResolveStepActionsRefAndInlineRejected(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:   "g",
		Ref:    &tektontypes.StepActionRef{Name: "greet"},
		Image:  "busybox", // illegal: can't set image alongside ref
		Script: "echo override",
	}}}
	_, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err == nil {
		t.Fatal("want ref+inline-rejected error, got nil")
	}
}

func TestResolveStepActionsPreservesIdentityFields(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:    "g",
		Ref:     &tektontypes.StepActionRef{Name: "greet"},
		OnError: "continue",
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].OnError != "continue" {
		t.Errorf("onError = %q (must be preserved from caller)", got.Steps[0].OnError)
	}
}

func TestResolveStepActionsDoesNotMutateInput(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	original := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"}}}}
	_, err := resolveStepActions(original, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if original.Steps[0].Ref == nil || original.Steps[0].Image != "" {
		t.Errorf("input mutated: %+v", original.Steps[0])
	}
}

// TestResolveStepActionsLeavesOuterRefsIntact: Critical-1 fix.
// The inner pass MUST leave every outer-scope token verbatim so the
// outer substituteSpec can resolve them. If this test ever goes red,
// the production code regressed away from SubstituteAllowStepRefs to
// plain Substitute (which errors on unknown $(...) refs).
func TestResolveStepActionsLeavesOuterRefsIntact(t *testing.T) {
	body := strings.Join([]string{
		"workspace=$(workspaces.source.path)",
		"step=$(step.results.greeting.path)",
		"prev=$(steps.prev.results.foo)",
		"trun=$(context.taskRun.name)",
		"chk=$(tasks.checkout.results.commit)",
		"outer=$(params.outerOnly)",
		"inner=$(params.who)",
	}, " ")
	action := mkAction("greet", "alpine:3", body,
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		[]tektontypes.ResultSpec{{Name: "greeting"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatalf("inner pass errored on outer-scope tokens: %v", err)
	}
	out := got.Steps[0].Script
	for _, must := range []string{
		"$(workspaces.source.path)",
		"$(step.results.greeting.path)",
		"$(steps.prev.results.foo)",
		"$(context.taskRun.name)",
		"$(tasks.checkout.results.commit)",
		"$(params.outerOnly)",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("outer token %q was rewritten or dropped; got script: %q", must, out)
		}
	}
	// And the inner-scope $(params.who) was resolved:
	if !strings.Contains(out, "inner=world") {
		t.Errorf("inner $(params.who) not resolved; got script: %q", out)
	}
}

// TestResolveStepActionsForwardsOuterParamRefAsLiteral: Critical-2 fix.
// A caller writes `params: [{name: who, value: $(params.repo)}]`. The
// inner Context's Params["who"] must contain the LITERAL string
// $(params.repo) (not pre-resolved). The inner pass rewrites
// $(params.who) in the body to $(params.repo). The outer pass (not
// exercised here) then resolves $(params.repo) from the Task scope.
func TestResolveStepActionsForwardsOuterParamRefAsLiteral(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who"}}, // required, no default
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
		Params: []tektontypes.Param{{
			Name:  "who",
			Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "$(params.repo)"},
		}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Script != "echo $(params.repo)" {
		t.Errorf("script = %q, want literal `echo $(params.repo)` (outer ref must be forwarded as literal)", got.Steps[0].Script)
	}
}

// TestResolveStepActionsTwoStepsSameAction: Important-6 fix.
// Two Steps in the same Task referencing the same StepAction with
// different param values produce two distinct inlined Steps, each
// keyed on the calling Step's name (not the StepAction's name) so
// per-step results dirs don't collide.
func TestResolveStepActionsTwoStepsSameAction(t *testing.T) {
	action := mkAction("git-clone", "alpine/git", "echo cloning $(params.url)",
		[]tektontypes.ParamSpec{{Name: "url"}},
		[]tektontypes.ResultSpec{{Name: "commit"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{
		{
			Name: "clone1", Ref: &tektontypes.StepActionRef{Name: "git-clone"},
			Params: []tektontypes.Param{{Name: "url", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "https://example.com/a"}}},
		},
		{
			Name: "clone2", Ref: &tektontypes.StepActionRef{Name: "git-clone"},
			Params: []tektontypes.Param{{Name: "url", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "https://example.com/b"}}},
		},
	}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].Name != "clone1" || got.Steps[1].Name != "clone2" {
		t.Errorf("step names = %q,%q (must be from caller)", got.Steps[0].Name, got.Steps[1].Name)
	}
	if got.Steps[0].Script != "echo cloning https://example.com/a" {
		t.Errorf("step[0].script = %q (param a should win)", got.Steps[0].Script)
	}
	if got.Steps[1].Script != "echo cloning https://example.com/b" {
		t.Errorf("step[1].script = %q (param b should win)", got.Steps[1].Script)
	}
	// Both Steps carry the same Results declaration, but per-step
	// results dirs in the docker backend are keyed on Step.Name, so
	// /tekton/steps/clone1/results/commit and /tekton/steps/clone2/results/commit
	// are distinct paths. Pin that the inlined Results lists are
	// independent slices (no aliasing surprise from the action source).
	if len(got.Steps[0].Results) != 1 || got.Steps[0].Results[0].Name != "commit" {
		t.Errorf("step[0].Results = %+v", got.Steps[0].Results)
	}
	if len(got.Steps[1].Results) != 1 || got.Steps[1].Results[0].Name != "commit" {
		t.Errorf("step[1].Results = %+v", got.Steps[1].Results)
	}
	if &got.Steps[0].Results[0] == &got.Steps[1].Results[0] {
		t.Errorf("Results slices are aliased; must be independent copies")
	}
}

// TestResolveStepActionsVolumeMountsUnion: Important-7 decision.
// StepAction body's volumeMounts are appended after the caller's
// (matches Tekton).
func TestResolveStepActionsVolumeMountsUnion(t *testing.T) {
	action := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{
			Image:        "alpine:3",
			Script:       "echo hi",
			VolumeMounts: []tektontypes.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		},
	}
	action.Metadata.Name = "greet"
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:         "g",
		Ref:          &tektontypes.StepActionRef{Name: "greet"},
		VolumeMounts: []tektontypes.VolumeMount{{Name: "cache", MountPath: "/cache"}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps[0].VolumeMounts) != 2 {
		t.Fatalf("mounts = %+v, want 2", got.Steps[0].VolumeMounts)
	}
	if got.Steps[0].VolumeMounts[0].Name != "tmp" || got.Steps[0].VolumeMounts[1].Name != "cache" {
		t.Errorf("mount order = %+v, want [tmp, cache] (action then caller)", got.Steps[0].VolumeMounts)
	}
}
```

(The new tests import `strings`; add it to the test file's import
block.)

- [ ] **Step 2: Run tests — expect FAIL** (`resolveStepActions undefined`).

```bash
go test -run TestResolveStepActions ./internal/engine/...
```

- [ ] **Step 3: Implement the resolver**

Create `internal/engine/step_action.go`:

```go
package engine

import (
	"fmt"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// resolveStepActions returns a new TaskSpec where every Step that
// carries a Ref has its body replaced by the referenced StepAction's
// inlined body, with Step.Params bound against the StepAction's
// declared params (StepAction defaults applied when the caller did
// not supply a value).
//
// Identity fields (Name, OnError, VolumeMounts) are kept from the
// calling Step. Body fields (Image, Command, Args, Script, Env,
// WorkingDir, ImagePullPolicy, Resources, Results) come from the
// StepAction. The calling Step must not set any body field alongside
// Ref — that's a hard error here as well as in the validator (defense
// in depth: validator catches it pre-run; engine catches it if a
// future loader path skips validation).
//
// $(params.X) inside the StepAction body is resolved here against a
// scoped param view (StepAction declarations + caller bindings).
// Outer-scope substitutions ($(params.<task-param>),
// $(tasks.X.results.Y), $(workspaces.X.path), $(step.results.X.path),
// $(steps.X.results.Y)) are left to the existing substituteSpec /
// per-step backend passes.
func resolveStepActions(spec tektontypes.TaskSpec, b *loader.Bundle) (tektontypes.TaskSpec, error) {
	hasRef := false
	for _, s := range spec.Steps {
		if s.Ref != nil {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return spec, nil
	}
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		if st.Ref == nil {
			out.Steps[i] = st
			continue
		}
		if err := assertNoInlineBody(st); err != nil {
			return tektontypes.TaskSpec{}, err
		}
		action, ok := b.StepActions[st.Ref.Name]
		if !ok {
			return tektontypes.TaskSpec{}, fmt.Errorf("step %q: references unknown StepAction %q", st.Name, st.Ref.Name)
		}
		resolved, err := inlineStepAction(st, action)
		if err != nil {
			return tektontypes.TaskSpec{}, err
		}
		out.Steps[i] = resolved
	}
	return out, nil
}

// assertNoInlineBody returns an error if the Step (with Ref set) also
// carries any body field. Mirrors validator rule 13.
func assertNoInlineBody(st tektontypes.Step) error {
	if st.Image != "" || len(st.Command) > 0 || len(st.Args) > 0 ||
		st.Script != "" || len(st.Env) > 0 || st.WorkingDir != "" ||
		st.ImagePullPolicy != "" || st.Resources != nil ||
		len(st.Results) > 0 {
		return fmt.Errorf("step %q: ref and inline body are mutually exclusive", st.Name)
	}
	return nil
}

func inlineStepAction(st tektontypes.Step, action tektontypes.StepAction) (tektontypes.Step, error) {
	// Build the scoped param view: StepAction defaults first, then
	// caller overrides. Caller values are forwarded as LITERAL
	// strings — if the caller wrote `value: $(params.repo)`, the
	// inner Context's Params["<inner>"] is the literal string
	// `$(params.repo)` (not pre-resolved). The inner pass rewrites
	// `$(params.<inner>)` → `$(params.repo)`; the OUTER substituteSpec
	// pass that runs immediately after this function (and after
	// applyStepTemplate) resolves $(params.repo) from the Task scope.
	// Pre-resolving caller values here would lose outer-scope tokens
	// like $(tasks.X.results.Y) that aren't bound at this site.
	params := map[string]string{}
	for _, decl := range action.Spec.Params {
		if decl.Default == nil {
			continue
		}
		// v1: only string defaults are honored. Array/object
		// defaults are rejected by validator rule 18; this guard is
		// defense-in-depth in case the engine is invoked without
		// validation.
		if decl.Default.Type != "" && decl.Default.Type != tektontypes.ParamTypeString {
			return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): param %q default type %q is not supported (only string defaults)", st.Name, action.Metadata.Name, decl.Name, decl.Default.Type)
		}
		params[decl.Name] = decl.Default.StringVal
	}
	for _, p := range st.Params {
		// Forward as a literal string. Outer-scope refs survive.
		params[p.Name] = p.Value.StringVal
	}
	rctx := resolver.Context{Params: params}

	// Substitute the StepAction body against the scoped context.
	// CRITICAL: Use the AllowStepRefs variants so outer-scope tokens
	// ($(workspaces.X.path), $(step.results.X.path), $(steps.X.results.Y),
	// $(context.X), $(tasks.X.results.Y), and outer $(params.<task-param>))
	// survive the inner pass and are resolved by the outer
	// substituteSpec pass that runs immediately after. Plain
	// resolver.Substitute would error on every one of these
	// tokens — see spec §3.3 "AllowStepRefs widening note".
	image, err := resolver.SubstituteAllowStepRefs(action.Spec.Image, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	script, err := resolver.SubstituteAllowStepRefs(action.Spec.Script, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	workdir, err := resolver.SubstituteAllowStepRefs(action.Spec.WorkingDir, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	args, err := resolver.SubstituteArgsAllowStepRefs(action.Spec.Args, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	cmd, err := resolver.SubstituteArgsAllowStepRefs(action.Spec.Command, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	env := make([]tektontypes.EnvVar, len(action.Spec.Env))
	for i, e := range action.Spec.Env {
		v, err := resolver.SubstituteAllowStepRefs(e.Value, rctx)
		if err != nil {
			return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
		}
		env[i] = tektontypes.EnvVar{Name: e.Name, Value: v}
	}

	// VolumeMounts: union — StepAction body's mounts first, caller's
	// appended (matches Tekton; see spec §9 open-question 3).
	var mounts []tektontypes.VolumeMount
	if len(action.Spec.VolumeMounts) > 0 {
		mounts = append(mounts, action.Spec.VolumeMounts...)
	}
	if len(st.VolumeMounts) > 0 {
		mounts = append(mounts, st.VolumeMounts...)
	}

	resolved := tektontypes.Step{
		Name:            st.Name,
		OnError:         st.OnError,
		VolumeMounts:    mounts,
		Image:           image,
		Command:         cmd,
		Args:            args,
		Script:          script,
		Env:             env,
		WorkingDir:      workdir,
		ImagePullPolicy: action.Spec.ImagePullPolicy,
		Resources:       action.Spec.Resources,
		Results:         append([]tektontypes.ResultSpec(nil), action.Spec.Results...),
	}
	return resolved, nil
}
```

**Pre-task: widen `SubstituteAllowStepRefs`.** The function as it
stands today defers only `$(step.results.X.path)` and
`$(steps.<step>.results.<name>)` placeholders. The inner pass
documented above also needs unknown `params.<name>`,
`workspaces.<name>.path`, `context.<name>`, and
`tasks.<name>.results.<name>` to be deferred (left in the string,
not erroring) so the outer `substituteSpec` pass can resolve them
under the full Task scope. Make the corresponding edit in
`internal/resolver/resolver.go` BEFORE running this Task's tests:

- In `lookup`, change the `unknown param`, `unknown context var`,
  `no results for task`, `task X has no result Y`, and
  `unknown workspaces ref` returns from plain errors to
  `return "", errStepRefDeferred` **only when called via**
  `SubstituteAllowStepRefs` (NOT via `Substitute`). Concretely:
  introduce a small wrapper-flag pattern, or duplicate the relevant
  arms into a `lookupAllowDefer` helper that swaps the error
  conversion. The simpler implementation: leave `lookup` as-is for
  unambiguous-malformed cases (e.g. `malformed task ref`,
  `unknown reference`), and convert the four "unknown name in a
  known scope" errors to `errStepRefDeferred` inside a separate
  code path the AllowStepRefs functions call.

  Add tests in `internal/resolver/step_test.go`:

  ```go
  func TestSubstituteAllowStepRefsLeavesEveryOuterScope(t *testing.T) {
      // Inner pass populates only the inner StepAction-declared params.
      ctx := resolver.Context{Params: map[string]string{"who": "tekton"}}
      cases := []string{
          "$(params.repo)",                // outer task-scope param
          "$(workspaces.source.path)",
          "$(context.taskRun.name)",
          "$(tasks.checkout.results.commit)",
          "$(step.results.greeting.path)",
          "$(steps.prev.results.foo)",
      }
      for _, in := range cases {
          got, err := resolver.SubstituteAllowStepRefs(in, ctx)
          if err != nil { t.Errorf("%s: err = %v", in, err) }
          if got != in { t.Errorf("%s: rewrote to %q (must survive)", in, got) }
      }
      // And a known inner param is still resolved:
      got, err := resolver.SubstituteAllowStepRefs("hi $(params.who)", ctx)
      if err != nil { t.Fatal(err) }
      if got != "hi tekton" { t.Errorf("got %q", got) }
  }
  ```

  This test goes red until `SubstituteAllowStepRefs` is widened and
  green afterwards.

- [ ] **Step 4: Run tests — expect PASS (all 8)**

```bash
go test -count=1 -run TestResolveStepActions ./internal/engine/...
```

- [ ] **Step 5: Commit**

```bash
git add internal/engine/step_action.go internal/engine/step_action_test.go
git commit -m "feat(engine): resolveStepActions inlines Step.Ref bodies"
```

---

### Task 4: Wire `resolveStepActions` into runOne and uniqueImages

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/step_action_engine_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/engine/step_action_engine_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

func TestStepActionResolvedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params:
    - {name: who, default: world}
  image: alpine:3
  script: 'echo hello $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: g
      ref: {name: greet}
      params:
        - {name: who, value: tekton}
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
		t.Errorf("image = %q, want alpine:3 (inlined from StepAction)", got)
	}
	if got := inv.Task.Steps[0].Script; got != "echo hello tekton" {
		t.Errorf("script = %q, want with caller-param applied", got)
	}
	if inv.Task.Steps[0].Ref != nil {
		t.Errorf("backend received Ref-bearing Step; resolveStepActions did not run before submit")
	}
}
```

(`captureBackend` and `sliceSink` reuse the helpers from
`step_template_engine_test.go` — keep both files in package
`engine_test`. If `captureBackend` is not yet exported in the test
package, copy its definition here verbatim.)

- [ ] **Step 2: Run test — expect FAIL** (Step keeps `Ref:` set; image is empty).

```bash
go test -count=1 -run TestStepActionResolvedBeforeBackend ./internal/engine/...
```

- [ ] **Step 3: Wire `resolveStepActions` into `runOne`**

In `internal/engine/engine.go`, find the `runOne` body where
`lookupTaskSpec` is called and `applyStepTemplate` follows
immediately after. Insert the new `resolveStepActions` call
**between** them (the same site where `applyStepTemplate` runs
between `lookupTaskSpec` and `substituteSpec`):

```go
	// Resolve task spec, expand Step.Ref → StepAction bodies, then
	// merge StepTemplate into each Step before any further
	// substitution / validation runs.
	spec, err := lookupTaskSpec(in.Bundle, pt)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	spec, err = resolveStepActions(spec, in.Bundle)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	spec = applyStepTemplate(spec)
```

- [ ] **Step 4: Wire `resolveStepActions` into `uniqueImages`**

In the same file, update `uniqueImages` so referenced StepAction images
are pre-pulled:

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
		// Expand StepAction refs first so their images are pre-pulled,
		// then merge stepTemplate so inherited images count too.
		if expanded, err := resolveStepActions(spec, b); err == nil {
			spec = expanded
		}
		spec = applyStepTemplate(spec)
		for _, s := range spec.Steps {
			if s.Image != "" {
				seen[s.Image] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for img := range seen {
		out = append(out, img)
	}
	return out
}
```

(Note the `s.Image != ""` guard — a Step with `Ref:` whose StepAction
is missing would otherwise contribute the empty string. The validator
catches the missing ref and fails the run; this guard keeps
pre-pulling robust against that path.)

- [ ] **Step 5: Run target test — expect PASS**

```bash
go test -count=1 -race -run TestStepActionResolvedBeforeBackend ./internal/engine/...
```

- [ ] **Step 6: Run the full engine suite (no regressions)**

```bash
go test -race -count=1 ./internal/engine/...
```

Expected: every existing test still PASS — including
`TestStepTemplate*`, `TestEngine*`, the v1.3 timeout tests, and the
v1.5 pipeline-results tests.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/engine.go internal/engine/step_action_engine_test.go
git commit -m "feat(engine): expand StepActions before stepTemplate / substitution"
```

---

### Task 5: Validator rules 12–18

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateStepActionRefValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who, default: world}]
  image: alpine:3
  script: 'echo $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: greet}}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateStepActionUnknownRef(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: nope}}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "unknown StepAction") {
		t.Errorf("want unknown-StepAction error, got %v", errs)
	}
}

func TestValidateStepActionRefAndInlineRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec: {image: alpine:3, script: 'echo hi'}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: g, ref: {name: greet}, image: busybox}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "ref and inline body") {
		t.Errorf("want ref+inline-rejected error, got %v", errs)
	}
}

func TestValidateStepActionMissingRequiredParam(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who}]    # required (no default)
  image: alpine:3
  script: 'echo $(params.who)'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: greet}}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), `missing required StepAction param "who"`) {
		t.Errorf("want missing-param error, got %v", errs)
	}
}

// Rule 15: a StepAction body itself must not contain `ref:`. Spec
// §3.4 settles this with a structural guard (StepActionSpec has no
// Ref field); no runtime post-load scan. This reflection-based test
// is the canonical enforcement — if anyone adds Ref to StepActionSpec,
// this test goes red and the build breaks.
func TestValidateStepActionNoNestedRef(t *testing.T) {
	var sa tektontypes.StepActionSpec
	v := reflect.ValueOf(sa)
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name == "Ref" {
			t.Fatal("StepActionSpec must not model Ref (would allow nested refs)")
		}
	}
}

// Rule 16: an inline Step (no ref:) must have a non-empty image
// after stepTemplate inheritance. Paired with the Step.Image JSON
// tag relaxation from required → omitempty.
func TestValidateInlineStepRequiresImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - {name: bad, script: 'echo'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "inline step has no image") {
		t.Errorf("want no-image error, got %v", errs)
	}
}

// Rule 16 positive: image inherited from stepTemplate should pass.
func TestValidateInlineStepImageInheritedFromStepTemplate(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  stepTemplate: {image: alpine:3}
  steps:
    - {name: ok, script: 'echo'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// Rule 17: resolver-form ref: { resolver: hub, ... } must be
// rejected with a clear "not supported in this release" message,
// not the confusing "references unknown StepAction \"\"" from rule 12.
func TestValidateResolverFormRefRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps:
    - name: g
      ref: {resolver: hub, params: [{name: name, value: git-clone}]}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "resolver-based StepAction refs") {
		t.Errorf("want resolver-form rejection, got %v", errs)
	}
}

// Rule 18: a StepAction param with array/object default is rejected.
func TestValidateStepActionArrayDefaultRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params:
    - name: who
      type: array
      default: [a, b]
  image: alpine:3
  script: 'echo'
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: g, ref: {name: greet}, params: [{name: who, value: x}]}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "default type") {
		t.Errorf("want default-type error, got %v", errs)
	}
}
```

(`mustLoad` is the existing helper in `validator_test.go`. The new
tests import `reflect`, `strings`, and `tektontypes` — make sure all
three are in the test file's import block.)

- [ ] **Step 2: Run tests — expect FAIL**

```bash
go test -count=1 -run TestValidateStepAction ./internal/validator/...
```

- [ ] **Step 3: Implement rules 12, 13, 14, 16, 17, 18**

In `internal/validator/validator.go`, after the existing rule 11 block
(volumeMounts), add:

```go
	// 12 / 13 / 14 / 17. StepAction reference rules.
	for taskName, spec := range resolvedTasks {
		// rawSteps[i] is the original map[string]any view of spec.Steps[i],
		// used by rule 17 to read the `ref:` map's pre-typing keys
		// (resolver / params / bundle) that sigs.k8s.io/yaml otherwise
		// drops during typed unmarshaling. Populated by the validator's
		// existing per-task raw-shape pass; if that pass doesn't exist
		// yet, add it: `yaml.Unmarshal(taskYAMLBytes, &rawTask)` and
		// pull `rawTask["spec"]["steps"]` as []any.
		rawSteps := rawStepsFor(taskName) // helper returning []map[string]any
		for i, st := range spec.Steps {
			if st.Ref == nil {
				continue
			}
			// Rule 17: resolver-form ref:.
			if i < len(rawSteps) {
				if rawRef, ok := rawSteps[i]["ref"].(map[string]any); ok {
					for _, key := range []string{"resolver", "params", "bundle"} {
						if _, has := rawRef[key]; has {
							errs = append(errs, fmt.Errorf("pipeline task %q step %q: resolver-based StepAction refs (resolver/params/bundle under ref:) are not supported in this release; see Track 1 #9", taskName, st.Name))
							goto nextStep
						}
					}
				}
			}
			// Rule 13: ref and inline body are mutually exclusive.
			if st.Image != "" || len(st.Command) > 0 || len(st.Args) > 0 ||
				st.Script != "" || len(st.Env) > 0 || st.WorkingDir != "" ||
				st.ImagePullPolicy != "" || st.Resources != nil ||
				len(st.Results) > 0 {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: ref and inline body are mutually exclusive", taskName, st.Name))
				continue
			}
			// Rule 12: the referenced StepAction must exist.
			action, ok := b.StepActions[st.Ref.Name]
			if !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: references unknown StepAction %q", taskName, st.Name, st.Ref.Name))
				continue
			}
			// Rule 14: required StepAction params must be bound.
			bound := map[string]bool{}
			for _, p := range st.Params {
				bound[p.Name] = true
			}
			for _, decl := range action.Spec.Params {
				if decl.Default == nil && !bound[decl.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: missing required StepAction param %q", taskName, st.Name, decl.Name))
				}
			}
		nextStep:
		}
	}

	// Rule 16. Inline Step (no ref:) must have a non-empty image
	// after stepTemplate inheritance. Run AFTER the validator's own
	// stepTemplate-merge pass so an image inherited from
	// Task.spec.stepTemplate.image counts.
	for taskName, spec := range resolvedTasks {
		merged := applyStepTemplateForValidation(spec) // existing or trivially-added helper mirroring engine.applyStepTemplate
		for _, st := range merged.Steps {
			if st.Ref != nil {
				continue
			}
			if st.Image == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: inline step has no image (set image: or use ref:)", taskName, st.Name))
			}
		}
	}

	// Rule 18. StepAction params with array/object defaults are
	// rejected at validate time (the inner pass only honors string
	// defaults; this prevents silent drops).
	for saName, sa := range b.StepActions {
		for _, decl := range sa.Spec.Params {
			if decl.Default == nil {
				continue
			}
			if t := decl.Default.Type; t == tektontypes.ParamTypeArray || t == tektontypes.ParamTypeObject {
				errs = append(errs, fmt.Errorf("StepAction %q param %q: default type %q is not supported (only string defaults)", saName, decl.Name, t))
			}
		}
	}
```

Rule 15 is enforced **structurally** — `StepActionSpec` does not
model `Ref`, and `TestValidateStepActionNoNestedRef` (reflection
guard) fails the build if anyone adds it. **There is no runtime
post-load scan**, contra earlier drafts of the spec; spec §3.4 has
been reconciled to match this plan (build-time guard only).

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test -count=1 -run TestValidateStepAction ./internal/validator/...
```

- [ ] **Step 5: Run full validator suite**

```bash
go test -count=1 ./internal/validator/...
```

Expected: every existing test still PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/validator/validator.go internal/validator/validator_test.go
git commit -m "feat(validator): rules 12-14 for Step.Ref → StepAction"
```

---

### Task 6: Cluster backend round-trips inlined Steps (regression-locking test)

**Files:**
- Test: `internal/backend/cluster/runpipeline_test.go`

The cluster backend already serialises `TaskSpec` via `taskSpecToMap`
(JSON round-trip). Because the engine expands StepActions **before**
calling `RunPipeline`, the cluster backend never sees `Ref` on a Step.
This test pins that contract: if a future code path bypasses the
engine and submits a raw Task with `Ref:` set, the test catches it.

- [ ] **Step 1: Write the test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunInlinesStepActionRefs: when the engine expanded
// a Step.Ref into an inlined Step, the cluster backend must serialise
// the resulting Step without `ref:` under
// pipelineSpec.tasks[].taskSpec.steps[<i>], with image / script /
// results carried through. (taskSpecToMap is a json.Marshal round-
// trip; this test guards that property after StepAction expansion.)
func TestBuildPipelineRunInlinesStepActionRefs(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	// Construct the post-expansion Task by hand — simulating what
	// resolveStepActions produces just before the engine hands the
	// spec to the cluster backend.
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps: []tektontypes.Step{{
			Name:    "g",
			Image:   "alpine:3",
			Script:  "echo hello tekton",
			Results: []tektontypes.ResultSpec{{Name: "greeting"}},
		}},
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
	tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks slice = %d, want 1", len(tasks))
	}
	taskMap := tasks[0].(map[string]any)
	taskSpec := taskMap["taskSpec"].(map[string]any)
	steps, ok := taskSpec["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("taskSpec.steps = %v", taskSpec["steps"])
	}
	step := steps[0].(map[string]any)
	if _, hasRef := step["ref"]; hasRef {
		t.Errorf("inlined step still has `ref:` field; engine expansion lost: %+v", step)
	}
	if step["image"] != "alpine:3" {
		t.Errorf("step.image = %v, want alpine:3", step["image"])
	}
	if step["script"] != "echo hello tekton" {
		t.Errorf("step.script = %v, want echo hello tekton", step["script"])
	}
}
```

- [ ] **Step 2: Run the test**

```bash
go test -count=1 -run TestBuildPipelineRunInlinesStepActionRefs ./internal/backend/cluster/...
```

Expected: PASS (no production change required — `taskSpecToMap`
already round-trips via `json.Marshal` / `json.Unmarshal`, and our
hand-built post-expansion Task has no `Ref` field on its Steps).

- [ ] **Step 3: Commit**

```bash
git add internal/backend/cluster/runpipeline_test.go
git commit -m "test(cluster): assert post-expansion Steps round-trip without ref:"
```

---

### Task 7: Cross-backend e2e fixture

**Files:**
- Create: `testdata/e2e/step-actions/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/step-actions/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata:
  name: greet
spec:
  params:
    - name: who
      default: world
  results:
    - name: greeting
  image: alpine:3
  script: |
    msg="hello $(params.who)"
    echo "$msg"
    printf "%s" "$msg" > $(step.results.greeting.path)
---
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: greeter
spec:
  results:
    - name: greeting
  steps:
    - name: greet
      ref: { name: greet }
      params:
        - name: who
          value: tekton
    - name: consume
      image: alpine:3
      env:
        - name: MSG
          value: $(steps.greet.results.greeting)
      script: |
        echo "got: $MSG"
        test "$MSG" = "hello tekton"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: step-actions
spec:
  tasks:
    - name: t
      taskRef: { name: greeter }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, inside `All()`, after the
`step-results` entry (or alongside `step-template`):

```go
		{Dir: "step-actions", Pipeline: "step-actions", WantStatus: "succeeded"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/step-actions -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Cluster-side fidelity assertion (Important 5)**

The cluster-integration harness in `internal/e2e/cluster/` already
runs every `fixtures.All()` row via `tkn-act run --cluster -f
<fixture>`. Extend the cluster harness's per-fixture body to
**additionally** assert, for the `step-actions` row, that the
submitted PipelineRun object has no `ref:` field on any inlined
Step under `spec.pipelineSpec.tasks[].taskSpec.steps[]`. Concretely,
in the existing cluster e2e per-row body, after the run completes:

```go
if f.Dir == "step-actions" {
    // Re-fetch the PipelineRun the cluster backend submitted and
    // inspect spec.pipelineSpec.tasks[].taskSpec.steps[].
    pr := getPipelineRun(t, kubeconfig, ns, runID) // existing helper
    spec, _, _ := unstructured.NestedMap(pr.Object, "spec", "pipelineSpec")
    tasks, _, _ := unstructured.NestedSlice(spec, "tasks")
    for _, ti := range tasks {
        ts, _, _ := unstructured.NestedSlice(ti.(map[string]any), "taskSpec", "steps")
        for _, si := range ts {
            step := si.(map[string]any)
            if _, hasRef := step["ref"]; hasRef {
                t.Errorf("cluster backend received Ref-bearing Step; client-side expansion regressed: %+v", step)
            }
        }
    }
}
```

This is the cross-backend invariant: tkn-act expands StepActions
client-side; the cluster backend submits the inlined Step shape;
the cluster harness fails the build if either property regresses.

Document the limitation in the same commit as the fixture
(`docs/feature-parity.md` row, see Task 8 step 6): tkn-act's
StepAction behavior matches Tekton ≥0.50 client-side semantics for
the Step-field subset tkn-act reads; controller-side resolver
behavior is intentionally out of scope (Track 1 #9).

- [ ] **Step 6: Commit**

```bash
git add testdata/e2e/step-actions/pipeline.yaml internal/e2e/fixtures/fixtures.go internal/e2e/cluster/...
git commit -m "test(e2e): step-actions fixture (cross-backend) + cluster fidelity assertion"
```

---

### Task 8: Documentation convergence

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/tkn-act/agentguide_data.md` (auto-generated from AGENTS.md)
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`

- [ ] **Step 1: Add a "StepActions" section to `AGENTS.md`**

Insert immediately after the existing `## `stepTemplate` (DRY for
Steps)` section, before `## Documentation rule: keep related docs in
sync with every change`:

```markdown
## StepActions (`tekton.dev/v1beta1`)

A `StepAction` is a referenceable Step shape: a top-level Tekton
resource (`apiVersion: tekton.dev/v1beta1`, `kind: StepAction`) that
the engine inlines into Steps that reference it via `ref:`. Same
multi-doc YAML stream as Tasks / Pipelines — pass with `-f`.

```yaml
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who, default: world}]
  results: [{name: greeting}]
  image: alpine:3
  script: |
    echo hello $(params.who) > $(step.results.greeting.path)
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: greeter}
spec:
  steps:
    - name: greet
      ref: {name: greet}
      params: [{name: who, value: tekton}]
```

Resolution rules tkn-act follows:

| Field | Behavior |
|---|---|
| `Step.ref.name` | Resolved against the loaded bundle's StepActions map. Unknown name → exit 4 (validate). |
| `Step.ref` + body fields | Mutually exclusive. A Step is either inline or a reference; setting both → exit 4. |
| `Step.params` | Bound into the StepAction's declared params. StepAction defaults apply for omitted entries; missing required params → exit 4. |
| `name`, `onError`, `volumeMounts` | Per-Step (kept from the calling Step). |
| `image`, `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources`, `results` | From the StepAction's body. |
| `$(params.X)` inside the StepAction body | Resolves against the StepAction-scoped param view, not the surrounding Task scope. |
| `$(step.results.X.path)` | Writes to `/tekton/steps/<calling-step-name>/results/X` — same per-step results dir as inline `Step.results`. |
| `$(steps.<calling-step-name>.results.X)` from later steps | Reads the literal value, same as for inline Steps. |
| Resolver-based `ref:` (`hub`, `git`, etc.) | Not supported in v1; tracked as Track 1 #9. |

The cluster backend receives the same expanded Step shape — there is
no `kind: StepAction` apply onto the per-run namespace. This keeps
both backends bit-identical at the submission layer.

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

In the `### -tags integration` table, add a row right after
`step-template/`:

```markdown
| `step-actions/`     | `Step.ref` → `StepAction` resolution: caller-param override of default + result via `$(steps.X.results.Y)` |
```

- [ ] **Step 5: Mark Track 1 #8 done in `docs/short-term-goals.md`**

In the Track 1 table, change the row 8 Status cell from:

```
| Not started. Needs design (resolution + caching). |
```

to:

```
| Done in v1.6 (PR for `feat: StepActions`). Engine-side expansion before stepTemplate; cluster backend receives the same inlined Steps; no resolver fetching (Track 1 #9). |
```

- [ ] **Step 6: Flip the `feature-parity.md` row**

Under `### Resolution & catalogs`, change:

```
| `StepActions` (`tekton.dev/v1beta1` referenceable steps) | `Step.ref` | gap | both | none | none | docs/short-term-goals.md (Track 1 #8) |
```

to:

```
| `StepActions` (`tekton.dev/v1beta1` referenceable steps) | `Step.ref` | shipped | both | step-actions | none | docs/superpowers/plans/2026-05-04-step-actions.md (Track 1 #8) |
```

- [ ] **Step 7: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 8: Commit**

```bash
git add AGENTS.md cmd/tkn-act/agentguide_data.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md
git commit -m "docs: document StepActions; flip Track 1 #8 to shipped"
```

---

### Task 9: Final verification and PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all exit 0; every test package OK or no-test-files.

- [ ] **Step 2: Coverage gate sanity-check**

```bash
bash .github/scripts/coverage-check.sh main HEAD || true
```

The new `internal/engine/step_action.go` ships with its own
`internal/engine/step_action_test.go` and integration test, so the
engine package's per-package coverage should not drop. Same for
`internal/loader` and `internal/validator`. The cluster-backend test
is regression-locking only (no new code), so the cluster package
coverage doesn't change.

- [ ] **Step 3: Push branch and open PR**

```bash
git push -u origin feat/step-actions
gh pr create --title "feat: StepActions (Track 1 #8)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #8 of `docs/short-term-goals.md`. Honors Tekton's
`StepAction` (`apiVersion: tekton.dev/v1beta1`) on both backends:

- New `tektontypes.StepAction` / `StepActionSpec` / `StepActionRef`
  types; `Step.Ref` and `Step.Params` fields. YAML round-trip
  tested.
- Loader recognizes `kind: StepAction` and indexes them in
  `Bundle.StepActions`, with the same duplicate-detection / multi-
  file merge rules as Tasks / Pipelines.
- Engine adds a `resolveStepActions` pass that expands every
  `Step.Ref` into a concrete inlined Step **before**
  `applyStepTemplate` and `substituteSpec`. StepAction `params`
  defaults apply when the caller did not bind them; caller bindings
  override defaults. Identity fields (`name`, `onError`,
  `volumeMounts`) are kept from the calling Step; body fields come
  from the StepAction. The StepAction's `results:` flow into the
  inlined `Step.Results`, so per-step results dirs work unchanged.
- Validator rules 12-14: unknown ref → exit 4; ref+inline →
  exit 4; missing required StepAction param → exit 4. Rule 15
  (no nested refs) is structurally enforced (StepActionSpec
  doesn't model `Ref`).
- Cluster backend receives the same inlined Step shape — no
  StepAction objects are applied into the per-run namespace; the
  expansion is fully client-side. Regression test pins this.
- One cross-backend fixture (`step-actions`) exercises caller-param
  override + result-via-`$(steps.X.results.Y)`.

Implements `docs/superpowers/specs/2026-05-04-step-actions-design.md`
via `docs/superpowers/plans/2026-05-04-step-actions.md`.

Resolver-based `ref:` (`hub`, `git`, `cluster`, `bundles`) remains
gap-listed under Track 1 #9.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] tests-required script
- [x] coverage-check script (no per-package drop)
- [ ] docker-integration CI — runs the new fixture
- [ ] cluster-integration CI — same fixture on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Type model → Task 1. Loader → Task 2.
  Resolver/expansion logic with rules for every Tekton-spec'd path
  → Task 3. Engine wiring (substitution + image pre-pull) → Task 4.
  Validator → Task 5. Cluster invariants → Task 6. Cross-backend
  fidelity → Task 7. Docs convergence → Task 8. Final ship → Task 9.
- **No placeholders:** every step has actual code or commands. The
  one judgment call (Task 5 step 3, where rule 15 is structurally
  enforced rather than runtime-checked) is documented in the spec
  open-questions section as well.
- **Type consistency:** `resolveStepActions(TaskSpec, *loader.Bundle)
  (TaskSpec, error)` is the only new public-within-package symbol;
  used uniformly across Tasks 3 and 4. `inlineStepAction` and
  `assertNoInlineBody` are file-scoped helpers.
- **Order-of-operations contract:** Task 4 inserts
  `resolveStepActions` between `lookupTaskSpec` and
  `applyStepTemplate`. The same order applies inside `uniqueImages`,
  so pre-pull and runtime see the identical shape. Step 6 of Task 4
  explicitly runs the full engine test suite to confirm
  `TestStepTemplate*`, `TestEngine*`, and the v1.3/v1.5 pipeline
  tests do not regress.
- **Cluster pass-through "free":** because expansion happens engine-
  side and `taskSpecToMap` is a `json.Marshal` round-trip, the
  cluster backend receives a Step whose `Ref` is nil (and thus
  `omitempty` drops the key entirely). Task 6 is a regression-locking
  test for that property, not a new code path.
- **Docs are atomic with the code:** Task 8 lands AGENTS.md ↔
  embedded guide convergence, test-coverage.md, short-term-goals.md,
  *and* the feature-parity row flip in one commit, so the
  `parity-check` is satisfied at every commit boundary.
- **`tests-required` hygiene:** every Go-source-changing commit in
  this plan ships with a co-located `_test.go` change. Task 6's
  cluster-side commit is a test-only change (no `.go` production
  edit), which the `tests-required` script handles natively.
- **Coverage-no-drop:** `internal/engine` gains `step_action.go` plus
  its tests in the same commit (Task 3). `internal/loader` and
  `internal/validator` similarly gain new code paths covered by new
  tests in the same commit. The coverage gate compares per-package;
  no package should report a drop.
