# `Task.sidecars` — design spec

**Date:** 2026-05-04
**Status:** Draft for human review
**Owner:** Track 1 #1 of `docs/short-term-goals.md`
**One-liner:** Honor Tekton v1 `Task.spec.sidecars` on both backends — pass-through on `--cluster`, and a per-Task shared-netns implementation on `--docker` so steps can reach a sidecar at `localhost:<port>` exactly the way upstream Tekton allows.

---

## 1. Goals and non-goals

### Goals

- Parse and honor `Task.spec.sidecars` from upstream `tekton.dev/v1` YAML.
- On `--cluster`: zero-extra-resolution pass-through. Tekton's controller already runs sidecars in the TaskRun pod; we just need to verify the JSON marshal round-trip preserves the `sidecars` list and add a cross-backend e2e fixture.
- On `--docker`: each running Task gets its own short-lived per-Task network, with sidecars started before the first Step and torn down after the last Step. Steps share the sidecar's network namespace so `localhost:<port>` works the same as inside a Tekton pod.
- Surface sidecar lifecycle on the JSON event stream so agents can attribute mid-run failures to a sidecar rather than a step.
- Match upstream behavior on failures: a sidecar that fails to *start* fails the Task (`infrafailed`); a sidecar that crashes mid-run is recorded but does NOT fail the Task on its own — the Task's status is still driven by its Steps.
- Graduate `testdata/limitations/sidecars/` (delete in the same PR — `parity-check` enforces it).

### Non-goals (explicit, deferred to a follow-up PR if needed)

- **Probes** (`readinessProbe`, `livenessProbe`, `startupProbe`). Tekton uses `readinessProbe` to gate "the sidecar is ready" before the first Step runs; tkn-act will use a fixed short start grace period instead. Probe support is deferred to a follow-up PR.
- **`ports` declarations.** Upstream allows `containerPort` declarations for documentation; in tkn-act docker mode, all sidecar ports are reachable on `localhost` automatically (shared netns), so the field is parsed-and-ignored — port numbers come from the Step's connection string, not from the sidecar declaration.
- **`securityContext`, `lifecycle`, `volumeDevices`, `terminationMessagePath`, `tty`, `stdin`.** Same posture as for Steps — tkn-act doesn't read these on Steps and won't on Sidecars.
- **`workspaces` on a Sidecar.** Tekton allows a Sidecar to reference Task-declared workspaces; Tekton mounts the same PVC. tkn-act will support this on `--docker` (workspace bind mounts on the sidecar container, same path as for steps) because the cost is one extra mount entry. Cluster pass-through is automatic.
- **Background image-pull parallelism for sidecars.** Sidecar images are pre-pulled in `Backend.Prepare` like step images. No streaming pull progress.
- **Sidecar `results`.** Tekton sidecars don't have results; this matches.
- **`script` mode for sidecars.** In scope. Same script-to-tmpfile mechanism Steps use.

### Tekton-v1 surface in scope vs. deferred

| Sidecar field | tkn-act docker | tkn-act cluster |
|---|---|---|
| `name` | in scope | pass-through |
| `image` | in scope | pass-through |
| `command`, `args` | in scope | pass-through |
| `script` | in scope | pass-through |
| `env` | in scope | pass-through |
| `workingDir` | in scope | pass-through |
| `resources` (limits/requests) | in scope (limits → `--memory`/`--cpus`, mirroring Steps) | pass-through |
| `volumeMounts` (Task-level volumes) | in scope | pass-through |
| `workspaces` | in scope | pass-through |
| `ports` | parsed; ignored on docker | pass-through |
| `readinessProbe` / `livenessProbe` / `startupProbe` | parsed; ignored on docker (deferred PR) | pass-through |
| `securityContext`, `lifecycle`, `tty`, `stdin` | parsed; ignored | pass-through |

The "parsed; ignored on docker" entries means the YAML loads cleanly and the cluster path forwards the field intact, but the docker backend logs nothing about them and silently doesn't enforce them. This mirrors the existing posture for `securityContext` etc. on Steps.

---

## 2. Type additions

`internal/tektontypes/types.go`. One new type, one new field on `TaskSpec`:

```go
type TaskSpec struct {
    // ... existing fields ...
    Sidecars []Sidecar `json:"sidecars,omitempty"`
}

// Sidecar is a long-lived helper container that shares the Task's pod /
// network namespace for the duration of the Task. tkn-act parses the
// upstream Tekton v1 superset but only honors the subset listed below
// on the docker backend; cluster mode pass-throughs the full marshalled
// shape via taskSpecToMap, so any field the Tekton controller knows
// about works under --cluster regardless of whether tkn-act reads it
// itself.
type Sidecar struct {
    Name         string         `json:"name"`
    Image        string         `json:"image"`
    Command      []string       `json:"command,omitempty"`
    Args         []string       `json:"args,omitempty"`
    Script       string         `json:"script,omitempty"`
    Env          []EnvVar       `json:"env,omitempty"`
    WorkingDir   string         `json:"workingDir,omitempty"`
    Resources    *StepResources `json:"resources,omitempty"`
    VolumeMounts []VolumeMount  `json:"volumeMounts,omitempty"`
    Workspaces   []WorkspaceUsage `json:"workspaces,omitempty"`
    // Ports is parsed for fidelity but ignored on the docker backend
    // (shared netns means everything is on localhost already). Cluster
    // backend forwards as-is.
    Ports []ContainerPort `json:"ports,omitempty"`
    // ImagePullPolicy honors Always|IfNotPresent|Never the same as Step.
    ImagePullPolicy string `json:"imagePullPolicy,omitempty"`
}

// ContainerPort is a fidelity-only stub for upstream's
// corev1.ContainerPort. tkn-act records the bytes and forwards them
// to the cluster backend; no semantic effect on docker.
type ContainerPort struct {
    Name          string `json:"name,omitempty"`
    ContainerPort int    `json:"containerPort"`
    Protocol      string `json:"protocol,omitempty"`
}

// WorkspaceUsage is the per-container workspace declaration used by
// Sidecar (and, in upstream Tekton, also by Step — tkn-act handles
// step workspaces implicitly via PipelineTask bindings, so this type
// is sidecar-only for now).
type WorkspaceUsage struct {
    Name      string `json:"name"`
    MountPath string `json:"mountPath,omitempty"`
    SubPath   string `json:"subPath,omitempty"`
}
```

YAML round-trip is the only thing the type-level test asserts; semantics are covered by the engine and backend tests.

---

## 3. Backend strategy

### 3.1 Docker backend — shared network namespace per Task

**Decision: shared netns owned by a tiny per-Task pause container** (chosen over both a per-Task user-defined bridge network and the previous "first-sidecar-as-netns-owner" sketch). Rationale and tradeoff:

| Approach | Pros | Cons |
|---|---|---|
| **Pause container owns netns (chosen)** | Steps reach sidecars on `localhost:<port>` exactly like a Tekton pod. No DNS, no port mapping, no ambiguity about hostnames. Matches upstream semantics 1:1. The netns owner is a tiny, never-exits-on-its-own image (`gcr.io/google-containers/pause:3.9`, ~700KB) so any user-declared sidecar can crash without taking the netns down. Mirrors upstream Kubernetes / Tekton's actual "infra container" model. | One extra container start per Task (cheap; the pause image is cached forever after first pull). |
| First-sidecar-as-netns-owner (rejected) | One fewer container to start. | Makes the first DECLARED sidecar implicitly privileged: if it dies, the whole task dies, and the sidecars-list ordering becomes a load-bearing invariant the user has no signal about. This is exactly the trap upstream avoids by using a pause container. |
| Per-Task bridge network | Sidecar stays in its own netns; container DNS resolves the sidecar by name. Robust to one sidecar dying. | Steps reach the sidecar at `<sidecar-name>:<port>`, NOT `localhost:<port>`. Upstream Tekton uses `localhost`. Forces real-world `localhost`-using catalog Tasks to break on `--docker` — which is exactly the parity gap Track 1 #1 exists to close. |

The pause-container choice means: per-Task, tkn-act starts one extra tiny container (`gcr.io/google-containers/pause:3.9`) that owns the netns and exits only when killed. Every sidecar and every step joins that netns via `network_mode: container:<pause-id>`. **Any sidecar can die at any time without disrupting steps or other sidecars** — match upstream semantics, where a sidecar crashing mid-task does NOT fail the Task by default.

If a Task has zero sidecars, no netns plumbing or pause container happens at all — current behavior is unchanged. This is the common case and we should not regress its container-create count.

If a Task has one or more sidecars, the lifecycle becomes (see also §3.1.1 for the canonical ordering against workspace prep):

1. **Prepare**: `Backend.Prepare` pre-pulls the pause image and every sidecar image alongside step images (the existing `RunSpec.Images` table; pause is added by the docker backend itself, not by the engine — it isn't a user-visible image).
2. **Per-Task setup** (inside `RunTask`, after workspace prep — see §3.1.1):
   1. Create + start the **pause container** with normal Docker networking. It owns the netns. Container name: `tkn-act-<runID>-<taskRun>-pause`.
   2. Create + start every **sidecar** in declaration order, each with `network_mode: container:<pause-id>`. Stream their logs to the sink (`sidecar-log`).
   3. Wait a fixed grace period (default `2s`, override via `--sidecar-start-grace`) for sidecars to come up. This is the deferred-probe substitute. Sufficient for redis/postgres/mock-http style sidecars in catalog Tasks; sidecars that genuinely need >2s to be ready can declare a longer grace via the flag, or wait for the probe-support follow-up.
   4. Verify each sidecar is still running. If a sidecar exited before grace, treat that sidecar as **start-failed** → `infrafailed` for the Task, with `message: "sidecar %q failed to start"`. The pause container alone never start-fails (it's a fixed, well-known image whose only failure mode is image-pull, which Prepare surfaces).
3. **Step execution loop** (existing): each step container is created with `network_mode: container:<pause-id>` (instead of default networking). Everything else — workspace mounts, results dir, script, env — is unchanged.
4. **Sidecar liveness check between steps** (cheap):
   - Before starting each step's container, inspect every sidecar with `ContainerInspect`. If any have exited since the last check:
     - Emit `sidecar-end` with the exit code; do **NOT** fail the Task — match upstream. The crashed sidecar stays in the JSON event stream as evidence. Steps continue because the pause container, not the sidecar, owns the netns.
   - The pause container is also inspected; if it has exited (extremely unlikely — `pause:3.9` blocks on `pause(2)` until killed), the Task is unsalvageable: `infrafailed`, message `"netns owner (pause container) exited unexpectedly"`. We expect this branch never to fire in practice; it exists for defense in depth.
5. **Per-Task teardown** (always, including failure paths, in the order listed): for each sidecar, `ContainerStop` with `--sidecar-stop-grace` (default 30s; sends SIGTERM, then SIGKILL after the grace expires). Drain logs. Then `ContainerRemove` with `Force: true`. Emit a terminal `sidecar-end` for each. Finally stop and remove the pause container (`ContainerStop` with a hard 1s grace — pause(2) responds to SIGTERM immediately, so the long stop-grace doesn't apply; the longer grace is for the user's sidecars only).

**Container naming**: `tkn-act-<runID>-<taskRun>-sidecar-<name>` for sidecars; `tkn-act-<runID>-<taskRun>-pause` for the per-Task netns owner. Mirrors the existing step naming convention `tkn-act-<runID>-<taskRun>-<step>`.

**Parallel tasks isolation**: each `RunTask` invocation creates its own pause container, owns its own netns, and tears down only its own sidecars + pause. Two pipeline tasks running at the same DAG level get independent sidecar sets — sidecar state is per-Task, not per-Pipeline. The container naming above includes `<taskRun>`, so collisions are impossible.

**Why no `--network` flag is needed**: shared netns via `container:<id>` works on the default Docker daemon configuration. We don't create or destroy any bridge network ourselves.

#### 3.1.1 Lifecycle ordering against workspace prep

Sidecars may declare `volumeMounts` referencing the same workspace dirs that Steps mount, so the workspace must be set up BEFORE sidecars start. Canonical per-Task order:

1. **Workspace prep** (existing `RunTask` plumbing): create the per-Task workspace bind directories.
2. **Pull pause + sidecar images** — already covered by `Backend.Prepare` and the engine's `uniqueImages` walk; pause is added by the docker backend itself.
3. **Start pause container** with normal Docker networking; capture its ID.
4. **Start sidecars** in declaration order, each `network_mode: container:<pause-id>`, with the same workspace mounts the Task declares. Wait `--sidecar-start-grace` (default 2s) before launching the first step.
5. **Run steps**, each joining `network_mode: container:<pause-id>`.
6. **Sidecar + pause teardown**: SIGTERM each sidecar; SIGKILL after `--sidecar-stop-grace` (default 30s). Then SIGTERM + SIGKILL the pause container (hard 1s grace).
7. **Workspace teardown** (existing): the per-Task workspace cleanup runs after sidecars are gone, so any sidecar still holding a workspace fd doesn't block the rmdir.

### 3.2 Cluster backend — pass-through verification

`internal/backend/cluster/run.go::taskSpecToMap` is a `json.Marshal` round-trip, so adding `Sidecars` to `TaskSpec` makes the field appear under the inlined `pipelineSpec.tasks[].taskSpec.sidecars` automatically. Tekton's `EmbeddedTask` schema accepts `sidecars` natively. **No production code change needed**; we add a regression test (mirrors the `TestBuildPipelineRunInlinesStepTemplate` pattern) so a future hand-rolled converter can't silently drop the field.

### 3.3 Engine wiring

The engine doesn't model sidecars itself. Lifecycle is entirely per-Task and lives in the docker backend. The engine does need to:

- **Pass `inv.Task.Sidecars` through** unchanged. Today `TaskInvocation.Task` is a `tektontypes.TaskSpec`, so the field arrives at the backend automatically once the type carries it.
- **Pre-pull sidecar images** in `RunSpec.Images`. Update `internal/engine/engine.go::uniqueImages` to walk `spec.Sidecars` after walking `spec.Steps`. (One-line plus a loop.)
- **Apply substitution** to sidecar fields. Sidecars can reference `$(params.X)` and `$(workspaces.X.path)` — same substitution rules as Steps. Update `substituteSpec` to walk `spec.Sidecars` the same way it walks `spec.Steps` for `Image`, `Command`, `Args`, `Script`, `WorkingDir`, `Env`. (Sidecars do NOT reference `$(steps.X.results.Y)` — sidecars start before steps and are torn down after, so step-result substitution doesn't apply.)

That's the entire engine surface. No DAG change, no policy-loop change, no when/finally interaction.

### 3.4 Validator changes

Three new rules in `internal/validator/validator.go`:

1. **`Sidecar.Image` is required** (string, non-empty). Same rule as `Step.Image` had before stepTemplate.
2. **`Sidecar.Name` is required and unique within the Task** — and must not collide with any Step name (sharing a netns means sharing a container-name namespace inside our naming scheme, but the user-visible disambiguation matters more: the JSON event stream uses `(task, name)` to attribute logs).
3. **Sidecar `volumeMounts` must reference declared `Task.spec.volumes`** — same rule we apply to Step volumeMounts.

Validation failures land as exit code 4, like every other validator rule.

---

## 4. JSON event additions

We add **three new event kinds** to the stable `EventKind` set:

| Kind | When | Payload |
|---|---|---|
| `sidecar-start` | Just after the sidecar container is started successfully. | `task`, `step` (= sidecar name; we reuse the `step` field rather than introduce `sidecar` so existing JSON consumers keep parsing), `time`. We also set `stream: "sidecar"` on this event (and on `sidecar-end`/`sidecar-log`) so consumers that *do* want to filter sidecar vs. step output have a clean signal. |
| `sidecar-end` | When the sidecar exits — voluntarily mid-Task, killed at teardown, or detected as crashed at the inter-step liveness check. | `task`, `step`, `exitCode`, `time`, `duration`. `status` is one of `succeeded` (exit 0 at teardown — clean SIGTERM response), `failed` (non-zero exit), `infrafailed` (failed to start; in this case it's also the terminal event for that sidecar — there's no preceding `sidecar-start`). |
| `sidecar-log` | One per line of sidecar stdout/stderr. | `task`, `step`, `stream` (= `"sidecar-stdout"` / `"sidecar-stderr"`; deliberately NOT `"stdout"` / `"stderr"` so step-vs-sidecar attribution is trivial), `line`, `time`. |

Concretely, in `internal/reporter/event.go`:

```go
const (
    // ... existing kinds ...
    EvtSidecarStart EventKind = "sidecar-start"
    EvtSidecarEnd   EventKind = "sidecar-end"
    EvtSidecarLog   EventKind = "sidecar-log"
)
```

The existing `Event` struct does not need new fields — `Task`, `Step`, `Stream`, `Line`, `Status`, `ExitCode`, `Duration`, `Message` cover the payload. Reusing `Step` for the sidecar name is a deliberate trade: agents that care about the difference can branch on `Kind`; agents that just want "all things named under task X" get them for free. The `stream` value (`"sidecar"`, `"sidecar-stdout"`, `"sidecar-stderr"`) carries the disambiguation when needed.

**No new exit codes.** Sidecar start-fail surfaces as task `infrafailed` (exit code 1 — generic), not as a new dedicated code. Crash-during-run doesn't change the run status at all.

**Cluster mode parity (cross-backend fidelity is a hard project rule)**: the cluster backend MUST surface `sidecar-start` and `sidecar-end` from Tekton's TaskRun status so the JSON event stream is shape-equivalent across backends. This ships in the same PR — not a follow-up. Implementation: a new pure helper `parsePodSidecarStatuses(taskRun *unstructured.Unstructured) []SidecarStatus` in `internal/backend/cluster/run.go` reads `status.sidecars[]` (each entry has `name`, `container`, plus a `running` / `terminated` / `waiting` state mirroring `corev1.ContainerStatus`); the existing per-TaskRun watch loop diff-checks the slice between polls and emits `sidecar-start` (when a `running` state first appears) and `sidecar-end` (when a `terminated` state first appears, with `exitCode` from `terminated.exitCode`). The helper is factored out as a pure function so it can be unit-tested untagged (see §7.1) and counts toward the per-package coverage gate.

Sidecar **log** streaming on cluster (one branch in `streamPodLogs`) MAY ship as a follow-up if it adds non-trivial complexity to the existing scanner-goroutine; the `start`/`end` events alone satisfy the cross-backend fidelity rule for this PR. If log streaming defers, it lands as an explicit `gap` row in `docs/feature-parity.md` so it isn't lost.

---

## 5. Pretty output additions (terse)

Existing pretty output is `task/step` per log line and one summary line per task. We add the bare minimum:

- On `sidecar-start`: in `--verbose` only, one line: `<task>/<sidecar> sidecar started`.
- On `sidecar-end` with non-zero exit OR `infrafailed`: always print one line in any verbosity above `--quiet`: `<task>/<sidecar> sidecar exited <code> [crashed mid-task | failed to start]`. This is the user-facing signal that something noteworthy happened.
- Sidecar logs: prefix with `<task>/<sidecar>:` (same pattern as steps). Disable in `--quiet`. The prefix uses `:` instead of `/` to disambiguate from step logs in mixed output.

No color changes. No new flags.

---

## 6. Failure semantics — mirrored from upstream

| Scenario | Step behavior | Task status |
|---|---|---|
| Sidecar **fails to start** (image pull fails, container won't start, exits before grace period) | Steps never run | `infrafailed` (exit code 1). One `error` event with the start-fail reason. Terminal `sidecar-end` with `status: "infrafailed"`. |
| Sidecar **crashes mid-Task** (any sidecar — there is no "privileged first sidecar" with the pause-container model) | Steps continue (the pause container, not the sidecar, owns the netns) | Whatever the steps produce — sidecar crash does NOT change the Task status. `sidecar-end` records the crash with `status: "failed"` and the exit code. This matches upstream Tekton's "sidecars are best-effort" posture. |
| Pause container exits unexpectedly (defense-in-depth path; should never fire) | Currently-running step's netns is gone; subsequent steps don't run | `infrafailed`, `message: "netns owner (pause container) exited unexpectedly"`. |
| Step fails | Existing per-Task `OnError` policy; sidecars + pause still get torn down at end of Task | Existing semantics |
| Task timeout fires (Track 1 #2 / `Task.spec.timeout`) | Steps + sidecars + pause all get the existing context-cancel teardown path | `timeout` (exit code **6**) — distinct from the sidecar-driven `infrafailed` (exit code **1**) even though both can show `status: "infrafailed"` for individual events. The exit-code mapping in `cmd/tkn-act/run.go` keeps the codes separate and is exercised by a unit test (see §7.1). |
| Sidecar exits cleanly (exit 0) at teardown (responded to SIGTERM within `--sidecar-stop-grace`) | n/a | n/a — `sidecar-end` with `status: "succeeded"` |
| Sidecar gets SIGKILLed at teardown (didn't respond to SIGTERM within `--sidecar-stop-grace`) | n/a | n/a — `sidecar-end` with `status: "failed"`, exit code from Docker (typically 137). Not user-fatal. |

The SIGTERM-then-SIGKILL grace is configurable via `--sidecar-stop-grace`, default **30s** (matches upstream Tekton's `terminationGracePeriodSeconds` default). The previous 10s default was too aggressive for real sidecars (postgres flush, kafka commit). The pause container itself is stopped with a hard 1s grace, since `pause(2)` responds to SIGTERM immediately.

---

## 7. Test plan

### 7.1 Unit tests

| Test | Asserts |
|---|---|
| `internal/tektontypes/types_test.go::TestUnmarshalTaskWithSidecars` | YAML → `TaskSpec.Sidecars` round-trip, including a sidecar with script, env, volumeMounts |
| `internal/engine/engine_test.go::TestUniqueImagesIncludesSidecars` | `uniqueImages` picks up sidecar images |
| `internal/engine/engine_test.go::TestSubstituteSpecAppliesToSidecars` | `$(params.X)` in sidecar `env`, `args`, `script` is resolved before backend sees it |
| `internal/validator/validator_test.go::TestValidateSidecarRequiresImage` | empty `image` → exit 4 |
| `internal/validator/validator_test.go::TestValidateSidecarNameUnique` | duplicate sidecar name → exit 4; collision with step name → exit 4 |
| `internal/validator/validator_test.go::TestValidateSidecarVolumeMountsResolve` | `volumeMounts.name` not declared in `Task.spec.volumes` → exit 4 |
| `internal/backend/docker/sidecars_helpers_test.go` (new, **untagged** for coverage gate) | Pure-helper tests for the docker-backend sidecar lifecycle: `sidecarContainerName` and `pauseContainerName` match the documented format; the start-grace-period loop returns immediately on context cancel |
| `internal/backend/cluster/sidecars_status_test.go` (new, **untagged** for coverage gate) | Pure-function test for `parsePodSidecarStatuses` (factored out of cluster's pod-watch loop). Asserts: a `pr.status.taskRuns[].status.sidecars[]` slice with mixed `running` / `terminated` containers maps to the right `sidecar-start` / `sidecar-end` event payloads. Lives untagged so the per-package coverage gate sees it (cluster integration tests are `-tags cluster` and don't count). |
| `internal/backend/cluster/runpipeline_test.go::TestBuildPipelineRunInlinesSidecars` | After `taskSpecToMap`, `pipelineSpec.tasks[].taskSpec.sidecars[]` is intact (regression lock) |
| `cmd/tkn-act/exit_test.go::TestSidecarInfraFailExitCodeDistinctFromTimeout` | A run that ends `infrafailed` because a sidecar failed to start exits **1**; a run that ends `timeout` (Track 1 #2 task timeout, or Track 1 #2 pipeline-level timeout) exits **6**. Both can produce `infrafailed` per-event statuses on the way to the run-end, but the exit-code mapping in `cmd/tkn-act/run.go` does NOT collide. |

#### `LogSink` implementer audit (Critical 3)

Adding `SidecarLog(taskName, sidecarName, stream, line string)` to the `LogSink` interface in `internal/backend/backend.go` requires every existing implementer to gain a (possibly no-op) method. Grepped enumeration of in-tree consumers as of this revision:

| File | Type | Treatment |
|---|---|---|
| `internal/reporter/event.go` | `*reporter.LogSink` (the production implementer) | Real implementation: emit `EvtSidecarLog` with the sidecar name in `Event.Step` and the documented `stream` value. |
| `internal/backend/docker/docker_integration_test.go` | `captureLogs` (used by docker integration tests) | No-op stub. Does not assert on sidecar logs. |
| `internal/backend/cluster/run_test.go` and `cluster_test.go` | Any inline test sinks (search confirms none today, but the same audit applies if a cluster test grows one) | No-op stub. |

There are no other in-tree `LogSink` implementations. The `grep -rn 'func.*StepLog' internal/ --include='*.go'` invocation in the plan's Task 4 Step 4 cross-checks this list at execution time; if a new implementer has been added since this spec was written, that grep finds it and the executing agent adds a no-op there too.

### 7.2 Integration (`-tags integration`) tests

The new e2e fixture `testdata/e2e/sidecars/`:

```yaml
# A redis sidecar; one step writes to it; another reads.
apiVersion: tekton.dev/v1
kind: Task
metadata: { name: with-redis }
spec:
  sidecars:
    - name: redis
      image: redis:7-alpine
  steps:
    - name: write
      image: redis:7-alpine
      script: |
        # Wait for redis to be reachable; the start-grace gives us 2s,
        # but extend with a tiny retry loop in the fixture so flaky
        # cold starts don't break the gate.
        for i in 1 2 3 4 5; do
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
metadata: { name: sidecars }
spec:
  tasks:
    - name: t
      taskRef: { name: with-redis }
```

Plus an entry in `internal/e2e/fixtures/fixtures.go::All()`:

```go
{Dir: "sidecars", Pipeline: "sidecars", WantStatus: "succeeded"},
```

The fixture runs on **both** backends via `internal/e2e/fixtures.All()` — that's the cross-backend fidelity guarantee. On `--docker` it exercises the new shared-netns code path; on `--cluster` it exercises the pass-through.

A second narrower fixture is *optional* and probably overkill: a dedicated `sidecar-failure-propagation/` documenting the start-fail and crash-mid-Task cases. We rely on unit tests to cover those instead, because authoring deterministic crash-on-startup behavior with public images is brittle.

### 7.3 Limitations fixture removal

`testdata/limitations/sidecars/` (the existing `pipeline.yaml` + comment block) is deleted in the same PR. `parity-check` enforces this when the parity-row flips from `gap` to `shipped`.

### 7.4 Coverage gate considerations

The new docker-backend sidecar code lives in a new file `internal/backend/docker/sidecars.go`. Because the `coverage` job measures **default** (no-tag) test set only, and the docker backend is gated behind `-tags integration`, that file's coverage doesn't move the needle on the default-set numbers. The pure-helper tests in `internal/backend/docker/sidecars_test.go` are also `-tags integration` (matching the existing `docker_integration_test.go`); the file `sidecars_helpers_test.go` (untagged) covers `sidecarContainerName`, `selectFirstSidecar`, etc. so per-package coverage on `internal/backend/docker/` doesn't drop below baseline.

---

## 8. Documentation updates required (in the same PR)

Per `AGENTS.md`'s "documentation rule" — every doc that mentions sidecars must update:

| File | Change |
|---|---|
| `docs/feature-parity.md` | Flip `Task.sidecars` row: `gap` → `shipped`; backends `cluster-only` → `both`; `e2e fixture` → `sidecars`; clear `limitations fixture`; link the plan |
| `docs/short-term-goals.md` | Mark Track 1 #1 status: "Done in v1.6 (PR for `feat: Task.sidecars`). Per-Task shared-netns on docker; cluster pass-through verified." |
| `docs/test-coverage.md` | Add `sidecars/` row under `### -tags integration`; remove the "Sidecars … out of scope" bullet from "By design" since it's now covered |
| `AGENTS.md` | New section `## Sidecars` documenting the supported field subset, the pause-container shared-netns model, the new `--sidecar-start-grace` (default 2s) and `--sidecar-stop-grace` (default 30s) flags, and the failure semantics table (mirrors Section 6 above, condensed) |
| `cmd/tkn-act/agentguide_data.md` | Mirror `AGENTS.md` after `go generate ./cmd/tkn-act/` (re-run is a hard step in Task 10) |
| `README.md` | Move "Sidecars" out of "Not yet supported" into "Tekton features supported"; mention both new flags |

---

## 9. Open questions for human review

1. **Start-grace period: 2s default fixed, or always probe-then-deferred?** Current proposal: 2s default with `--sidecar-start-grace` override. Alternative: default to 0s (start steps immediately) and ship probe support up-front. The 2s default is conservative and matches the time it takes for redis/postgres images to listen. Probe support is genuinely a follow-up because it requires a Tekton-style `tcpSocket`/`httpGet` evaluator we don't have today. **Decision needed: ship without probes, or block on probes?**
2. **Stop-grace default 30s — confirm.** New `--sidecar-stop-grace` flag, default 30s to match upstream Tekton's `terminationGracePeriodSeconds`. Previous draft used a hardcoded 10s; review feedback flagged that as too aggressive for postgres flush / kafka commit. 30s is a per-Task additive cost on every Task with sidecars when the sidecar misbehaves on shutdown — acceptable given how rare shutdown-misbehavior is. **Confirm 30s, or pick a different number.**
3. **Pause image pin and provenance.** `gcr.io/google-containers/pause:3.9` is the upstream Kubernetes pause image — ~700KB, cached forever after first pull, and the same image Tekton uses indirectly via Kubernetes. Alternatives: `registry.k8s.io/pause:3.9` (newer registry); a tkn-act-built busybox-as-pause to avoid the gcr.io dependency. **Confirm the gcr.io pin, or pick an alternative — and confirm we're OK adding it as a docker-backend implicit pull (not user-visible in the engine's `RunSpec.Images` table).**
4. **Should we ship a dedicated `sidecar-failure/` fixture?** Section 7.2 argues no (brittle to author with public images, and unit tests cover the paths). **Confirm — or push for the fixture and accept the upkeep.**
5. **`stream` value for sidecar log events: `"sidecar-stdout"` / `"sidecar-stderr"` vs. `"sidecar"` flat.** The fine-grained values match the existing `"stdout"` / `"stderr"` granularity for steps but break the symmetry with `EvtSidecarLog`'s single name. Recommendation: the fine-grained pair, because real users will want to attribute sidecar errors specifically.
6. **Container name length.** `tkn-act-<runID:8>-<taskRun>-sidecar-<name>` and `tkn-act-<runID:8>-<taskRun>-pause` can blow past Docker's 64-char container-name limit for long task and sidecar names. Mitigation: truncate `<taskRun>` and hash-suffix it the way `taskRunName` is already constructed elsewhere. We need to verify the existing limit isn't already being hit by step container names — if it is, this is a separate cleanup, not blocking.
7. **`Sidecar.workspaces` field, or implicit?** Upstream Tekton lets a Sidecar opt into a Task's workspaces explicitly via a `workspaces` list. Alternative (simpler): give every sidecar access to every Task workspace automatically. The explicit-opt-in form matches upstream and is what Section 2 proposes. **Confirm — and confirm the type shape (`WorkspaceUsage`) doesn't conflict with anything we already have.**
8. **Cluster sidecar log streaming — same PR or follow-up?** §4 defers to follow-up if non-trivial; §3.2 + the cluster fidelity rule require `sidecar-start` / `sidecar-end` events to ship now. Recommendation: ship logs in the same PR if `streamPodLogs`'s scanner-goroutine takes a single name argument that can be extended; otherwise defer logs and document the gap. **Confirm the escape hatch is acceptable.**

---

## 10. References

- Upstream Tekton sidecar docs (worked from prior knowledge, not re-fetched): https://tekton.dev/docs/pipelines/tasks/#specifying-sidecars
- The original v1 spec's punted-features list: `docs/superpowers/specs/2026-05-01-tkn-act-design.md` §1 ("Non-goals (v1, explicitly punted): Sidecar containers")
- Parity row: `docs/feature-parity.md` §"Task structure" — `Task.sidecars` (gap, cluster-only)
- Limitation fixture: `testdata/limitations/sidecars/pipeline.yaml`
- The implementation plan that turns this design into one-task-at-a-time work: `docs/superpowers/plans/2026-05-04-task-sidecars.md`
