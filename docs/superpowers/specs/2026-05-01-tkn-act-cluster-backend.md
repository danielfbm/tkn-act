# tkn-act cluster backend (v1.1) — design spec

**Date:** 2026-05-01
**Status:** Approved (extends v1 spec)
**One-liner:** Add a `--cluster` execution backend that runs PipelineRuns on an ephemeral local Kubernetes cluster (k3d), giving Tekton-fidelity test runs without the user owning a real cluster.

This spec extends `2026-05-01-tkn-act-design.md`. Read that first.

---

## 1. Goals & non-goals

### Goals
- `tkn-act run --cluster` runs the same Pipeline that the Docker backend runs, but on a real Tekton install in an ephemeral k3d cluster.
- Cluster lifecycle is **invisible by default**: first `--cluster` run lazily creates the cluster, installs Tekton, runs the pipeline, and leaves the cluster up for fast re-runs.
- Explicit lifecycle subcommands: `tkn-act cluster up | down | status`.
- Tekton-fidelity is the differentiator: persona B (catalog/platform engineer in v1 spec) gets a real Tekton reconciliation loop, real CRDs, real entrypoint shim, real result-files semantics.
- Stays a single static Go binary — no Python/scripts.

### Non-goals (v1.1)
- `kind` as an alternative driver (k3d only; driver abstraction is left as v1.2 work).
- User-supplied workspace host paths (`-w name=./path`) — cluster mode uses `volumeClaimTemplate` workspaces only, with a warning if `-w` is passed.
- Multi-cluster support (one cluster per host: name `tkn-act`).
- Cluster sharing between multiple `tkn-act` instances.
- Caching/pre-pulling images into the cluster (Tekton handles pulls per-Step).
- TLS / private registry auth in cluster mode.
- Replacing the Docker backend — Docker stays default, cluster is opt-in.

### What persona B gets that Docker can't give
- Real Tekton entrypoint shim (sequential step ordering inside one pod).
- Real `volumeClaimTemplate` workspace lifecycle (PVC bound across all tasks of a PipelineRun).
- `$(context.pipelineRun.uid)` and other context vars set by the real controller.
- Catalog Tasks loaded into the cluster work the same way they do in production.

---

## 2. Architecture additions

The v1 architecture stays intact. We add three new units and one engine extension:

```
internal/cluster/                 driver: lifecycle of the local cluster
  driver.go                       Driver interface (Ensure/Status/Delete)
  k3d/                            k3d implementation (shells out to `k3d`)

internal/cluster/tekton/          Tekton installer (kubectl apply + readiness wait)

internal/backend/cluster/         Backend implementation:
                                    - constructor takes a driver + a kubernetes client
                                    - submits a PipelineRun, watches it, streams logs
                                    - implements both backend.Backend (delegates to RunPipeline)
                                      and the new backend.PipelineBackend opt-in interface

internal/engine/engine.go         minor extension: when backend satisfies
                                    PipelineBackend, skip the DAG and delegate the
                                    whole PipelineRun
```

**New optional interface:**

```go
package backend

type PipelineBackend interface {
    Backend
    RunPipeline(ctx context.Context, in PipelineRunInvocation) (PipelineRunResult, error)
}

type PipelineRunInvocation struct {
    RunID, PipelineRunName string
    Pipeline tektontypes.Pipeline
    Tasks    map[string]tektontypes.Task // referenced by name from the bundle
    Params   []tektontypes.Param
    Workspaces map[string]WorkspaceMount  // name → host path (warning-only in cluster mode)
    LogSink  LogSink
    EventSink func(Event)                  // optional; cluster backend emits task-start/end events
}

type PipelineRunResult struct {
    Status   string                            // succeeded | failed
    Tasks    map[string]TaskOutcomeOnCluster   // task name → status + results
    Started, Ended time.Time
}
```

**Engine routing:**

```go
// In engine.RunPipeline, before DAG construction:
if pb, ok := e.be.(backend.PipelineBackend); ok {
    return e.runViaPipelineBackend(ctx, pb, in)
}
// else: existing per-task orchestration path unchanged
```

This keeps Docker untouched and lets the cluster backend own its execution model.

---

## 3. Cluster lifecycle (k3d driver)

### 3.1 Ensure
1. Check if cluster `tkn-act` exists: `k3d cluster list -o json` and look for name match.
2. If absent: `k3d cluster create tkn-act --no-lb --wait --timeout 120s --k3s-arg "--disable=traefik@server:0"`. Disabling traefik shaves ~10s and avoids port conflicts.
3. Capture the kubeconfig: `k3d kubeconfig get tkn-act` → write to a per-cluster path under `$XDG_CACHE_HOME/tkn-act/cluster/kubeconfig`.
4. Wait for nodes Ready (client-go list).

### 3.2 Tekton install
Idempotent. On first run only:
1. List CRDs; if `pipelines.tekton.dev` exists, skip install.
2. Else apply `https://storage.googleapis.com/tekton-releases/pipeline/previous/v0.65.0/release.yaml` (pinned version; bumpable via constant). Use `kubectl apply -f` shell-out — server-side apply via client-go for hundreds of resources is ~250 lines we don't want to write.
3. Wait for `tekton-pipelines-controller` and `tekton-pipelines-webhook` deployments to have all replicas Ready (timeout 180s).

### 3.3 Status
`tkn-act cluster status`:
```
cluster:  tkn-act          [running]
kubeconfig: ~/.cache/tkn-act/cluster/kubeconfig
tekton:   v0.65.0           [ready]
recent:   pipelinerun/foo-abc123  [Succeeded] 14s
```
Reads from k3d (cluster state), kube API (deployment readiness), and the `tekton-pipelines` namespace (last few PipelineRuns by creation time).

### 3.4 Down
`tkn-act cluster down`: confirm with user (`-y` to skip), `k3d cluster delete tkn-act`, wipe kubeconfig path. Spec discipline: confirmation default-on; `-y/--yes` flag for scripts.

### 3.5 What about `kind`?
Not in v1.1. `internal/cluster/driver.go` defines a `Driver` interface so adding `kind` later is just another implementation. We do NOT ship that implementation in v1.1.

---

## 4. PipelineRun submission flow

`cluster.Backend.RunPipeline`:

1. Resolve all referenced Tasks from the bundle into `Task` objects.
2. Apply Tasks to a fresh per-run namespace `tkn-act-<runid8>` (so multiple invocations don't collide). Namespace is auto-created.
3. Build a single `PipelineRun` resource:
   - `pipelineSpec` inlined (so we don't need to apply the Pipeline separately).
   - Workspaces use `volumeClaimTemplate` with `accessModes: [ReadWriteOnce]`, `1Gi` (k3d's local-path provisioner satisfies this).
   - Params from the user.
   - Service account: default in the namespace.
4. Apply the PipelineRun via client-go.
5. Watch the PipelineRun via informer until `Status.Conditions[Succeeded].Status` is `True` or `False` (or `ctx` cancelled).
6. As TaskRuns are created and progress, stream their pod logs (one pod per TaskRun; each step is a container `step-<name>`). Log lines flow into `LogSink` tagged with task+step.
7. Emit `EvtTaskStart`/`EvtTaskEnd` events as we observe TaskRun state transitions, so the reporter can render the same tree as the Docker backend.
8. On completion: read `taskResults` from each TaskRun's status; populate `PipelineRunResult.Tasks`.
9. On failure or context cancellation: leave the PipelineRun in the cluster for inspection; print `kubectl -n <ns> logs/describe` hints.
10. Cleanup of the namespace: NOT done automatically. `tkn-act cluster prune` (added in v1.2) will sweep old namespaces. Rationale: post-mortem inspection is the whole point of the cluster backend.

### 4.1 Workspace strategy
- **Auto-allocated workspaces**: `volumeClaimTemplate` on the PipelineRun with default storage class. Lives until namespace is deleted.
- **User-supplied `-w name=./path`**: print a warning and ignore the path. v1.1 doesn't bind-mount host paths into k3d nodes. (v1.2: support via `k3d cluster create --volume <host>:<container>` at cluster create time, then `hostPath` PVs.)

### 4.2 Logs
One TaskRun = one pod. Steps are containers named `step-<name>`. Strategy:
- After observing TaskRun pod creation, start `client-go`'s `Watch` on pod events for that pod.
- For each container, open a streaming `GetLogs(Follow: true)` call. Pipe lines into `LogSink`.
- Tekton's entrypoint shim ensures only one container is active at a time, so log streams don't interleave confusingly.

### 4.3 Events back to the reporter
The cluster backend translates Tekton resource transitions into the engine's `reporter.Event` shape via the `EventSink` callback in the invocation. The reporter doesn't need to know about Tekton at all.

---

## 5. CLI changes

### New global flag (already in v1)
- `--cluster` selects the cluster backend.

### New subcommands
- `tkn-act cluster up` — ensure cluster + Tekton.
- `tkn-act cluster down [--yes]` — delete cluster after confirmation.
- `tkn-act cluster status` — show cluster + tekton state.

### Behavioral changes
- `tkn-act run --cluster` calls `cluster up` first (lazy ensure). Idempotent — fast on subsequent runs.
- `tkn-act validate` does NOT touch the cluster — pure local check.
- `--max-parallel` is honored on Docker but ignored in cluster mode (Tekton's own scheduler runs the DAG); a warning is printed if both flags appear.

### New flag on `run`
- `--cluster-keep-namespace` (default: true) — keep per-run namespace for inspection. Pair with future `cluster prune`.

---

## 6. Error classes (extends v1 §4)

| Class | Examples | Exit | UX |
|---|---|---|---|
| User-input | malformed YAML, unknown taskRef | 2 | unchanged |
| Backend/env | k3d binary missing, kubectl missing, port conflict, `pipelines.tekton.dev` failed to install | 3 | install hints; "run `tkn-act cluster status`" |
| Runtime | TaskRun fails on the cluster | 1 | log tail + `kubectl -n <ns> logs <pod>` hint |
| Internal | panics | 70 | preserve namespace; print run-id |

`tkn-act` does NOT bundle `k3d` or `kubectl`. If missing, error tells the user how to install (`brew install k3d` / `brew install kubectl`).

---

## 7. Testing strategy

### 7.1 Unit tests
- **`cluster/k3d`**: shell-out is gated behind a `Runner` interface; tests inject a fake runner asserting argv. Real k3d is never invoked in unit tests.
- **`cluster/tekton`**: kube interactions abstracted behind a `Client` interface; unit tests use the fake client from `client-go/kubernetes/fake`. Verifies CRD existence check, apply skip, deployment readiness wait.
- **`backend/cluster`**: uses `client-go/kubernetes/fake` + a fake driver. Verifies PipelineRun construction (correct workspace bindings, namespace, params). Watch logic uses fake informers.

### 7.2 Integration tests (build tag `cluster`)
One smoke test per CI run: `TestClusterE2E_Hello` runs the same `testdata/e2e/hello/pipeline.yaml` from v1 through the cluster backend. Requires `k3d`, `kubectl`, and Docker. Skipped if any are missing.

### 7.3 What we don't test in v1.1
- `cluster down --yes` automation (manual exercise).
- Re-install of Tekton after a version bump.
- Concurrent `tkn-act run --cluster` from two terminals (single-cluster lock is out of scope; documented as a known limitation).

---

## 8. Observability / debug

- `--debug` prints every k3d/kubectl shell command before running it.
- Cluster status output already shows the kubeconfig path; users can `KUBECONFIG=... kubectl ...` directly.
- A run preserves `pipelinerun/<name>` in `tkn-act-<runid8>` namespace; the run summary prints `kubectl -n tkn-act-<runid8> describe pipelinerun <name>` so the user can poke immediately.

---

## 9. Risks & open questions

- **Tekton release pinning.** We pin to `v0.65.0`. Bumping requires a smoke test and a CHANGELOG entry. Future `tkn-act cluster upgrade` may automate, but not in v1.1.
- **Namespace bloat.** Every run creates a new namespace and never cleans it. Acceptable for "I'll inspect failures" UX; risky for a long-lived cluster. v1.2 adds `cluster prune --older-than 24h`.
- **k3d image pull throttling.** Tekton images pulled per-step on first run; subsequent runs reuse the node-local image cache. No mitigation needed in v1.1.
- **`kubectl` shell-out for `apply -f`.** The Tekton release YAML has ~50 resources including CRDs; client-go server-side apply is doable but adds significant code. Shell-out keeps the spec compact. Risk: divergent kubectl versions parse `release.yaml` slightly differently. Mitigation: pin to a tested kubectl + Tekton pair.
- **Streaming logs with pod restarts.** If a TaskRun pod is rescheduled (rare on k3d), our log stream gets a closed-pipe. We restart the stream; some lines may dup. Documented quirk.
