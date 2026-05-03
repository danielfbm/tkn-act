# Implementation plan — cross-backend fidelity

**Date:** 2026-05-03
**Status:** Ready to execute
**Tracks:** Track 2 #1–#4 of `docs/short-term-goals.md`
**Estimated size:** one PR, three commits, ~2 days

This plan is execution-ready: a fresh session should be able to read
this doc and the referenced files and finish the work without further
brainstorming. It assumes the v1.2 docker-fidelity work merged in PR #4
is the current state of `main`.

---

## 1. Goal

Every v1.2-and-earlier feature behaves identically under `--docker`
and `--cluster`, validated by the same fixture descriptor running
through both backends in CI.

Concrete acceptance criteria:

- A new shared fixture descriptor type lives in
  `internal/e2e/fixtures` (or similar) and is consumed by both
  `internal/e2e/e2e_test.go` and `internal/clustere2e/cluster_e2e_test.go`.
- Every fixture under `testdata/e2e/` runs in *both* test binaries
  with the same `wantStatus`. Fixtures with a backend-specific
  divergence are explicitly tagged in the descriptor (only one
  expected today: see §3.6 retries).
- The `cluster-integration` workflow runs the full fixture set, not
  just `hello`.
- Tekton's `Reason: PipelineRunTimeout` / `TaskRunTimeout` map to
  task status `timeout` in `internal/backend/cluster/run.go`.
- Volumes referencing `configMap` / `secret` work under `--cluster`
  by applying ephemeral resources into the run namespace, sourced
  from the same `volumes.Store` the docker backend uses.
- The `cluster` backend emits `task-retry` events for Tekton retry
  attempts (one per failed attempt), so the JSON event stream is
  shape-equivalent across backends.

---

## 2. Files you will touch

| File | Why |
|---|---|
| `internal/e2e/fixtures/fixtures.go` (new) | Shared fixture descriptor + table |
| `internal/e2e/e2e_test.go` | Switch to consume the shared descriptor |
| `internal/clustere2e/cluster_e2e_test.go` | Same |
| `internal/backend/cluster/run.go` | Map Tekton `Reason` → task status; surface TaskRun retry watch |
| `internal/backend/cluster/cluster.go` (or new `internal/backend/cluster/volumes.go`) | Apply ephemeral ConfigMap / Secret resources before `RunPipeline` |
| `cmd/tkn-act/run.go` | Pass the configured `volumes.Store`s into the cluster backend constructor (currently only the engine sees them) |
| `internal/backend/cluster/run.go` | Emit `task-retry` events from a TaskRun watch |
| `.github/workflows/cluster-integration.yml` | Drop the `_test.TestClusterE2EHello` filter (currently implicit; just stop scoping) |
| `docs/test-coverage.md` | Update §3 ("Smoke vs. real e2e gaps") once the gap is closed |

---

## 3. Step-by-step

### 3.1. Pre-flight read (~15 min)

Read these in order; nothing is changed yet. The plan assumes you've
seen each file:

1. `internal/e2e/e2e_test.go` — fixture harness for docker
2. `internal/clustere2e/cluster_e2e_test.go` — fixture harness for cluster
3. `internal/backend/cluster/run.go` — `RunPipeline`, `watchPipelineRun`,
   `streamAllTaskRunLogs`
4. `internal/backend/cluster/cluster.go` — `Backend` lifecycle (`Prepare`,
   `Cleanup`)
5. `internal/volumes/store.go` — bytes source the docker side uses
6. `internal/engine/policy.go` — what `task-retry` events look like in
   docker mode (the contract you're matching on the cluster side)
7. `docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md`,
   §3 "Agent-contract additions"

### 3.2. Shared fixture descriptor (commit 1)

Create `internal/e2e/fixtures/fixtures.go`. Suggested shape:

```go
package fixtures

import "github.com/danielfbm/tkn-act/internal/tektontypes"

type Fixture struct {
    Dir          string                     // testdata/e2e/<dir>
    Pipeline     string                     // pipeline name in the YAML
    Params       map[string]string
    WantStatus   string                     // "succeeded" | "failed" | "timeout"
    ConfigMaps   map[string]map[string]string // name -> key -> value (inline seed)
    Secrets      map[string]map[string]string
    DockerOnly   bool                       // skip in cluster harness
    ClusterOnly  bool                       // skip in docker harness
    Description  string                     // human-readable for test names
}

func All() []Fixture { /* table of every fixture under testdata/e2e/ */ }
```

Then update both test files to iterate `fixtures.All()` and call a
backend-specific `runFixture` helper. The helpers stay in their own
files; only the descriptor list is shared.

Commit: `test(e2e): share fixture descriptor across docker and cluster harnesses`

### 3.3. Tekton condition → task-status mapping (commit 2)

In `internal/backend/cluster/run.go:watchPipelineRun`, the watch
currently only inspects `cm["status"]`. Tekton sets a `Reason` field
that distinguishes `PipelineRunTimeout`, `Cancelled`, `Failed` (step
exit), etc. Extend the switch to:

- `status == "True"` → `succeeded`
- `status == "False"` && `reason == "PipelineRunTimeout"` →
  `timeout`
- `status == "False"` && `reason == "TaskRunTimeout"` (rare on
  PipelineRun but defensively) → `timeout`
- `status == "False"` (any other reason) → `failed`

Mirror the `oc.Message` shape the docker backend uses (
`"task timeout 30s exceeded"`) so pretty output reads the same.

Add a unit test in `internal/backend/cluster/cluster_test.go`
(table-driven over fake unstructured PipelineRun objects).

Commit: `feat(cluster): map Tekton timeout reason to status "timeout"`

### 3.4. Volumes via ephemeral ConfigMap/Secret apply (still commit 2)

The cluster backend's `Prepare` runs once per PipelineRun. Before
`RunPipeline` submits the PipelineRun:

1. Walk `pl.Spec.Tasks` + `pl.Spec.Finally`. For each `taskSpec.volumes`
   entry whose source is `configMap` or `secret`, collect the source
   names.
2. For each unique configMap name, call `cmStore.Resolve(name)` to
   get `map[string][]byte`. Apply a `corev1.ConfigMap` to the run
   namespace with those keys.
3. Same for secrets, applying `corev1.Secret` (`type: Opaque`).
4. Use `metav1.ObjectMeta.OwnerReferences` pointing at the
   PipelineRun once it's created, so namespace teardown removes
   them. Or just rely on the ephemeral namespace's lifecycle (
   `tkn-act-<runid>`).

Wiring: `cmd/tkn-act/run.go` constructs the `volumes.Store`s today
only for the engine's `VolumeResolver`. Plumb them into
`clusterbe.New(... cm, sec)` as well; the cluster backend stashes
them on `Backend` and uses them in `Prepare`.

Add a unit test using the existing fake-kube-client pattern in
`internal/backend/cluster/cluster_test.go` that asserts a ConfigMap
was applied with the expected keys when the spec declares one.

Commit: same as 3.3 — both are "make the cluster backend match docker
semantics."

### 3.5. `task-retry` events from cluster mode (commit 3)

The cluster backend already streams TaskRun pod logs in
`streamAllTaskRunLogs`. Extend that watch (or add a sibling watch on
the TaskRun resource itself) to:

- Track `status.podName` over time. When `podName` changes, that's a
  retry.
- Emit `reporter.Event{Kind: EvtTaskRetry, Task: pipelineTaskName,
  Attempt: N, Status: "failed", Message: <reason if available>}`
  per retry.
- The terminal `task-end` is already emitted by the
  PipelineRun-level watch; that path needs to read the final
  TaskRun's `status.retriesStatus` length and set `Attempt` on the
  outgoing event.

Note: `runViaPipelineBackend` in the engine is what actually emits
task-end for cluster mode today, but it does so based on
`PipelineRunResult.Tasks[name]` — extend `TaskOutcomeOnCluster` to
carry an `Attempts int` field, populate it from
`status.childReferences[].retriesStatus` (or similar; check the
Tekton API), and surface it on the engine's `task-end` event.

Add a unit test that feeds a fake unstructured PipelineRun whose
TaskRun has `retriesStatus` of length 2 and asserts the engine emits
two `task-retry` events plus a `task-end` with `Attempt: 3`.

Commit: `feat(cluster): emit task-retry events for Tekton retry attempts`

### 3.6. Known divergences to bake into the descriptor

| Fixture | Cluster behavior | Action |
|---|---|---|
| `timeout/` | Same status after 3.3 | None |
| `retries/` | Tekton retries, our task-end carries `attempts: N` after 3.5 | None |
| `onerror/` | Tekton handles `onError: continue` natively | None |
| `step-results/` | Tekton handles per-step results natively at the same `/tekton/steps/<step>/results/` path | None |
| `volumes/` | Works after 3.4 | None |
| `multilog/` | Cluster backend interleaves logs from multiple TaskRun pods through the LogSink | Verify; may need to label `Fixture.ClusterOnly = false` and ensure the existing log-streaming path produces ordered events |

If a divergence remains that we don't intend to fix now, mark it on
the descriptor (`DockerOnly: true`) and add a TODO to the
short-term-goals doc.

### 3.7. CI workflow (still commit 3)

Edit `.github/workflows/cluster-integration.yml`:

- The `go test -tags cluster ...` step already runs the whole
  package, so once `cluster_e2e_test.go` is iterating
  `fixtures.All()` the new tests run automatically. No workflow
  change strictly needed.
- Bump the timeout if the matrix gets large (currently 25 min).
  Each cluster fixture adds ~30s after warm-up.

If you want a quick turnaround, add a `--scope=fast` flag (e.g., env
var honored by the harness) so a developer can run just the docker
+ a single cluster fixture locally. Optional polish.

### 3.8. Docs (commit 3 tail)

Update `docs/test-coverage.md` §3 ("What is NOT covered" → "Smoke vs.
real e2e gaps") to reflect that the cluster job now exercises the
full fixture set. Remove the bullet that says "the cluster job only
runs the `hello` fixture today."

---

## 4. Out of scope (do not do here)

- Sidecars (Track 1 #1, separate spec).
- PipelineRun-level timeouts (Track 1 #2).
- Matrix (Track 1 #3).
- A `kind` driver. Track 2 follow-up; the workflow is already
  driver-agnostic.

---

## 5. Verification before opening the PR

```sh
# Unit + lint + format.
go vet ./...
go build ./...
go test -race -count=1 ./...

# Docker integration (requires Docker running locally).
go test -tags integration -count=1 -timeout 15m ./internal/e2e/...

# Cluster integration (requires kubectl + k3d).
go test -tags cluster -count=1 -timeout 25m ./internal/clustere2e/...
```

If you don't have docker / k3d locally, the GitHub Actions workflows
will run them once the PR is open. Local dev path:

```sh
tkn-act doctor -o json | jq '.checks'
```

PR template: title `feat(cluster): cross-backend fidelity for v1.2 features`,
body summarising the three commits and the closed gap from
`docs/test-coverage.md`. Merge per project default: squash +
delete-branch.

---

## 6. After this lands

Track 2 is complete. Natural next pickups, in order:

1. **Track 1 #2** — PipelineRun-level timeouts. Small, builds on the
   per-Task primitive we already have.
2. **Track 1 #5** — load `kind: ConfigMap` / `kind: Secret` from
   `-f`. Small loader change; reuses the v1.2 store.
3. **Track 1 #4** — `stepTemplate`. Pure substitution-time merge.
4. **Track 1 #1** — sidecars. Larger; needs its own brainstorm + spec.
