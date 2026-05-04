# StepActions (`tekton.dev/v1beta1`) — design spec

**Date:** 2026-05-04
**Status:** Proposed (Track 1 #8)
**One-liner:** Make Tekton's `StepAction` — a referenceable, reusable Step — work end-to-end on both backends, by inlining the referenced `StepAction.spec` into the calling Step before any other engine pass.

This spec extends `2026-05-01-tkn-act-design.md` and follows the same shape
as `2026-05-02-tkn-act-docker-fidelity-design.md`. Read those first if
you have not yet.

---

## 1. Goals & non-goals

### Goals

- A YAML stream passed to `tkn-act -f` may contain documents with
  `apiVersion: tekton.dev/v1beta1`, `kind: StepAction`. The loader
  parses them into a typed in-memory map, keyed by `metadata.name`.
- A Step inside `Task.spec.steps` may carry `ref: { name: <stepaction> }`
  and a list of `params:`. The engine resolves that Step into the
  referenced `StepAction.spec`, applying the Step-supplied params (with
  StepAction defaults for those the Step did not override) and writes
  the resolved Step back into the Task spec **before** `stepTemplate`
  merging or `$(...)` substitution runs.
- The StepAction's `results:` block contributes to the surrounding
  Task's results: writing to `$(step.results.<name>.path)` from inside
  a referenced Step's script lands a file at the Task's per-step
  results dir, exactly as if the Task had declared `Step.results`
  inline (the existing `step-results` fixture's contract).
- Both backends agree:
  - The docker backend never sees a Step with `ref:` set; it sees the
    inlined Step shape it has always understood.
  - The cluster backend submits the **same** inlined Step shape under
    `pipelineSpec.tasks[].taskSpec.steps[]` — i.e. tkn-act expands
    StepActions client-side; it does **not** apply `StepAction`
    objects into the per-run namespace and let the Tekton controller
    resolve them.
- Cross-backend `internal/e2e/fixtures.All()` row exercises both
  backends through the same fixture; cluster fidelity is a checked
  invariant, not an aspiration.

### Non-goals (this spec)

- **Resolver-based fetching** of StepActions (`ref: { resolver: hub, ... }`,
  `ref: { resolver: git, ... }`, etc.). This stays gap-listed under
  Track 1 #9; v1 of StepActions is local-files-only, the same way
  `taskRef.name` is local-files-only today.
- **The `apiVersion: tekton.dev/v1` form of StepAction.** Upstream is
  still on `v1beta1`; we follow that. When upstream graduates we add
  a one-line case to the loader.
- **Cross-Task StepAction references via cluster lookup.** `ref.name` is
  resolved against the in-memory bundle; we do not reach into the
  Tekton control plane to find a `StepAction` we did not load.
- **Recursion / nested StepActions.** A StepAction's body is a single
  Step shape (image / script / params / results); it cannot itself
  contain `ref:` or call out to another StepAction. The validator
  rejects nested refs.
- **Sidecar-style StepActions** or other v1beta1-only fields beyond the
  Step subset tkn-act already reads (`image`, `command`, `args`,
  `script`, `env`, `workingDir`, `imagePullPolicy`, `resources`,
  `volumeMounts`, `results`, `onError`).
- **Hot-reload / caching of StepActions across runs.** They live in the
  bundle; the bundle is rebuilt every run.

### Personas

Same as v1. Persona A (the local Docker-mode developer) gets the
biggest win: catalog Tasks that begin to publish StepActions can be
consumed without `--cluster`.

---

## 2. Tekton-upstream behavior we're matching

A StepAction is a top-level Tekton resource that looks like a partial
Step:

```yaml
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata:
  name: git-clone
spec:
  params:
    - name: url
    - name: revision
      default: main
  results:
    - name: commit
  image: alpine/git
  script: |
    git clone $(params.url) /workspace/source
    git -C /workspace/source checkout $(params.revision)
    git -C /workspace/source rev-parse HEAD > $(step.results.commit.path)
```

A Task references it from inside `spec.steps[]`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata: { name: ci }
spec:
  results:
    - name: commit
  steps:
    - name: clone
      ref:
        name: git-clone
      params:
        - name: url
          value: $(params.repo)
        - name: revision
          value: main
```

Resolution rules tkn-act mirrors:

| Aspect | Behavior |
|---|---|
| **Reference site** | A `Step` with `ref:` set. The Step provides `name` (always; identifies the Step in events / scripts), and may provide `params:` (key=value), `onError:`, `volumeMounts:`. |
| **Inlined fields** | `image`, `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources`, `results` come from the referenced StepAction. |
| **Step.params application** | The StepAction's `params:` declarations supply names + defaults; the calling Step's `params:` supply values. Substitution happens inside the StepAction body in the same pass that handles Task-level params. |
| **Results contribution** | The referenced StepAction's `results:` block is treated identically to inline `Step.results`: each result gets a per-step results path under `/tekton/steps/<step-name>/results/<result-name>`, and is exposed to later steps via `$(steps.<step-name>.results.<name>)`. |
| **Both `ref:` and inline** | Invalid — same Step cannot set `ref:` and `image:`/`script:`/`command:`/`args:`. Validator error. |
| **Missing ref** | A Step refers to a StepAction whose name is not in the bundle → validator error (exit 4). |
| **Recursive ref** | A StepAction's body cannot itself contain `ref:` (we don't model that field on StepAction.spec). Schema-level rejection at load time. |
| **Calling-side overrides of inlined fields** | Out of scope. Tekton itself permits a small set (e.g. `onError`); tkn-act starts with the strict "ref is the source of truth for the body" rule and adds the override list when a user asks. |

This matches Tekton ≥0.50 client-side semantics; the only deliberate
narrowing is that we do not implement `ref:` overrides for body fields
in v1.

---

## 3. Architecture

### 3.1 Type additions

`internal/tektontypes/types.go`:

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
// is honored in v1 (resolver-based forms come in Track 1 #9).
type StepActionRef struct {
    Name string `json:"name"`
}
```

`Step` gains two fields:

```go
type Step struct {
    Name string `json:"name"`
    // Ref, when set, points to a StepAction whose body is inlined into
    // this Step before substitution. Mutually exclusive with Image /
    // Command / Args / Script: a Step is either inline or a reference.
    Ref *StepActionRef `json:"ref,omitempty"`
    // Params is the list of values bound into the referenced StepAction's
    // declared params. Ignored when Ref is nil.
    Params []Param `json:"params,omitempty"`

    Image           string         `json:"image,omitempty"` // was required; now omitempty (ref-Steps don't set it)
    // ... existing fields unchanged ...
}
```

The single non-obvious diff: `Step.Image` becomes `omitempty`. Steps
with `Ref:` legitimately leave it empty; steps without `Ref:` get
caught by the validator (rule #12 below).

### 3.2 Loader

`internal/loader/loader.go`:

- Bundle gains `StepActions map[string]tektontypes.StepAction`.
- `loadOne` adds an `apiVersion: tekton.dev/v1beta1` arm. Inside that
  arm, only `kind: StepAction` is accepted; other v1beta1 kinds are
  rejected with the same shape of error the loader uses today.
- Duplicate StepAction names (within a file or across `-f` files)
  are an error, mirroring the Task / Pipeline rules.

### 3.3 Engine resolution algorithm

A new package-internal function `resolveStepActions` runs on every
TaskSpec the engine handles, **before** `applyStepTemplate`:

```
loaded YAML
   ↓
loader.Bundle{Tasks, Pipelines, StepActions, ...}
   ↓
engine.runOne / uniqueImages
   ↓ lookupTaskSpec(bundle, pt)        ─ returns TaskSpec as authored
   ↓ resolveStepActions(spec, bundle)  ─ new: inlines every Step.Ref
   ↓ applyStepTemplate(spec)           ─ existing
   ↓ substituteSpec(spec, taskCtx)     ─ existing
   ↓ backend.RunTask(...)
```

`resolveStepActions(spec TaskSpec, b *loader.Bundle) (TaskSpec, error)`:

1. Make a defensive copy of `spec.Steps`.
2. For each Step `st`:
   - If `st.Ref == nil`: keep `st` unchanged.
   - If `st.Ref != nil`:
     - Look up `b.StepActions[st.Ref.Name]`. If absent, return an
       error `step %q references unknown StepAction %q`.
     - Build the resolved Step:
       - **Identity**: keep `st.Name`, `st.OnError` (these are
         intrinsically per-Step).
       - **Body**: copy `Image`, `Command`, `Args`, `Script`,
         `WorkingDir`, `ImagePullPolicy`, `Resources` from
         `StepAction.spec`.
       - **VolumeMounts**: **union** — StepAction body's
         `volumeMounts` first, then the calling Step's appended.
         Mirrors Tekton's behavior; lets catalog StepActions that
         ship `volumeMounts:` work without forcing the caller to
         re-declare them. See §9 open-question #3.
       - **Env**: union by `Name`, **Step's `env` not used** in v1 —
         per the non-goals above, the calling Step does not override
         body fields. The StepAction's `env` is taken verbatim. (If
         a future iteration wants to allow Step env to win on
         conflict, change this to `mergeEnv(action.Env, st.Env)`.)
       - **Results**: copy `StepAction.spec.Results` onto
         `resolvedStep.Results` so the existing per-step results dir
         creation in the docker backend just works.
     - Apply `Step.Params` to the resolved body:
       - Build a `resolver.Context` with `Params` populated from the
         StepAction's declared `params` (defaults first, then
         `Step.Params` overrides). This is a **second** substitution
         pass scoped to the referenced step — earlier passes don't
         see StepAction param names.
       - Run `resolver.SubstituteAllowStepRefs` over `Image`,
         `WorkingDir`, `Script`, and each `Env.Value`; run
         `resolver.SubstituteArgsAllowStepRefs` over `Args` and
         `Command`. **Critical:** the AllowStepRefs variants are
         load-bearing here. A real StepAction body will reference
         outer scopes — `$(workspaces.source.path)`,
         `$(step.results.X.path)`, `$(steps.prev.results.foo)`,
         `$(context.taskRun.name)`, `$(tasks.X.results.Y)`, and
         outer `$(params.<task-param>)` — which the inner pass must
         leave intact so the existing `substituteSpec` pass that runs
         immediately afterward can resolve them under the full Task
         scope. Plain `resolver.Substitute` would error on every one
         of these refs and break common StepAction bodies (including
         the §7.2 fixture's `$(step.results.greeting.path)`).
       - The inner pass therefore only rewrites `$(params.<inner>)`
         refs whose `<inner>` matches a StepAction-declared param
         name. To avoid false matches against outer
         `$(params.<task-param>)` refs that happen to share a name,
         the inner pass populates `rctx.Params` with **only** the
         StepAction's declared params (defaults + caller bindings).
         Outer params that aren't shadowed by an inner declaration
         pass through untouched: AllowStepRefs treats unknown
         `params.<name>` as a deferred placeholder and leaves the
         literal `$(params.<name>)` in the body for the outer pass.
       - `Step.Params` values themselves can carry `$(params.X)` /
         `$(tasks.X.results.Y)` from the surrounding Task. Those
         outer references are forwarded into the StepAction body
         **as literal substitution-tokens** — the inner Context's
         `Params[<inner>]` is set to the raw `Step.Params[i].Value.StringVal`
         (e.g. the literal string `$(params.repo)`). The inner pass
         then substitutes `$(params.<inner>)` → `$(params.repo)`
         in the body, and the outer `substituteSpec` pass resolves
         `$(params.repo)` from the Task scope. **Do not** pre-resolve
         caller param values before forwarding them, or outer
         references inside them are evaluated against the wrong
         scope (and miss outer-scope tokens like
         `$(tasks.X.results.Y)` that aren't in scope at the inner
         pass's substitution site).
       - **AllowStepRefs widening note.** Today
         `SubstituteAllowStepRefs` defers only `$(step.results.X.path)`
         and `$(steps.X.results.Y)` placeholders; it still errors on
         unknown `params.X`, `workspaces.X.path`, `context.X`, and
         `tasks.X.results.Y`. The inner pass needs all of those
         deferred too — otherwise a StepAction body that references
         `$(workspaces.source.path)` blows up at the inner pass.
         This spec widens `SubstituteAllowStepRefs` /
         `SubstituteArgsAllowStepRefs` (in `internal/resolver/resolver.go`)
         to also defer unknown `params.<name>`, any `workspaces.<name>.path`,
         any `context.<name>`, and any `tasks.<name>.results.<name>`
         token to the outer pass. The plain `Substitute` /
         `SubstituteArgs` functions used by `substituteSpec` keep
         their strict error-on-unknown semantics, so outer-pass
         typos are still caught.

       Trace example (caller forwards an outer ref):
       ```yaml
       # StepAction body:
       script: 'git clone $(params.url) /workspace/source'
       # Caller:
       steps:
         - name: clone
           ref: { name: git-clone }
           params:
             - { name: url, value: $(params.repo) }
       ```
       1. Inner Context: `Params = {"url": "$(params.repo)"}` —
          the LITERAL string, not pre-resolved.
       2. Inner pass: `$(params.url)` → `$(params.repo)`. Other
          tokens (none here, but `$(workspaces.X.path)` etc.)
          would survive verbatim.
       3. After `applyStepTemplate` runs.
       4. Outer `substituteSpec`: `$(params.repo)` resolves against
          the Task scope to (e.g.) `https://example.com/foo.git`.
       5. Final script: `git clone https://example.com/foo.git /workspace/source`.
   - Validate: a Step that has `Ref != nil` must not also have any
     of `Image`, `Command`, `Args`, `Script`, `WorkingDir`,
     `ImagePullPolicy`, `Resources`, `Env`, or `Results` set. If any
     of these is non-zero, return an error
     `step %q: ref and inline body are mutually exclusive`.
3. Return the new TaskSpec with `Steps` replaced.

`uniqueImages` calls the same expansion path so referenced
StepAction images are pre-pulled.

### 3.4 Validator rules

`internal/validator/validator.go` gains six rules (numbered to
continue the existing 1..11 list):

- **Rule 12.** Every Step with `Ref:` set must have `Ref.Name`
  populated and that name must be present in `bundle.StepActions`.
  Error message: `pipeline task %q step %q: references unknown StepAction %q`.
- **Rule 13.** Every Step with `Ref:` set must not also set any
  body field (`Image`, `Command`, `Args`, `Script`, `WorkingDir`,
  `ImagePullPolicy`, `Resources`, `Env`, or `Results`). Error
  message: `pipeline task %q step %q: ref and inline body are
  mutually exclusive`.
- **Rule 14.** Every Step with `Ref:` set must supply every required
  StepAction param (StepAction params with `default == nil`). Same
  error shape as the existing required-task-param rule:
  `pipeline task %q step %q: missing required StepAction param %q`.
- **Rule 16 (paired with `Step.Image omitempty`).** Every Step
  WITHOUT `Ref:` set must have a non-empty `Image:` after
  `stepTemplate` inheritance. Without this rule, the JSON-tag
  relaxation from required → `omitempty` on `Step.Image` (made so
  ref-Steps can leave it empty) would silently let an inline Step
  with `image: ""` reach the docker backend, where it surfaces as
  an opaque `Cannot connect to ...` / image-pull error. Error
  message: `pipeline task %q step %q: inline step has no image (set image: or use ref:)`.
  The check runs **after** the validator's own `stepTemplate` merge
  (so a Step that inherits an image from `Task.spec.stepTemplate.image`
  passes).
- **Rule 17 (resolver-form `ref:` rejection).** Today `sigs.k8s.io/yaml`
  silently drops unknown fields under `Step.ref`. A Step authored
  as `ref: { resolver: hub, ... }` parses as `StepActionRef{Name: ""}`,
  then trips Rule 12 with the confusing message `references unknown
  StepAction ""`. To produce a clear error, the validator scans the
  raw `ref:` map (re-marshaled per Step that has `Ref` set) for any
  of `resolver`, `params`, or `bundle` keys; if any is present, it
  emits `pipeline task %q step %q: resolver-based StepAction refs
  (resolver/params/bundle under ref:) are not supported in this
  release; see Track 1 #9`. Implementation: keep a parallel
  `rawSteps []map[string]any` view per Task during validation
  (cheap re-unmarshal) so we can read the original keys before
  they're dropped by typed unmarshaling.

The validator runs `resolveStepActions` itself only to test ref
existence and the mutual-exclusion rule; full body resolution is the
engine's job.

Loop detection (a StepAction cannot reference another StepAction) is
schema-enforced: `StepActionSpec` has no `Ref` field. Any `ref:`
inside a StepAction body parses as an unknown field; `sigs.k8s.io/yaml`
default settings ignore it. We pin this with a build-time guard:

- **Rule 15.** A StepAction body must not contain `ref:`. Enforced
  structurally: `StepActionSpec` does not model `Ref`. The unit test
  `TestValidateStepActionNoNestedRef` uses `reflect` over
  `StepActionSpec` to fail-fast if anyone adds a `Ref` field. **No
  runtime post-load scan is performed** (earlier drafts proposed
  one; we picked the build-time guard for simplicity and to avoid
  re-marshal overhead per StepAction). Plan and spec agree: the
  rule lives in the type schema + reflection test only.

**Default-type handling for `ParamSpec` on StepActions.** The inner
pass reads `decl.Default.StringVal` to seed the inner Context. Array
and object defaults on a StepAction's `params:` are therefore
silently dropped. To avoid surprise, the validator (and the loader,
defense-in-depth) rejects array/object defaults on StepAction
`ParamSpec` entries at validate time:

- **Rule 18 (StepAction param-default type).** A StepAction
  `ParamSpec` whose `Default` has `Type` of `array` or `object` is
  rejected with `StepAction %q param %q: default type %q is not
  supported (only string defaults)`. v1 honors only string
  defaults; array/object defaults can be added when a real fixture
  asks for them.

Failure: any of these → exit 4 (validate).

### 3.5 Docker backend

No code change. After `resolveStepActions`, every Step the docker
backend sees is a normal inline Step. The per-step results dir
machinery already handles `Step.Results`, so writing to
`$(step.results.<name>.path)` from a script that originated in a
referenced StepAction lands at the same path as if the Task had
declared the result inline — see `internal/backend/docker/results.go`.

### 3.6 Cluster backend

`internal/backend/cluster/run.go`'s `inlineTaskSpec` already
serializes the resolved `TaskSpec` (post-StepAction expansion in this
spec) via `taskSpecToMap` → `json.Marshal`. Because the engine
expands StepActions **before** handing the spec to the cluster
backend, the resulting `pipelineSpec.tasks[].taskSpec.steps[]`
contains plain inlined Steps. Tekton accepts it as a normal
EmbeddedTask spec — no need to also `kubectl apply` the StepAction
into the per-run namespace and let the controller resolve it.

**Why client-side expansion (a) over submit-as-objects (b)?**

- **Identity of behavior across backends**: with (a) the docker and
  cluster backends both consume the *same* expanded Step. There is
  no class of bug where the cluster Tekton controller resolves a
  StepAction differently than our engine does. (a) collapses the
  "is this a tkn-act bug or a Tekton bug?" question to a single
  expansion site.
- **Doesn't depend on cluster API support level**: not every Tekton
  controller has StepActions enabled by default (the feature flag
  was opt-in for several minor versions). With (a) we work on any
  Tekton ≥ the version we already require.
- **Same submission shape as inline Steps**: we don't have to teach
  the cluster backend a third object type; existing `kubectl apply`
  / namespace teardown logic is unchanged.
- **Trade-off accepted**: (b) would let us validate against
  StepActions defined in the cluster (not just in `-f` files). v1
  doesn't promise that — `taskRef.name` is also strictly local —
  and adding that contract belongs with Track 1 #9 (resolvers).

### 3.7 Resolution-order property

The contract is: **StepActions resolve before everything else.**
Specifically, every transformation that already mutates a
TaskSpec — `applyStepTemplate`, `substituteSpec`, validator rules
that walk `spec.Steps` — sees a TaskSpec where every Step has its
body. This is the same property `applyStepTemplate` already enjoys
relative to `substituteSpec`; we extend it one rung outward. No
existing pass needs to learn about `Step.Ref`.

---

## 4. Agent contract

No new event kinds. No new exit codes. The resolved Step is reported
through the same `step-start` / `step-end` / `step-log` stream a normal
inline Step uses. `step.task` is the Task name; `step.step` is the
**calling** Step's `name` (never the StepAction's `metadata.name`),
because that's the identity the user wrote in their pipeline and the
identity that flows into `$(steps.<name>.results.<name>)` references.

`tkn-act help-json` and `tkn-act doctor -o json` shapes are unchanged
(no new flags; StepActions are loaded via `-f` like every other
Tekton resource).

---

## 5. Pretty output

No change. Each resolved Step prints exactly as if it had been written
inline, including `step-start <task>/<step>` / `step-end` markers and
log-line prefixes. The pretty reporter never displays the
`StepAction` object.

---

## 6. Failure modes

| Symptom | Exit code | Where caught |
|---|---|---|
| Step `ref:` to an unknown StepAction name | 4 (validate) | Rule 12 |
| Step has both `ref:` and inline body | 4 (validate) | Rule 13 |
| Step `ref:` is missing a required StepAction param | 4 (validate) | Rule 14 |
| StepAction body itself contains `ref:` (loop) | build/test-time | Rule 15 (StepActionSpec schema + reflection guard) |
| Inline Step (no `ref:`) with no `image:` after stepTemplate inheritance | 4 (validate) | Rule 16 |
| Step `ref:` uses resolver form (`ref: {resolver: hub, ...}`) | 4 (validate) | Rule 17 |
| StepAction `ParamSpec.Default` is array/object | 4 (validate) | Rule 18 |
| Two StepActions with the same `metadata.name` in the bundle | 4 (validate, loader-level) | Loader duplicate-check |
| StepAction script writes to `$(step.results.X.path)` for an undeclared result | 5 (pipeline) | Engine: same path as today's Step.Results |

---

## 7. Test plan

### 7.1 Unit tests

| Package | What |
|---|---|
| `internal/tektontypes` | YAML round-trip: `kind: StepAction` parses into `StepAction{Spec: {...}}`; `Step{Ref: {Name: foo}, Params: [...]}` parses; `Step.Image` round-trips empty when `Ref` is set. |
| `internal/loader` | (a) StepAction doc lands under `Bundle.StepActions[name]`. (b) Duplicate StepAction across docs → error. (c) Unknown v1beta1 kind → error. |
| `internal/engine` (new `step_action.go` + `_test.go`) | Tests for `resolveStepActions`: (a) absent ref → no-op; (b) present ref → body inlined, identity fields preserved; (c) StepAction defaults applied for params Step did not bind; (d) Step-supplied params override defaults; (e) `$(params.X)` *inside* the StepAction body resolves against the StepAction-scoped params, not the surrounding Task; (f) results from the StepAction surface as `Step.Results` on the inlined Step; (g) ref-and-inline is rejected; (h) **outer-scope tokens survive the inner pass** — `TestResolveStepActionsLeavesOuterRefsIntact` exercises a StepAction whose body uses every outer-scope token (`$(workspaces.source.path)`, `$(step.results.X.path)`, `$(steps.prev.results.foo)`, `$(context.taskRun.name)`, `$(tasks.X.results.Y)`, and an outer `$(params.<task-param>)`) and asserts each survives `resolveStepActions` verbatim and is then resolved correctly by the existing `substituteSpec` pass; (i) **caller-side outer refs forward as literals** — `TestResolveStepActionsForwardsOuterParamRefAsLiteral`: caller writes `params: [{name: who, value: $(params.repo)}]`; the inner Context's `Params["who"]` MUST be the literal string `$(params.repo)`; the inner pass rewrites `$(params.who)` in the body to `$(params.repo)`; the outer pass then resolves it from the Task scope; (j) **two Steps reference the same StepAction** — `TestResolveStepActionsTwoStepsSameAction`: a Task with two Steps both referencing `git-clone` (one named `clone1`, one `clone2`, each with distinct `params:` values) produces two distinct inlined Steps with distinct per-step results paths (`/tekton/steps/clone1/results/commit` vs `/tekton/steps/clone2/results/commit`); confirms the per-step results dir is keyed on calling-Step name, not StepAction name. |
| `internal/engine` integration | Run a Pipeline whose Task uses `ref:` and assert the backend captureBackend received an inlined Step shape (image and script from the StepAction, name from the Step, env merged correctly). |
| `internal/resolver` | New tests for the widened `SubstituteAllowStepRefs` / `SubstituteArgsAllowStepRefs`: each unknown `params.<name>`, `workspaces.<name>.path`, `context.<name>`, and `tasks.<name>.results.<name>` token is left intact (deferred), and a known `params.<inner>` is still resolved. |
| `internal/validator` | Rules 12–18: each violation surfaces a distinct error message. Rule 16 test: `Task{steps: [{name: bad, script: echo}]}` (no image, no ref, no inheritance) → exit 4 with `inline step has no image`. Rule 16 positive: a Step with `image:` inherited from `Task.spec.stepTemplate.image` passes. Rule 17 test: `ref: { resolver: hub }` → exit 4 with the resolver-not-supported message. Rule 18 test: a StepAction `params: [{name: x, default: [a, b]}]` → exit 4 with the default-type error. |
| `internal/backend/cluster` | Regression test: when a Task references a StepAction, `BuildPipelineRunObject` produces `pipelineSpec.tasks[].taskSpec.steps[<i>]` with the inlined image/script and **no** `ref:` field. |

### 7.2 Cross-backend e2e fixture

`testdata/e2e/step-actions/pipeline.yaml`:

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

Add to `internal/e2e/fixtures/All()`:

```go
{Dir: "step-actions", Pipeline: "step-actions", WantStatus: "succeeded"},
```

**Cross-backend fidelity (checked invariant, not aspiration).** The
`step-actions` row in `internal/e2e/fixtures.All()` is consumed by
both the docker e2e harness (`-tags integration`) and the cluster
e2e harness (`-tags cluster`). On the cluster side, the harness:

1. Runs the pipeline via `tkn-act run --cluster -f <fixture>` and
   asserts the same `WantStatus`.
2. Inspects the `PipelineRun` object the cluster backend submitted
   (via `kubectl get pipelinerun -o yaml` against the ephemeral
   namespace) and asserts that under
   `spec.pipelineSpec.tasks[].taskSpec.steps[]` there is **no**
   `ref:` field on any Step — i.e. expansion happened client-side
   before submission. The Step's `image:` and (substring of)
   `script:` are taken from the StepAction body, not the calling
   Step.

This pins Important 5 ("Cluster fidelity claim needs test
enforcement"): the engine-side expansion isn't merely documented as
the design — the cluster harness fails the build if the cluster
backend ever stops receiving the inlined shape. **Documented
limitation:** because tkn-act expands StepActions client-side, its
behavior matches Tekton's client-side semantics for the Step-field
subset tkn-act reads (the seven body fields enumerated in §2);
controller-side resolver behavior (resolver-fetched StepActions,
admission webhooks that mutate StepAction objects in the cluster)
is intentionally out of scope, since tkn-act never applies a
`kind: StepAction` object into the per-run namespace.

### 7.3 Parity row

In `docs/feature-parity.md`, flip the existing `StepActions` row from
`gap` → `shipped`, set the e2e fixture to `step-actions`, leave the
limitations cell `none`, and link to the plan
(`docs/superpowers/plans/2026-05-04-step-actions.md`).

### 7.4 CI gates

- `tests-required` — unit + e2e tests cover every new Go file.
- `coverage` — engine package gains a new file; new file ships with
  its own tests so per-package coverage cannot drop.
- `parity-check` — flipping the StepActions row to `shipped` requires
  the `step-actions` fixture to be present and to appear in
  `internal/e2e/fixtures.All()`. No `testdata/limitations/step-actions/`
  directory needs to be deleted (none exists today).

---

## 8. Documentation updates required

This spec ships with same-PR updates to:

| File | Change |
|---|---|
| `docs/feature-parity.md` | Flip the StepActions row to `shipped` + e2e fixture name + plan link. |
| `docs/short-term-goals.md` | Mark Track 1 #8 done with the v1.6 note. |
| `docs/test-coverage.md` | Add a row under the integration-tag table for `step-actions/`. |
| `AGENTS.md` | New section "StepActions" mirroring the `stepTemplate` section's layout: what's supported, what's deferred (resolvers), and the merge rules in a one-table summary. |
| `cmd/tkn-act/agentguide_data.md` | Mirror via `go generate ./cmd/tkn-act/` so the `agent-guide` command embeds the same text. |

---

## 9. Open questions for human review

1. **Step-side env override.** Tekton itself lets the calling Step add
   to (and in some cases shadow) the StepAction's `env`. This spec
   takes the strict "ref is the body" stance for v1 — Step `env` is
   ignored when `Ref` is set. Should we instead implement
   `mergeEnv(action.Env, step.Env)` (Step wins on conflict, mirroring
   `stepTemplate`)? My recommendation is to wait until a real fixture
   asks for it; the strict rule is easier to undo than a permissive
   one is to tighten.
2. **`onError` from the calling Step.** Today we keep
   `Step.OnError` from the calling Step (since it's an
   identity/per-Step concern). Tekton agrees. Confirm this is the
   right call.
3. **`volumeMounts` on the calling Step + StepAction body.**
   *Decision: implement union (matches Tekton).* StepAction body's
   `volumeMounts` are appended to the calling Step's
   `volumeMounts` in `inlineStepAction` — StepAction's mounts come
   first, caller's appended. Conflicting mount paths are not
   detected (Tekton itself doesn't pre-validate this; the kubelet
   surfaces it). This was previously left open; reviewer feedback
   selected option (a) union over option (b) load-time rejection of
   `volumeMounts` in `StepActionSpec`, because it matches Tekton's
   actual behavior and doesn't break catalog StepActions that ship
   `volumeMounts:`. Test: a StepAction declaring `volumeMounts: [{name: tmp, mountPath: /tmp}]`
   and a caller Step adding `volumeMounts: [{name: cache, mountPath: /cache}]`
   yields a resolved Step with both mounts present.
4. **Result-name collisions across multiple ref-Steps in one Task.**
   *Decision: keep current per-Step-name keying (Tekton's natural
   behavior).* Two referenced StepActions could both declare
   `result: commit`, but the per-step dir is keyed on the calling
   Step's `name`, not the result name, so two Steps with `name:
   clone1` / `clone2` both referencing `git-clone` get distinct
   `/tekton/steps/clone1/results/commit` and
   `/tekton/steps/clone2/results/commit` paths. Pinned by the
   `TestResolveStepActionsTwoStepsSameAction` unit test in §7.1.
5. **`apiVersion: tekton.dev/v1` form.** Tekton is in the process of
   graduating StepActions; the v1 form is appearing in some catalogs.
   Should the loader accept both `v1beta1` and `v1` for `kind:
   StepAction` from day one? Cheap, low-risk; but it commits us to
   any v1-only fields that show up later.
6. **Strict rejection of resolver-form `ref:`.** *Decision:
   shipped as Rule 17 in §3.4.* A `Step.ref: { resolver: hub, ... }`
   silently parses as `StepActionRef{Name: ""}` under
   `sigs.k8s.io/yaml`'s default unknown-field behavior; without a
   dedicated rule it would trip Rule 12 with "references unknown
   StepAction \"\"". Rule 17 scans the raw `ref:` map per Step and
   emits a clear "resolver-based StepAction refs are not supported
   in this release; see Track 1 #9" error. No longer an open
   question.

---

## 10. Rollout

Single PR, single squash commit. The plan in
`docs/superpowers/plans/2026-05-04-step-actions.md` lists every
bite-sized TDD task; the work fits under the existing `tests-required`,
`coverage`, and `parity-check` gates without adjustment.
