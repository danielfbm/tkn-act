# `Task.sidecars` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `Task.spec.sidecars` — long-lived helper containers that share the Task's pod / network namespace for the duration of the Task — on both the docker and cluster backends. Steps must reach a sidecar at `localhost:<port>` exactly the way upstream Tekton allows.

**Architecture:** New `tektontypes.Sidecar` type and `TaskSpec.Sidecars` field. Engine threads sidecars through to the backend via the existing `TaskInvocation.Task` field, pre-pulls sidecar images, and runs `$(params.X)` substitution on sidecar fields the same way it does for steps. Docker backend gains a per-Task lifecycle built around a tiny **pause container** that owns the network namespace (mirrors upstream Kubernetes / Tekton's "infra container" model — chosen over the previous "first-sidecar-as-netns-owner" sketch so any sidecar can crash without disrupting the Task):

1. Workspace prep (existing).
2. Pull pause + sidecar images (the pause image — `gcr.io/google-containers/pause:3.9`, ~700KB, cached after first pull — is added by the docker backend itself; sidecar images are added by the engine via `uniqueImages`).
3. Start the **pause container** with normal Docker networking.
4. Start every sidecar in declaration order, each `network_mode: container:<pause-id>`.
5. Wait `--sidecar-start-grace` (default 2s) before launching the first Step.
6. Run each Step with `network_mode: container:<pause-id>` so `localhost:<port>` resolves to any sidecar.
7. Inter-step liveness check: any sidecar crash records `sidecar-end` but does **NOT** fail the Task (matches upstream "sidecars are best-effort"). Only a pause-container exit (defense-in-depth path) infrafails the Task.
8. Per-Task teardown: SIGTERM each sidecar, SIGKILL after `--sidecar-stop-grace` (default 30s, matches upstream `terminationGracePeriodSeconds`), remove. Then SIGTERM + SIGKILL the pause container with a hard 1s grace.
9. Workspace teardown (existing, AFTER sidecars are gone so no fd holds the rmdir).

Cluster backend: (a) **pass-through** of the `sidecars` list — `taskSpecToMap` is `json.Marshal`-based so adding the field to `TaskSpec` makes `pipelineSpec.tasks[].taskSpec.sidecars[]` appear automatically; Tekton's `EmbeddedTask` schema accepts `sidecars` natively. (b) **`sidecar-start` / `sidecar-end` events MUST ship on cluster too** (cross-backend fidelity is a hard project rule) — read from `taskRun.status.sidecars[]` via a new pure helper `parsePodSidecarStatuses` (factored out for an untagged unit test that counts toward the per-package coverage gate).

Three new JSON event kinds (`sidecar-start`, `sidecar-end`, `sidecar-log`) carry sidecar lifecycle on the stable agent contract; no new exit codes; no engine policy-loop change. The exit-code mapping in `cmd/tkn-act/run.go` is verified by a unit test to NOT collide between sidecar-driven `infrafailed` (exit 1) and `Task.spec.timeout` driven `timeout` (exit 6).

**Tech Stack:** Go 1.25, no new dependencies. Reuses `internal/engine`, `internal/tektontypes`, `internal/validator`, `internal/backend/docker`, `internal/backend/cluster`, the cross-backend `internal/e2e/fixtures` table, and the existing `parity-check` + `tests-required` + `coverage` CI gates.

---

## Track 1 #1 context

This closes Track 1 #1 of `docs/short-term-goals.md` — the highest-priority remaining Tekton parity gap. The Status column says: *"Out of scope in v1.2; documented in `testdata/limitations/sidecars/`. Needs design work for the docker backend (per-Task network + shared netns). Cluster mode already works."*

This plan implements the design in `docs/superpowers/specs/2026-05-04-task-sidecars-design.md`. Read that first; this plan is the bite-sized path.

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `Sidecar`, `WorkspaceUsage`, `ContainerPort` types; add `Sidecars` field on `TaskSpec` |
| `internal/tektontypes/types_test.go` | YAML round-trip for `sidecars` |
| `internal/validator/validator.go` | New rules: sidecar `image` required, name unique within Task and not colliding with step names, `volumeMounts` reference declared volumes |
| `internal/validator/validator_test.go` | Negative + positive coverage for the three new rules |
| `internal/engine/engine.go` | `uniqueImages` walks sidecar images; `substituteSpec` walks sidecar fields |
| `internal/engine/engine_test.go` (or new file) | Unit: sidecar images appear in pre-pull set; sidecar `$(params.X)` substituted before backend |
| `internal/reporter/event.go` | Add `EvtSidecarStart`, `EvtSidecarEnd`, `EvtSidecarLog` constants |
| `internal/reporter/reporter_test.go` | Assert JSON encoding of the three new event kinds |
| `internal/reporter/pretty.go` | Tag sidecar log lines, surface sidecar-end on non-zero/infrafail |
| `internal/reporter/pretty_test.go` | Pretty rendering for sidecar events |
| `internal/backend/backend.go` | Add `SidecarLog(taskName, name, stream, line)` to `LogSink` (mirrors `StepLog`); reporter implements it |
| `internal/backend/docker/sidecars.go` (new) | Per-Task pause container + sidecar lifecycle: start, liveness, teardown |
| `internal/backend/docker/sidecars_helpers_test.go` (new, untagged) | Pure-helper unit tests for `sidecarContainerName`, `pauseContainerName` — coverage-gate friendly |
| `internal/backend/docker/sidecars_integration_test.go` (new, `-tags integration`) | Integration: a Task with redis sidecar; a Task whose sidecar fails to start (`infrafailed`); a Task whose sidecar exits mid-Task but Task still succeeds (matches upstream "sidecars are best-effort") |
| `internal/backend/docker/docker.go` | `RunTask` invokes the new pause + sidecar lifecycle around the existing step loop; `Prepare` is unchanged on the engine side (pause image pulled by the docker backend itself, before user images) |
| `internal/backend/cluster/run.go` | (a) Add `sidecar-start` / `sidecar-end` event emission from `taskRun.status.sidecars[]` in the per-TaskRun watch loop. (b) (Stretch) sidecar log streaming branch in `streamPodLogs` (one line — `step-` prefix becomes `step-`/`sidecar-`) — defer to follow-up if non-trivial. |
| `internal/backend/cluster/sidecars_status.go` (new) | Pure helper `parsePodSidecarStatuses(taskRun *unstructured.Unstructured) []SidecarStatus`. Factored OUT of `run.go`'s loop so it can be unit-tested untagged (cluster integration tests are `-tags cluster` and don't count toward the per-package coverage gate). |
| `internal/backend/cluster/sidecars_status_test.go` (new, untagged) | Unit test for `parsePodSidecarStatuses`: a fixture `unstructured.Unstructured` with `status.sidecars[]` containing a `running` sidecar and a `terminated` sidecar produces the right `SidecarStatus` entries (state, exitCode, name). Counts toward coverage gate. |
| `internal/backend/cluster/runpipeline_test.go` | Regression: `pipelineSpec.tasks[].taskSpec.sidecars[]` survives `taskSpecToMap` |
| `cmd/tkn-act/exit_test.go` (new or extend existing) | Unit test: a sidecar-driven `infrafailed` task exits **1**; a `Task.spec.timeout` driven `timeout` exits **6**. The two paths do NOT collide despite both showing up as `infrafailed` per-event status on intermediate events. |
| `internal/e2e/fixtures/fixtures.go` | Add `sidecars` fixture entry |
| `testdata/e2e/sidecars/pipeline.yaml` | Cross-backend fixture: redis sidecar, steps connect on localhost |
| `testdata/limitations/sidecars/` | DELETE the directory (graduation rule — `parity-check` enforces it) |
| `cmd/tkn-act/run.go` | New `--sidecar-start-grace` flag (`time.Duration`, default `2s`) AND new `--sidecar-stop-grace` flag (`time.Duration`, default `30s`, matches upstream `terminationGracePeriodSeconds`); thread both through to `docker.Options` |
| `cmd/tkn-act/helpjson_test.go` | Help-JSON snapshot picks up both new flags |
| `cmd/tkn-act/agentguide_data.md` | Document sidecar support, the pause-container lifecycle model, both start- and stop-grace flags, the new event kinds. Re-run `go generate ./cmd/tkn-act/` after editing `AGENTS.md` so the embedded copy mirrors. |
| `AGENTS.md` | Mirror agentguide_data.md (the `go generate` target keeps them in sync) |
| `docs/test-coverage.md` | Add `sidecars/` row under `### -tags integration`; remove "Sidecars" from the "By design" not-covered bullet |
| `docs/short-term-goals.md` | Mark Track 1 #1 done |
| `docs/feature-parity.md` | Flip `Task.sidecars` row: `gap` → `shipped`, backends `cluster-only` → `both`, `e2e fixture` → `sidecars`, clear `limitations fixture` |
| `README.md` | Move "Sidecars" out of "Not yet supported"; add bullet under "Tekton features supported" |

## Out of scope (don't do here)

- **Probes** (`readinessProbe`, `livenessProbe`, `startupProbe`). Use a fixed start-grace instead. Probe support is a follow-up PR.
- **`Sidecar.ports` semantics.** Parsed and forwarded to cluster; ignored on docker (shared netns means everything's on localhost).
- **`securityContext`, `lifecycle`, `tty`, `stdin` on sidecars.** Same posture as on Steps — parsed by upstream's tolerant JSON, ignored by tkn-act.
- **A separate `sidecar-failure/` e2e fixture.** Unit tests cover the paths; authoring deterministic crash-on-startup with public images is brittle.
- **`Sidecar.results`.** Tekton sidecars don't have results.
- **Cluster-side sidecar log streaming.** Cluster `sidecar-start` / `sidecar-end` event emission is **in scope** (cross-backend fidelity is a hard project rule and is enforced by the cluster integration test running the same `sidecars` fixture). Sidecar log line streaming via `streamPodLogs` is below the stretch-goal line — if extending `streamPodLogs` is non-trivial, defer to a follow-up PR and add an explicit `gap` row in `docs/feature-parity.md`.
- **Renaming any existing event kind.** Reuse `Event.Step` for the sidecar name and `Event.Stream = "sidecar-stdout"` / `"sidecar-stderr"` for the log stream — no new payload fields needed.

---

### Task 1: Add `Sidecar` types

**Files:**
- Modify: `internal/tektontypes/types.go`
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tektontypes/types_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestUnmarshalTaskWithSidecars ./internal/tektontypes/...
```

Expected: FAIL with `Spec.Sidecars undefined`.

- [ ] **Step 3: Add the types and field**

In `internal/tektontypes/types.go`:

1. Add the new `Sidecars` field to `TaskSpec` (after `StepTemplate`):

```go
type TaskSpec struct {
	// ... existing fields ...
	StepTemplate *StepTemplate `json:"stepTemplate,omitempty"`
	Sidecars     []Sidecar     `json:"sidecars,omitempty"`
}
```

2. Append the new types (after the existing `StepTemplate` struct):

```go
// Sidecar is a long-lived helper container that shares the Task's
// pod/network namespace for the duration of the Task. Mirrors Tekton's
// v1 Sidecar; tkn-act honors the subset listed below on the docker
// backend (image, command/args, script, env, workingDir, resources,
// volumeMounts, workspaces). The cluster backend forwards the full
// marshalled shape — any other field the controller knows about works
// under --cluster regardless of what tkn-act reads.
type Sidecar struct {
	Name            string           `json:"name"`
	Image           string           `json:"image"`
	Command         []string         `json:"command,omitempty"`
	Args            []string         `json:"args,omitempty"`
	Script          string           `json:"script,omitempty"`
	Env             []EnvVar         `json:"env,omitempty"`
	WorkingDir      string           `json:"workingDir,omitempty"`
	Resources       *StepResources   `json:"resources,omitempty"`
	VolumeMounts    []VolumeMount    `json:"volumeMounts,omitempty"`
	Workspaces      []WorkspaceUsage `json:"workspaces,omitempty"`
	Ports           []ContainerPort  `json:"ports,omitempty"`
	ImagePullPolicy string           `json:"imagePullPolicy,omitempty"`
}

// WorkspaceUsage is the per-container workspace declaration used by
// Sidecar. Tekton's Step takes its workspace bindings from the
// PipelineTask, so this type is sidecar-only for now.
type WorkspaceUsage struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath,omitempty"`
	SubPath   string `json:"subPath,omitempty"`
}

// ContainerPort is a fidelity-only stub for upstream's
// corev1.ContainerPort. tkn-act records the bytes and forwards them
// to the cluster backend; no semantic effect on docker.
type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestUnmarshalTaskWithSidecars ./internal/tektontypes/...
go test -run TestUnmarshalTaskWithoutSidecars ./internal/tektontypes/...
go vet ./...
```

Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add TaskSpec.Sidecars (Tekton v1)"
```

---

### Task 2: Validator rules

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

Three rules: image required, name unique (incl. no collision with step names), volumeMounts reference declared volumes.

- [ ] **Step 1: Write failing tests**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateSidecarRequiresImage(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: ""}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for empty sidecar image")
	}
}

func TestValidateSidecarNameUnique(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: redis:7-alpine}
    - {name: redis, image: redis:7-alpine}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for duplicate sidecar name")
	}
}

func TestValidateSidecarNameCollidesWithStep(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: shared, image: redis:7-alpine}
  steps:
    - {name: shared, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for sidecar name colliding with step name")
	}
}

func TestValidateSidecarVolumeMountResolves(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - name: redis
      image: redis:7-alpine
      volumeMounts:
        - {name: undeclared, mountPath: /data}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for sidecar volumeMount referencing undeclared volume")
	}
}

func TestValidateSidecarsValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  volumes:
    - {name: shared, emptyDir: {}}
  sidecars:
    - name: redis
      image: redis:7-alpine
      volumeMounts:
        - {name: shared, mountPath: /data}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: a, taskRef: {name: t}}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

```bash
go test -run 'TestValidateSidecar' -count=1 ./internal/validator/...
```

Expected: FAIL — current validator does not check sidecars.

- [ ] **Step 3: Implement the rules**

In `internal/validator/validator.go`, after the existing volume-validation block (search for the loop that walks `resolvedTasks` and inspects `spec.Volumes`), add a sidecar-validation block:

```go
	// Sidecars: name uniqueness within the Task (and against Step names),
	// image required, and every volumeMount must reference a declared
	// Task-level volume.
	for ptName, spec := range resolvedTasks {
		if len(spec.Sidecars) == 0 {
			continue
		}
		volumeNames := map[string]bool{}
		for _, v := range spec.Volumes {
			volumeNames[v.Name] = true
		}
		stepNames := map[string]bool{}
		for _, st := range spec.Steps {
			stepNames[st.Name] = true
		}
		seen := map[string]bool{}
		for _, sc := range spec.Sidecars {
			if sc.Name == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar has empty name", ptName))
				continue
			}
			if sc.Image == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q: image is required", ptName, sc.Name))
			}
			if seen[sc.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q has duplicate sidecar name %q", ptName, sc.Name))
			}
			seen[sc.Name] = true
			if stepNames[sc.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q collides with a step of the same name", ptName, sc.Name))
			}
			for _, vm := range sc.VolumeMounts {
				if !volumeNames[vm.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q sidecar %q volumeMount %q references undeclared Task volume", ptName, sc.Name, vm.Name))
				}
			}
		}
	}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/validator/...
```

Expected: PASS (all five new tests + existing suite).

- [ ] **Step 5: Commit**

```bash
git add internal/validator/validator.go internal/validator/validator_test.go
git commit -m "feat(validator): require sidecar image; enforce name + volumeMount rules"
```

---

### Task 3: Engine substitutes and pre-pulls sidecar images

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/sidecars_engine_test.go` (new) or extend `engine_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/engine/sidecars_engine_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
)

// captureBackend records the resolved Task every time RunTask fires.
// (Mirrors the helper in step_template_engine_test.go; if the file
// already defines captureBackend, reuse it instead of redeclaring.)
type captureBackendForSidecars struct {
	steps map[string]backend.TaskInvocation
}

func (c *captureBackendForSidecars) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (c *captureBackendForSidecars) Cleanup(_ context.Context) error                    { return nil }
func (c *captureBackendForSidecars) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if c.steps == nil {
		c.steps = map[string]backend.TaskInvocation{}
	}
	c.steps[inv.TaskName] = inv
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}

func TestSidecarParamsSubstitutedBeforeBackend(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params:
    - {name: redis_pass, default: "hunter2"}
  sidecars:
    - name: redis
      image: redis:7-alpine
      env:
        - {name: PASS, value: $(params.redis_pass)}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
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
	be := &captureBackendForSidecars{}
	if _, err := engine.New(be, &sliceSink{}, engine.Options{}).RunPipeline(
		context.Background(), engine.PipelineInput{Bundle: b, Name: "p"},
	); err != nil {
		t.Fatal(err)
	}
	inv := be.steps["t"]
	if len(inv.Task.Sidecars) != 1 {
		t.Fatalf("sidecars = %d, want 1", len(inv.Task.Sidecars))
	}
	if got := inv.Task.Sidecars[0].Env[0].Value; got != "hunter2" {
		t.Errorf("sidecar env value = %q, want hunter2 (substituted)", got)
	}
}
```

(`sliceSink` is the existing helper in `policy_test.go` and reusable here.)

For the pre-pull side, create or append to `internal/engine/uniqueimages_test.go`:

```go
package engine

import (
	"reflect"
	"sort"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
)

func TestUniqueImagesIncludesSidecars(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  sidecars:
    - {name: redis, image: redis:7-alpine}
  steps:
    - {name: s, image: alpine:3, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks: [{name: t, taskRef: {name: t}}]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := uniqueImages(b, b.Pipelines["p"])
	sort.Strings(got)
	want := []string{"alpine:3", "redis:7-alpine"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueImages = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

```bash
go test -count=1 -run 'TestSidecarParamsSubstitutedBeforeBackend|TestUniqueImagesIncludesSidecars' ./internal/engine/...
```

Expected: FAIL.

- [ ] **Step 3: Wire `uniqueImages` to walk sidecars**

In `internal/engine/engine.go` `uniqueImages`, after the `for _, s := range spec.Steps { seen[s.Image] = struct{}{} }` line, add:

```go
		for _, sc := range spec.Sidecars {
			if sc.Image != "" {
				seen[sc.Image] = struct{}{}
			}
		}
```

- [ ] **Step 4: Wire `substituteSpec` to walk sidecars**

In `internal/engine/engine.go::substituteSpec` (around line 422), after the existing loop that copies and substitutes `spec.Steps`, add a parallel loop for sidecars. Sidecars do NOT use `SubstituteAllowStepRefs` because step results don't substitute into sidecars (sidecars start before any step):

```go
	if len(spec.Sidecars) > 0 {
		out.Sidecars = make([]tektontypes.Sidecar, len(spec.Sidecars))
		for i, sc := range spec.Sidecars {
			ns := sc
			ns.Image, _ = resolver.Substitute(sc.Image, ctx)
			if len(sc.Command) > 0 {
				ns.Command, _ = resolver.SubstituteArgs(sc.Command, ctx)
			}
			if len(sc.Args) > 0 {
				ns.Args, _ = resolver.SubstituteArgs(sc.Args, ctx)
			}
			ns.Script, _ = resolver.Substitute(sc.Script, ctx)
			ns.WorkingDir, _ = resolver.Substitute(sc.WorkingDir, ctx)
			ns.Env = make([]tektontypes.EnvVar, len(sc.Env))
			for j, e := range sc.Env {
				v, _ := resolver.Substitute(e.Value, ctx)
				ns.Env[j] = tektontypes.EnvVar{Name: e.Name, Value: v}
			}
			out.Sidecars[i] = ns
		}
	}
```

- [ ] **Step 5: Run tests**

```bash
go test -count=1 -race ./internal/engine/...
```

Expected: PASS (every test, including the new ones and the existing suite — including the v1.3 `TestPipelineLevelTimeoutTriggers` etc.).

- [ ] **Step 6: Commit**

```bash
git add internal/engine/engine.go internal/engine/sidecars_engine_test.go internal/engine/uniqueimages_test.go
git commit -m "feat(engine): substitute sidecar fields and pre-pull sidecar images"
```

---

### Task 4: Reporter event kinds + LogSink for sidecars

**Files:**
- Modify: `internal/reporter/event.go`
- Modify: `internal/reporter/pretty.go`
- Modify: `internal/backend/backend.go` (add `SidecarLog` to `LogSink`)
- Test: `internal/reporter/reporter_test.go`, `internal/reporter/pretty_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/reporter/reporter_test.go`:

```go
func TestJSONSidecarEventsEncodeKind(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarStart, Task: "t", Step: "redis"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Stream: "sidecar-stdout", Line: "ready"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "succeeded", ExitCode: 0})
	out := buf.String()
	for _, want := range []string{`"kind":"sidecar-start"`, `"kind":"sidecar-log"`, `"kind":"sidecar-end"`, `"stream":"sidecar-stdout"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got: %s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test -count=1 -run TestJSONSidecarEventsEncodeKind ./internal/reporter/...
```

Expected: FAIL with `EvtSidecarStart undefined`.

- [ ] **Step 3: Add the event kinds**

In `internal/reporter/event.go`, append to the constant block:

```go
	EvtSidecarStart EventKind = "sidecar-start"
	EvtSidecarEnd   EventKind = "sidecar-end"
	EvtSidecarLog   EventKind = "sidecar-log"
```

- [ ] **Step 4: Add `SidecarLog` to the LogSink contract**

In `internal/backend/backend.go`, extend the `LogSink` interface:

```go
type LogSink interface {
	StepLog(taskName, stepName, stream string, line string)
	SidecarLog(taskName, sidecarName, stream string, line string)
}
```

In `internal/reporter/event.go`, implement `SidecarLog` on `LogSink`:

```go
func (s *LogSink) SidecarLog(taskName, sidecarName, stream, line string) {
	s.r.Emit(Event{
		Kind:   EvtSidecarLog,
		Time:   time.Now(),
		Task:   taskName,
		Step:   sidecarName,
		Stream: stream,
		Line:   line,
	})
}
```

Every existing `LogSink` implementer must gain a `SidecarLog` method (no-op stub for fakes; real emission for the production reporter). The complete enumerated in-tree list, audited at plan-write time:

| File | Type | Treatment |
|---|---|---|
| `internal/reporter/event.go` | `*reporter.LogSink` (production) | Real implementation — emit `EvtSidecarLog` (added in Step 3 above). Already specified earlier in this task. |
| `internal/backend/docker/docker_integration_test.go` | `captureLogs` (used by docker integration tests) | Add no-op stub: `func (c *captureLogs) SidecarLog(_, _, _, _ string) {}` |

That's the complete in-tree list. Cross-check with:

```bash
grep -rn 'func.*StepLog' internal/ --include='*.go'
```

If the grep finds an implementer not in the table above (added since this plan was written), add a no-op `SidecarLog` to it too:

```go
func (s *fakeSink) SidecarLog(_, _, _, _ string) {}
```

- [ ] **Step 5: Add pretty rendering for sidecar log lines and events**

In `internal/reporter/pretty.go`, find where `EvtStepLog` is handled and add a parallel branch for `EvtSidecarLog`:

```go
	case EvtSidecarLog:
		// Use ":" instead of "/" between task and sidecar name so
		// mixed step + sidecar logs are visually attributable at a glance.
		fmt.Fprintf(p.w, "%s:%s %s\n", e.Task, e.Step, e.Line)
	case EvtSidecarStart:
		if p.verbosity >= verbosityVerbose {
			fmt.Fprintf(p.w, "%s:%s sidecar started\n", e.Task, e.Step)
		}
	case EvtSidecarEnd:
		if e.Status != string(StatusSucceeded) || e.ExitCode != 0 {
			fmt.Fprintf(p.w, "%s:%s sidecar exited %d (%s)\n", e.Task, e.Step, e.ExitCode, e.Status)
		} else if p.verbosity >= verbosityVerbose {
			fmt.Fprintf(p.w, "%s:%s sidecar exited 0\n", e.Task, e.Step)
		}
```

(The exact field names — `p.w`, `p.verbosity`, `verbosityVerbose` — should match what's already used in `pretty.go` for steps; copy the exact pattern.)

- [ ] **Step 6: Test pretty rendering**

Append to `internal/reporter/pretty_test.go`:

```go
func TestPrettyRendersSidecarLogsAndCrashEvents(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: reporter.VerbosityNormal})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Line: "ready"})
	r.Emit(reporter.Event{Kind: reporter.EvtSidecarEnd, Task: "t", Step: "redis", Status: "failed", ExitCode: 137})
	out := buf.String()
	if !strings.Contains(out, "t:redis ready") {
		t.Errorf("missing sidecar log line; got: %s", out)
	}
	if !strings.Contains(out, "t:redis sidecar exited 137") {
		t.Errorf("missing sidecar-end line; got: %s", out)
	}
}
```

(Adjust `PrettyOptions` / `VerbosityNormal` names to match existing exports.)

- [ ] **Step 7: Run tests**

```bash
go vet ./...
go test -count=1 ./internal/reporter/... ./internal/backend/...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/reporter/event.go internal/reporter/pretty.go internal/reporter/reporter_test.go internal/reporter/pretty_test.go internal/backend/backend.go internal/backend/backend_test.go
# add any other files that gained no-op SidecarLog stubs
git commit -m "feat(reporter): add sidecar-{start,end,log} event kinds and LogSink hook"
```

---

### Task 5: Docker backend — pure-helper unit tests (default test set)

**Files:**
- Create: `internal/backend/docker/sidecars.go`
- Create: `internal/backend/docker/sidecars_helpers_test.go` (UNTAGGED — runs in default `go test`)

The pure helpers exist so the coverage gate (default test set, no build tags) sees coverage for `internal/backend/docker/`. The integration-tagged tests in Task 7 cover the actual Docker plumbing.

- [ ] **Step 1: Write failing helper tests**

Create `internal/backend/docker/sidecars_helpers_test.go` (no build tag):

```go
package docker

import (
	"testing"
)

func TestSidecarContainerName(t *testing.T) {
	got := sidecarContainerName("abc12345", "build", "redis")
	want := "tkn-act-abc12345-build-sidecar-redis"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPauseContainerName(t *testing.T) {
	got := pauseContainerName("abc12345", "build")
	want := "tkn-act-abc12345-build-pause"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPauseImage(t *testing.T) {
	// Pinned to upstream Kubernetes' pause image; ~700KB; cached
	// forever after first pull. See spec §3.1 and Open Question #3.
	if pauseImage != "gcr.io/google-containers/pause:3.9" {
		t.Errorf("pauseImage = %q; pin must match the spec exactly", pauseImage)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test -count=1 -run 'TestSidecar' ./internal/backend/docker/...
```

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement the helpers**

Create `internal/backend/docker/sidecars.go`:

```go
package docker

import (
	"fmt"
)

// pauseImage is the per-Task netns owner. Tiny (~700KB), cached
// forever after first pull, blocks on pause(2) until killed.
// See spec §3.1 and Open Question #3 for provenance.
const pauseImage = "gcr.io/google-containers/pause:3.9"

// sidecarContainerName returns the Docker container name for a sidecar
// of the given task in the given run. Mirrors the step-name format
// tkn-act-<runID:8>-<taskRun>-<step> with "sidecar-" interposed.
func sidecarContainerName(runID, taskRun, sidecarName string) string {
	return fmt.Sprintf("tkn-act-%s-%s-sidecar-%s", runID, taskRun, sidecarName)
}

// pauseContainerName returns the Docker container name for the per-Task
// pause container that owns the netns. Every sidecar and every step in
// the Task joins it via network_mode: container:<id>.
func pauseContainerName(runID, taskRun string) string {
	return fmt.Sprintf("tkn-act-%s-%s-pause", runID, taskRun)
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/backend/docker/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/docker/sidecars.go internal/backend/docker/sidecars_helpers_test.go
git commit -m "feat(docker): scaffold sidecar lifecycle helpers (no-op pure functions)"
```

---

### Task 6: Docker backend — integration: pause + sidecar lifecycle, success path

**Files:**
- Modify: `internal/backend/docker/sidecars.go` (add the lifecycle functions)
- Modify: `internal/backend/docker/docker.go` (call into them; pull pause image at first sidecar-using Task; thread `pauseID` to `runStep`)
- Create: `internal/backend/docker/sidecars_integration_test.go` (`-tags integration`)

This is the riskiest task: the actual Docker plumbing for the per-Task pause container + shared netns. Expected duration: half a day. Iterate against `docker info` available locally.

**Lifecycle ordering (must match spec §3.1.1):** workspace prep → pull pause + sidecar images → start pause → start sidecars (each `network_mode: container:<pause-id>`) → wait `--sidecar-start-grace` → run steps (each `network_mode: container:<pause-id>`) → SIGTERM sidecars + SIGKILL after `--sidecar-stop-grace` → stop pause (hard 1s grace) → workspace teardown.

- [ ] **Step 1: Write the failing integration test (success path)**

Create `internal/backend/docker/sidecars_integration_test.go`:

```go
//go:build integration

package docker_test

import (
	"context"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestSidecarLifecycleHappyPath(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := be.Prepare(ctx, backend.RunSpec{
		RunID:  "sidetest1",
		Images: []string{"redis:7-alpine", "alpine:3"},
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = be.Cleanup(context.Background()) }()

	resultsHost := t.TempDir()
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID:       "sidetest1",
		TaskName:    "t",
		TaskRunName: "tt",
		ResultsHost: resultsHost,
		LogSink:     &noopSink{}, // declared in docker_integration_test.go
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				{Name: "redis", Image: "redis:7-alpine"},
			},
			Steps: []tektontypes.Step{
				{
					Name:   "ping",
					Image:  "redis:7-alpine",
					Script: "for i in 1 2 3 4 5; do redis-cli -h 127.0.0.1 -p 6379 PING && exit 0; sleep 1; done; exit 1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s, want succeeded", res.Status)
	}
}

func TestSidecarStartFailMarksInfraFailed(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// hopefully-unpullable tag.
	_ = be.Prepare(ctx, backend.RunSpec{RunID: "sidetest2", Images: []string{}})
	defer func() { _ = be.Cleanup(context.Background()) }()

	res, _ := be.RunTask(ctx, backend.TaskInvocation{
		RunID: "sidetest2", TaskName: "t", TaskRunName: "tt",
		ResultsHost: t.TempDir(), LogSink: &noopSink{},
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				{Name: "broken", Image: "this-image-definitely-does-not-exist:never"},
			},
			Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		},
	})
	if res.Status != backend.TaskInfraFailed {
		t.Errorf("status = %s, want infrafailed (sidecar pull failed)", res.Status)
	}
}

// TestSidecarCrashMidTaskDoesNotFailTask asserts the pause-container
// model: a sidecar dying mid-task records a sidecar-end event but
// does NOT fail the Task. Matches upstream "sidecars are best-effort".
// This is the test that would have FAILED under the previous
// "first-sidecar-as-netns-owner" design.
func TestSidecarCrashMidTaskDoesNotFailTask(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := be.Prepare(ctx, backend.RunSpec{
		RunID: "sidetest3", Images: []string{"alpine:3"},
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = be.Cleanup(context.Background()) }()

	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: "sidetest3", TaskName: "t", TaskRunName: "tt",
		ResultsHost: t.TempDir(), LogSink: &noopSink{},
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				// A sidecar that lives long enough to start
				// successfully, then exits cleanly mid-task.
				{Name: "shortlived", Image: "alpine:3", Script: "sleep 1; exit 0"},
			},
			Steps: []tektontypes.Step{
				{Name: "wait", Image: "alpine:3", Script: "sleep 3"},
				{Name: "after", Image: "alpine:3", Script: "true"},
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s, want succeeded (sidecar crash must not fail Task — upstream parity)", res.Status)
	}
}
```

(`noopSink` should be declared in `docker_integration_test.go`; if it isn't, add it as a small fake implementing `StepLog` and `SidecarLog`.)

- [ ] **Step 2: Run test to verify failure**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -count=1 -run 'TestSidecar' ./internal/backend/docker/...
```

Expected: FAIL — `RunTask` ignores `Sidecars`, so the redis-cli step can't reach a redis on localhost.

- [ ] **Step 3: Implement the lifecycle**

Extend `internal/backend/docker/sidecars.go` with the actual lifecycle. The key new symbols:

- `startPause(ctx, b, inv) (pauseID string, err error)` — creates and starts the per-Task pause container (`pauseImage`, normal Docker networking). Returns the container ID. If the pause image isn't already present, pulls it first (idempotent — `gcr.io/google-containers/pause:3.9` is cached forever after first pull).
- `startSidecars(ctx, b, inv, pauseID) (ids []string, err error)` — creates and starts every sidecar in declaration order, each with `network_mode: container:<pauseID>`. After starting all, sleeps for `b.opts.SidecarStartGrace` (default `2s`), then verifies every sidecar is still running. If any sidecar isn't running, returns an error so `RunTask` can return `infrafailed` (sidecar start-fail). The pause container is NOT in this list — the caller stops it separately.
- `checkSidecarLiveness(ctx, b, sidecarIDs, pauseID) (pauseAlive bool, crashed []string)` — for use between steps. Inspects each sidecar container and the pause container. Returns whether the pause container is still alive (defense-in-depth — should always be true) and the list of any sidecars that crashed since last check. **Sidecar crash never fails the Task** (matches upstream); only a pause-container exit infrafails.
- `stopSidecars(ctx, b, sidecarIDs)` — for each sidecar container: `ContainerStop` with `b.opts.SidecarStopGrace` (default 30s; sends SIGTERM, then SIGKILL after the grace), drain logs, emit terminal `sidecar-end` event, `ContainerRemove(Force: true)`.
- `stopPause(ctx, b, pauseID)` — `ContainerStop` with a hard 1s grace (pause(2) responds to SIGTERM immediately), then `ContainerRemove(Force: true)`.
- `streamSidecarLogs(ctx, b, sink, taskName, sidecarName, containerID)` — like `streamLogs` for steps, but emits via `sink.SidecarLog(taskName, sidecarName, "sidecar-stdout"|"sidecar-stderr", line)`.

Then in `internal/backend/docker/docker.go`:

a. Update `Options` to include both new flags:

```go
type Options struct {
	PullPolicyOverride string
	// SidecarStartGrace is how long the docker backend waits after
	// starting all sidecars before launching the first step.
	// Default 2s.
	SidecarStartGrace time.Duration
	// SidecarStopGrace is the SIGTERM-then-SIGKILL window when
	// tearing down sidecars at end of Task. Default 30s, matches
	// upstream Tekton's terminationGracePeriodSeconds default.
	SidecarStopGrace time.Duration
}
```

Defaults in `New`:

```go
if opts.SidecarStartGrace == 0 {
	opts.SidecarStartGrace = 2 * time.Second
}
if opts.SidecarStopGrace == 0 {
	opts.SidecarStopGrace = 30 * time.Second
}
```

b. The pause image pull is handled inside `startPause` (lazy, idempotent), NOT in `Prepare` — the pause image is a docker-backend implementation detail and shouldn't appear in the engine's `RunSpec.Images` table or be visible to the cluster backend. Sidecar images ARE pre-pulled in `Prepare` via the existing `RunSpec.Images` table (Task 3 already wired `uniqueImages` to walk sidecars).

c. In `RunTask`, after workspace prep and before the existing `for _, rawStep := range inv.Task.Steps` loop, add:

```go
	var pauseID string
	var sidecarIDs []string
	if len(inv.Task.Sidecars) > 0 {
		var err error
		pauseID, err = b.startPause(ctx, inv)
		if err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("pause container: %w", err)
			res.Ended = time.Now()
			return res, nil
		}
		// Defer teardown in reverse order: sidecars first, then pause.
		// Both run on a fresh background context so a cancelled ctx
		// (e.g. timeout) still drains them.
		defer b.stopPause(context.Background(), pauseID)
		defer b.stopSidecars(context.Background(), inv, sidecarIDs)
		sidecarIDs, err = b.startSidecars(ctx, inv, pauseID)
		if err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("sidecars: %w", err)
			res.Ended = time.Now()
			return res, nil
		}
	}
```

d. In `runStep` (the per-step container create), if `pauseID != ""`, set `hostConf.NetworkMode = container.NetworkMode("container:" + pauseID)`.

You'll need to thread `pauseID` from `RunTask` to `runStep` — easiest: pass it as an additional argument, or add it to whatever struct `runStep` already takes.

e. Between each step (in the `for _, rawStep := range inv.Task.Steps` loop, after each step ends), call `checkSidecarLiveness`. The semantics changed from the previous draft — sidecar crashes do NOT fail the Task:

```go
	pauseAlive, crashed := b.checkSidecarLiveness(ctx, sidecarIDs, pauseID)
	for _, id := range crashed {
		// Emit terminal sidecar-end with status:"failed" and the
		// exit code; continue running steps. Matches upstream
		// "sidecars are best-effort".
		b.emitSidecarCrashed(inv, id)
	}
	if !pauseAlive {
		// Defense-in-depth path; should never fire because pause(2)
		// only exits on signal.
		res.Status = backend.TaskInfraFailed
		res.Err = fmt.Errorf("netns owner (pause container) exited unexpectedly")
		res.Ended = time.Now()
		return res, nil
	}
```

- [ ] **Step 4: Run tests**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -count=1 -run 'TestSidecar' -timeout 5m ./internal/backend/docker/...
```

Expected: PASS for `TestSidecarLifecycleHappyPath` and `TestSidecarStartFailMarksInfraFailed`.

- [ ] **Step 5: Run the existing docker integration tests to confirm no regression**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -count=1 -timeout 10m ./internal/backend/docker/...
```

Expected: every test PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/docker/sidecars.go internal/backend/docker/docker.go internal/backend/docker/sidecars_integration_test.go
# also stage any docker_integration_test.go fake updates and any test fakes
# elsewhere that gained a SidecarLog stub
git commit -m "feat(docker): per-Task sidecar lifecycle with shared network namespace"
```

---

### Task 7: Wire `--sidecar-start-grace` and `--sidecar-stop-grace` flags + exit-code collision test

**Files:**
- Modify: `cmd/tkn-act/run.go`
- Test: `cmd/tkn-act/helpjson_test.go` (the help-json snapshot will need to know about the new flags)
- Test: `cmd/tkn-act/exit_test.go` (new or extend existing) — exit-code-collision check between sidecar-driven `infrafailed` and `Task.spec.timeout` driven `timeout`

- [ ] **Step 1: Add both flags**

In `cmd/tkn-act/run.go`, find the existing flag-registration block. Add:

```go
	cmd.Flags().Duration("sidecar-start-grace", 2*time.Second,
		"how long to wait after starting all sidecars before launching the first step")
	cmd.Flags().Duration("sidecar-stop-grace", 30*time.Second,
		"SIGTERM-then-SIGKILL window when stopping sidecars at end of Task (matches upstream Tekton's terminationGracePeriodSeconds)")
```

Plumb both values into `docker.Options`:

```go
	startGrace, _ := cmd.Flags().GetDuration("sidecar-start-grace")
	stopGrace, _ := cmd.Flags().GetDuration("sidecar-stop-grace")
	be, err := docker.New(docker.Options{
		PullPolicyOverride: pullPolicy,
		SidecarStartGrace:  startGrace,
		SidecarStopGrace:   stopGrace,
	})
```

- [ ] **Step 2: Update / regenerate help-json snapshot if needed**

```bash
go test -count=1 ./cmd/tkn-act/...
```

If `TestHelpJSONShape` (or similar) fails because the snapshot lists flags exhaustively, update it to include both new flags. If the test verifies presence of expected flags rather than exact equality, no change needed.

- [ ] **Step 3: Add exit-code-collision unit test**

The reviewer flagged a risk: a sidecar-driven `infrafailed` and a `Task.spec.timeout` driven `timeout` can both produce `infrafailed`-like statuses on intermediate per-step events, but the **exit-code mapping** must keep them distinct (1 vs 6). Add `cmd/tkn-act/exit_test.go` (or extend an existing exit-code test):

```go
func TestSidecarInfraFailExitCodeDistinctFromTimeout(t *testing.T) {
	// Construct a synthetic RunResult ending with a sidecar-driven
	// infrafailed task and assert the resolved exit code is 1.
	infra := engine.RunResult{Status: engine.RunInfraFailed}
	if got := exitCodeFor(infra); got != 1 {
		t.Errorf("infrafailed → exit %d, want 1", got)
	}
	// Now a timeout-driven RunResult and assert exit 6.
	to := engine.RunResult{Status: engine.RunTimeout}
	if got := exitCodeFor(to); got != 6 {
		t.Errorf("timeout → exit %d, want 6", got)
	}
}
```

(Adjust symbol names — `exitCodeFor`, `RunInfraFailed`, `RunTimeout` — to match the actual exports in `cmd/tkn-act/run.go` and `internal/engine`. The point of the test is locked-in: 1 vs 6 must NOT collide.)

- [ ] **Step 4: Commit**

```bash
git add cmd/tkn-act/run.go cmd/tkn-act/helpjson_test.go cmd/tkn-act/exit_test.go
git commit -m "feat(cli): add --sidecar-start-grace + --sidecar-stop-grace; lock exit-code separation"
```

---

### Task 8: Cluster backend — pass-through, parsePodSidecarStatuses helper, sidecar-start/sidecar-end emission

**Files:**
- Test: `internal/backend/cluster/runpipeline_test.go` (`taskSpec.sidecars` round-trip lock)
- Create: `internal/backend/cluster/sidecars_status.go` (pure helper, untagged)
- Create: `internal/backend/cluster/sidecars_status_test.go` (unit test, untagged — counts toward per-package coverage gate)
- Modify: `internal/backend/cluster/run.go` (call the helper from the per-TaskRun watch loop; emit `sidecar-start` / `sidecar-end` events; (stretch) sidecar log streaming branch)

**Why a pure helper exists:** cluster integration tests are `-tags cluster` and don't count toward the per-package coverage gate. Factoring `parsePodSidecarStatuses` out as a pure `(unstructured.Unstructured) → []SidecarStatus` function lets the untagged unit test cover it, satisfying the coverage gate even though the watch-loop wiring is only exercised by the tagged tests.

- [ ] **Step 1: Write the regression test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunInlinesSidecars: when a referenced Task has
// sidecars, the cluster backend must inline them under
// pipelineSpec.tasks[].taskSpec.sidecars[] intact (Tekton's
// EmbeddedTask schema accepts sidecars natively).
func TestBuildPipelineRunInlinesSidecars(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Sidecars: []tektontypes.Sidecar{
			{Name: "redis", Image: "redis:7-alpine"},
		},
		Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
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
	taskMap, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("tasks[0] not a map: %T", tasks[0])
	}
	taskSpec, ok := taskMap["taskSpec"].(map[string]any)
	if !ok {
		t.Fatalf("taskSpec missing under inlined task")
	}
	scs, ok := taskSpec["sidecars"].([]any)
	if !ok {
		t.Fatalf("sidecars missing on inlined taskSpec; got: %v", taskSpec)
	}
	if len(scs) != 1 {
		t.Fatalf("sidecars len = %d, want 1", len(scs))
	}
	scMap, ok := scs[0].(map[string]any)
	if !ok || scMap["name"] != "redis" || scMap["image"] != "redis:7-alpine" {
		t.Errorf("sidecars[0] = %v", scMap)
	}
}
```

- [ ] **Step 2: Run the test**

```bash
go test -count=1 -run TestBuildPipelineRunInlinesSidecars ./internal/backend/cluster/...
```

Expected: PASS without any production change to `run.go` (because `taskSpecToMap` is `json.Marshal`-based).

- [ ] **Step 3: Write the failing pure-helper test (`parsePodSidecarStatuses`)**

Create `internal/backend/cluster/sidecars_status_test.go` (no build tag — counts toward coverage gate):

```go
package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParsePodSidecarStatusesRunningAndTerminated(t *testing.T) {
	// Synthetic TaskRun with two sidecar entries: one still running,
	// one terminated with exit code 0.
	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{
					"name":      "redis",
					"container": "sidecar-redis",
					"running":   map[string]any{"startedAt": "2026-05-04T00:00:00Z"},
				},
				map[string]any{
					"name":      "mock",
					"container": "sidecar-mock",
					"terminated": map[string]any{
						"exitCode":   int64(0),
						"finishedAt": "2026-05-04T00:00:01Z",
					},
				},
			},
		},
	}}
	got := parsePodSidecarStatuses(tr)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "redis" || !got[0].Running {
		t.Errorf("got[0] = %+v, want redis running", got[0])
	}
	if got[1].Name != "mock" || !got[1].Terminated || got[1].ExitCode != 0 {
		t.Errorf("got[1] = %+v, want mock terminated exit 0", got[1])
	}
}

func TestParsePodSidecarStatusesEmpty(t *testing.T) {
	tr := &unstructured.Unstructured{Object: map[string]any{}}
	if got := parsePodSidecarStatuses(tr); len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
```

- [ ] **Step 4: Implement the pure helper**

Create `internal/backend/cluster/sidecars_status.go`:

```go
package cluster

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SidecarStatus is a tkn-act-internal projection of one entry from
// taskRun.status.sidecars[]. Mirrors the subset of corev1.ContainerStatus
// fields the watch-loop needs to emit sidecar-start / sidecar-end.
type SidecarStatus struct {
	Name       string
	Container  string
	Running    bool
	Terminated bool
	ExitCode   int32
}

// parsePodSidecarStatuses reads taskRun.status.sidecars[] from an
// unstructured TaskRun and returns one SidecarStatus per entry.
// Returns an empty slice if status.sidecars is missing or empty.
func parsePodSidecarStatuses(tr *unstructured.Unstructured) []SidecarStatus {
	if tr == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(tr.Object, "status", "sidecars")
	if err != nil || !found {
		return nil
	}
	out := make([]SidecarStatus, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		s := SidecarStatus{}
		if v, ok := m["name"].(string); ok {
			s.Name = v
		}
		if v, ok := m["container"].(string); ok {
			s.Container = v
		}
		if _, ok := m["running"].(map[string]any); ok {
			s.Running = true
		}
		if t, ok := m["terminated"].(map[string]any); ok {
			s.Terminated = true
			switch v := t["exitCode"].(type) {
			case int64:
				s.ExitCode = int32(v)
			case float64:
				s.ExitCode = int32(v)
			}
		}
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 5: Run the helper test**

```bash
go test -count=1 -run TestParsePodSidecarStatuses ./internal/backend/cluster/...
```

Expected: PASS.

- [ ] **Step 6: Wire the helper into the per-TaskRun watch loop and emit `sidecar-start` / `sidecar-end`**

In `internal/backend/cluster/run.go`'s per-TaskRun watch loop (the same loop that today resolves `taskRunToOutcome`), call `parsePodSidecarStatuses(tr)` on each poll and diff against the previous poll's result. The diff rules:

- A `Running == true` entry not seen running before → emit `EvtSidecarStart` with `Task = pipelineTaskName`, `Step = sc.Name`, `Stream = "sidecar"`.
- A `Terminated == true` entry not seen terminated before → emit `EvtSidecarEnd` with `Task`, `Step = sc.Name`, `Status` = `"succeeded"` if `ExitCode == 0` else `"failed"`, `ExitCode = sc.ExitCode`.

This satisfies the cross-backend fidelity rule: the same `sidecars` e2e fixture from Task 9 produces the same `sidecar-start` / `sidecar-end` event stream on both backends.

- [ ] **Step 7: (Stretch) add sidecar log streaming**

In `internal/backend/cluster/run.go::streamPodLogs`, the existing loop filters to `step-` prefix:

```go
		if !strings.HasPrefix(c.Name, "step-") {
			continue
		}
		stepName := strings.TrimPrefix(c.Name, "step-")
```

Extend to also stream sidecar containers (Tekton names them `sidecar-<name>`):

```go
		var stepName, sidecarName string
		switch {
		case strings.HasPrefix(c.Name, "step-"):
			stepName = strings.TrimPrefix(c.Name, "step-")
		case strings.HasPrefix(c.Name, "sidecar-"):
			sidecarName = strings.TrimPrefix(c.Name, "sidecar-")
		default:
			continue
		}
```

Then in the goroutine that scans the log stream, branch on whether `sidecarName != ""`:

```go
		go func(stepName, sidecarName string, rc io.ReadCloser) {
			defer rc.Close()
			s := bufio.NewScanner(rc)
			s.Buffer(make([]byte, 64*1024), 1024*1024)
			for s.Scan() {
				if sidecarName != "" {
					in.LogSink.SidecarLog(taskName, sidecarName, "sidecar-stdout", s.Text())
				} else {
					in.LogSink.StepLog(taskName, stepName, "stdout", s.Text())
				}
			}
		}(stepName, sidecarName, rc)
```

If extending `streamPodLogs` is more invasive than expected (the scanner-goroutine already takes a single name argument elsewhere in the file), defer to a follow-up PR and document the gap as an explicit row in `docs/feature-parity.md`. The `sidecar-start` / `sidecar-end` events from Steps 3-6 above are NOT optional — those ship in this PR — but log streaming MAY defer.

- [ ] **Step 8: Run cluster tests**

```bash
go test -count=1 ./internal/backend/cluster/...
go vet -tags cluster ./...
```

Expected: PASS, vet clean. The new `TestParsePodSidecarStatuses*` tests count toward the per-package coverage gate.

- [ ] **Step 9: Commit**

```bash
git add internal/backend/cluster/runpipeline_test.go internal/backend/cluster/sidecars_status.go internal/backend/cluster/sidecars_status_test.go internal/backend/cluster/run.go
git commit -m "feat(cluster): emit sidecar-start/end from status.sidecars; lock taskSpec.sidecars round-trip"
```

---

### Task 9: Cross-backend e2e fixture

**Files:**
- Create: `testdata/e2e/sidecars/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`
- Delete: `testdata/limitations/sidecars/` (graduation rule — `parity-check` enforces it)

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/sidecars/pipeline.yaml`:

```yaml
# Demonstrates: Task.spec.sidecars on both backends.
# A redis sidecar starts before the steps; the write step pings until
# redis is reachable on localhost:6379, sets a key, the read step
# verifies the value. Both backends share the same expected outcome.
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: with-redis
spec:
  sidecars:
    - name: redis
      image: redis:7-alpine
  steps:
    - name: write
      image: redis:7-alpine
      script: |
        # tkn-act default start grace is 2s; this loop tolerates slow
        # cold starts on shared CI runners.
        for i in 1 2 3 4 5 6 7 8 9 10; do
          redis-cli -h 127.0.0.1 -p 6379 PING && break
          sleep 1
        done
        redis-cli -h 127.0.0.1 -p 6379 SET hello world
    - name: read
      image: redis:7-alpine
      script: |
        v=$(redis-cli -h 127.0.0.1 -p 6379 GET hello)
        test "$v" = "world"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: sidecars
spec:
  tasks:
    - name: t
      taskRef: { name: with-redis }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go::All()`, after the `secret-from-yaml` entry:

```go
		{Dir: "sidecars", Pipeline: "sidecars", WantStatus: "succeeded"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run docker e2e locally if Docker is available**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/sidecars -count=1 -timeout 5m ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS.

- [ ] **Step 5: Delete the limitations fixture**

```bash
rm -rf testdata/limitations/sidecars/
```

- [ ] **Step 6: Run parity-check (will still fail until Task 10 flips the row)**

```bash
bash .github/scripts/parity-check.sh
```

Expected: FAIL, complaining that the `Task.sidecars` parity row claims `gap` but the limitations directory no longer exists. Task 10 fixes this.

- [ ] **Step 7: Commit**

```bash
git add testdata/e2e/sidecars/pipeline.yaml internal/e2e/fixtures/fixtures.go
git rm -r testdata/limitations/sidecars
git commit -m "test(e2e): sidecars fixture (cross-backend); remove limitation example"
```

---

### Task 10: Documentation convergence

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/tkn-act/agentguide_data.md`
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`
- Modify: `README.md`

- [ ] **Step 1: Add a `## Sidecars` section to `AGENTS.md`**

In `AGENTS.md`, after the existing `## stepTemplate (DRY for Steps)` section and before `## Documentation rule:`, insert:

```markdown
## Sidecars (`Task.sidecars`)

`Task.spec.sidecars` declares long-lived helper containers that share
the Task's pod / network namespace for the duration of the Task.
Catalog Tasks use them for databases, mock services, registries.

Supported fields tkn-act honors on `--docker`:

| Field | Behavior |
|---|---|
| `name`, `image` | Required. Name must be unique within the Task and not collide with a Step name. |
| `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources` | Same semantics as on Steps. |
| `volumeMounts` | Must reference a Task-declared `volumes` entry. |
| `workspaces` | Sidecar opts into Task workspaces by name; mounted at the workspace's path inside the sidecar. |
| `ports`, `readinessProbe`, `livenessProbe`, `startupProbe`, `securityContext`, `lifecycle` | Parsed for fidelity but ignored on `--docker` (`--cluster` forwards them to Tekton). Probe support is a deferred follow-up. |

Lifecycle on `--docker` (per Task with at least one sidecar):

1. Workspace prep (existing).
2. Sidecar images are pre-pulled by the engine; the pause image
   (`gcr.io/google-containers/pause:3.9`, ~700KB, cached forever after
   first pull) is pulled by the docker backend itself.
3. A tiny **pause container** is started with normal Docker networking.
   It owns the netns and only exits on signal (mirrors upstream
   Kubernetes / Tekton's "infra container" model).
4. Every sidecar in declaration order is started, each with
   `network_mode: container:<pause-id>`. Steps reach any sidecar at
   `localhost:<port>` exactly the way they would in a real Tekton pod.
5. After all sidecars are started, tkn-act waits a fixed grace
   (`--sidecar-start-grace`, default `2s`) before launching the first
   Step. Override the flag for slower-starting sidecars.
6. Each Step container is also started `network_mode: container:<pause-id>`.
7. Sidecars are sent SIGTERM after the last Step ends, with a
   `--sidecar-stop-grace` window (default `30s`, matches upstream
   Tekton's `terminationGracePeriodSeconds`) before SIGKILL. The pause
   container is then stopped (hard 1s grace).
8. Workspace teardown (existing).

Failure semantics:

| Scenario | Task status |
|---|---|
| Sidecar fails to start (image pull fails / container exits before grace) | `infrafailed`. One terminal `sidecar-end` event with `status: "infrafailed"`. |
| Sidecar crashes mid-Task (any sidecar — there is no "privileged" sidecar in the pause-container model) | Task status unchanged. Steps continue (the pause container, not the sidecar, owns the netns). The crash surfaces as `sidecar-end` with `status: "failed"` and the exit code. Matches upstream "sidecars are best-effort". |

JSON event stream additions (stable contract):

| Kind | Payload |
|---|---|
| `sidecar-start` | `task`, `step` (= sidecar name), `time`. `stream: "sidecar"`. |
| `sidecar-end` | `task`, `step`, `exitCode`, `duration`, `status` (`succeeded` / `failed` / `infrafailed`), `time`. |
| `sidecar-log` | `task`, `step`, `stream` (= `"sidecar-stdout"` / `"sidecar-stderr"`), `line`, `time`. |

The `step` field is reused for the sidecar's name so existing JSON
consumers don't need new fields; consumers that care about step-vs-
sidecar disambiguation can branch on `kind` or on `stream`.

Cluster pass-through is automatic — tkn-act inlines the full sidecars
list into `pipelineSpec.tasks[].taskSpec.sidecars`, and Tekton's
controller takes it from there.

---

```

- [ ] **Step 2: Mirror into `agentguide_data.md`**

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

In the `### -tags integration` table, add a row right after `secret-from-yaml/`:

```markdown
| `sidecars/`         | `Task.spec.sidecars`: redis sidecar; steps connect on `localhost:6379` (shared netns on docker) |
```

In the "By design" / "Plumbed but not covered" sections, **delete** the bullet starting "Sidecars. Need shared network namespaces; out of scope for the Docker backend.": that gap is now closed.

- [ ] **Step 5: Mark Track 1 #1 done in `docs/short-term-goals.md`**

In the Track 1 table, change the row 1 Status cell from:

```
| Out of scope in v1.2; documented in `testdata/limitations/sidecars/`. Needs design work for the docker backend (per-Task network + shared netns). Cluster mode already works. |
```

to:

```
| Done in v1.6 (PR for `feat: Task.sidecars`). Per-Task shared-netns lifecycle on docker; cluster pass-through verified. |
```

- [ ] **Step 6: Flip the parity row**

In `docs/feature-parity.md` under `### Task structure`, change:

```
| `Task.sidecars` (long-lived helper containers) | `Task.spec.sidecars` | gap | cluster-only | none | sidecars | docs/short-term-goals.md (Track 1 #1) — needs design for docker (per-Task network + shared netns) |
```

to:

```
| `Task.sidecars` (long-lived helper containers) | `Task.spec.sidecars` | shipped | both | sidecars | none | docs/superpowers/plans/2026-05-04-task-sidecars.md (Track 1 #1) |
```

- [ ] **Step 7: Update `README.md`**

In the `## Tekton features supported` list, add (after the `Task.spec.stepTemplate` bullet):

```
- `Task.spec.sidecars` — long-lived helper containers (databases,
  proxies, mock services). On `--docker`, a tiny per-Task pause
  container owns the netns and steps + sidecars all join it via
  `network_mode: container:<pause-id>` so steps reach sidecars at
  `localhost:<port>`. Two new flags: `--sidecar-start-grace`
  (default 2s) and `--sidecar-stop-grace` (default 30s, matches
  upstream Tekton's `terminationGracePeriodSeconds`). Cluster
  pass-through forwards the full Tekton schema; `sidecar-start` /
  `sidecar-end` events fire on both backends.
```

In the `## Not yet supported` paragraph, change:

```
Sidecars (cluster-only), StepActions, Resolvers (git/hub/cluster/
bundles), `PipelineTask.matrix`, custom tasks, signed pipelines,
tekton-results, Windows.
```

to:

```
StepActions, Resolvers (git/hub/cluster/bundles), `PipelineTask.matrix`,
custom tasks, signed pipelines, tekton-results, Windows.
```

- [ ] **Step 8: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 9: Commit**

```bash
git add AGENTS.md cmd/tkn-act/agentguide_data.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md README.md
git commit -m "docs: document Task.sidecars; flip Track 1 #1 to shipped"
```

---

### Task 11: Final verification and PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: every command exits 0; every test package OK or `[no test files]`.

- [ ] **Step 2: Coverage gate (per-package no-drop)**

```bash
bash .github/scripts/coverage-check.sh main HEAD
```

Expected: no per-package drop > 0.1pp. The risk packages are `internal/backend/docker/` (new file `sidecars.go`; covered by `sidecars_helpers_test.go` at the helper level) and `internal/engine/` (new substitution branch; covered by `TestSidecarParamsSubstitutedBeforeBackend`). If coverage drops, add tests rather than skipping the gate.

- [ ] **Step 3: Docker integration tests locally (if Docker available)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -count=1 -timeout 15m ./internal/e2e/... ./internal/backend/docker/...
```

Expected: every test PASS, including `TestE2E/sidecars` and `TestSidecar*`.

- [ ] **Step 4: Push branch and open PR**

```bash
git push -u origin feat/task-sidecars
gh pr create --title "feat: Task.sidecars on both backends (Track 1 #1)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #1 of `docs/short-term-goals.md`. Honors Tekton's
`Task.spec.sidecars` on both backends.

- New `tektontypes.Sidecar` type and `TaskSpec.Sidecars` field.
  YAML round-trip tested.
- Engine pre-pulls sidecar images, substitutes `$(params.X)` in
  sidecar fields before the backend runs.
- Validator: image required; name unique within the Task and not
  colliding with a Step name; `volumeMounts` reference declared
  Task-level volumes.
- Docker backend: per-Task **pause container** owns the network
  namespace (mirrors upstream Kubernetes / Tekton's "infra container"
  model — `gcr.io/google-containers/pause:3.9`, ~700KB, cached after
  first pull). Every sidecar and every Step joins it via
  `network_mode: container:<pause-id>`. Steps reach any sidecar at
  `localhost:<port>` exactly the way they would in a Tekton pod.
  Sidecars are SIGTERMed after the last Step (`--sidecar-stop-grace`,
  default 30s, then SIGKILL); pause container then stops with hard 1s
  grace. New flags: `--sidecar-start-grace` (default 2s) and
  `--sidecar-stop-grace` (default 30s, matches upstream Tekton's
  `terminationGracePeriodSeconds`).
- Failures: sidecar that fails to start → Task `infrafailed`;
  sidecar crash mid-Task → Task status UNCHANGED, recorded on the
  event stream (matches upstream "sidecars are best-effort"). Locked
  unit test ensures sidecar-driven `infrafailed` (exit 1) does NOT
  collide with `Task.spec.timeout` driven `timeout` (exit 6).
- Three new JSON event kinds: `sidecar-start`, `sidecar-end`,
  `sidecar-log`. Reuses `Event.Step` for the sidecar name; uses
  `stream: "sidecar-stdout"` / `"sidecar-stderr"` for log
  attribution.
- Cluster backend: (a) `taskSpecToMap` round-trips `sidecars` field
  automatically; regression test added. (b) New pure helper
  `parsePodSidecarStatuses` (untagged unit test for coverage gate)
  reads `status.sidecars[]` and the watch loop emits matching
  `sidecar-start` / `sidecar-end` events — cross-backend fidelity.
  Sidecar log streaming via `streamPodLogs` is a stretch goal
  (deferred if non-trivial; documented as gap if so).
- Cross-backend e2e fixture (`sidecars`) with redis. Limitation
  fixture deleted in this PR (parity-check enforces it).

Implements `docs/superpowers/plans/2026-05-04-task-sidecars.md`.
Design: `docs/superpowers/specs/2026-05-04-task-sidecars-design.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] `bash .github/scripts/coverage-check.sh main HEAD`
- [x] tests-required script
- [ ] docker-integration CI — runs `TestE2E/sidecars` + `TestSidecar*`
- [ ] cluster-integration CI — same `sidecars` fixture on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 5: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Type model → Task 1. Validator rules → Task 2. Engine substitution + pre-pull → Task 3. Reporter event kinds + LogSink + pretty rendering → Task 4. Docker pure helpers → Task 5. Docker lifecycle (the riskiest task) → Task 6. CLI flag → Task 7. Cluster regression + log streaming → Task 8. Cross-backend fixture + limitation removal → Task 9. Docs convergence → Task 10. Final ship → Task 11.
- **TDD throughout:** every implementation task starts with a failing test (Step 1) before writing production code.
- **Coverage no-drop is engineered in, not retrofitted:** Task 5 is a deliberate split between untagged pure-helper tests (counted by the gate) and `-tags integration` lifecycle tests (not counted by the gate). Without that split, `internal/backend/docker/sidecars.go` would land with measured 0% coverage and the gate would fail.
- **Parity-check is a checkable invariant at every commit boundary:** the limitation fixture deletion (Task 9 Step 5) and the parity-row flip (Task 10 Step 6) land in adjacent commits; in between, parity-check briefly fails (documented in Task 9 Step 6). After Task 10 it's green again.
- **One-task non-regression:** Task 6 Step 5 explicitly runs the full docker integration suite to confirm the sidecar lifecycle insertion doesn't silently break the v1.2 step model. Task 3 Step 5 does the same for the engine suite (the new sidecar substitution branch sits right next to `substituteSpec` for steps).
- **Cluster pass-through "free" + cluster fidelity in scope:** Task 8 Step 2 verifies that adding the type field is enough for `taskSpec.sidecars` round-trip — no production change to the cluster backend's marshal path. Steps 3-6 of Task 8 add the `parsePodSidecarStatuses` pure helper and wire it into the watch loop so `sidecar-start` / `sidecar-end` events fire on cluster too (cross-backend fidelity is a hard project rule). The pure helper is untagged so it counts toward the per-package coverage gate (`-tags cluster` integration tests don't). The optional sidecar log-streaming work in Step 7 has an explicit punt-to-follow-up escape hatch.
- **No placeholders:** every step has actual code or commands. The riskiest step (Task 6 Step 3, the actual Docker plumbing for the pause container + sidecars) has a self-contained spec of every new symbol; the executing agent can write each helper in turn.
- **Open questions defer to the spec:** all eight Open Questions in `docs/superpowers/specs/2026-05-04-task-sidecars-design.md` §9 carry over here; the plan assumes the recommended-default answers (2s start grace, 30s stop grace, `gcr.io/google-containers/pause:3.9` pin, sidecar crashes do NOT fail the Task, no dedicated `sidecar-failure` fixture, fine-grained stream values, explicit `Sidecar.workspaces`, cluster log streaming may defer).
