## Sidecars (`Task.sidecars`)

`Task.spec.sidecars` declares long-lived helper containers that share
the Task's pod / network namespace for the duration of the Task.
Catalog Tasks use them for databases, mock services, registries.

Supported fields tkn-act honors on `--docker`:

| Field | Behavior |
|---|---|
| `name`, `image` | Required. Name must be unique within the Task and must not collide with a Step name. |
| `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources` | Same semantics as on Steps. |
| `volumeMounts` | Must reference a Task-declared `volumes` entry. |
| `workspaces` | Sidecar opts into Task workspaces by name; mounted at the workspace's path inside the sidecar. |
| `ports`, `readinessProbe`, `livenessProbe`, `startupProbe`, `securityContext`, `lifecycle` | Parsed for fidelity but ignored on `--docker` (`--cluster` forwards them to Tekton). Probe support is a deferred follow-up. |

Lifecycle on `--docker` (per Task with at least one sidecar):

1. Workspace prep (existing).
2. Sidecar images are pre-pulled by the engine; the pause image
   (`registry.k8s.io/pause:3.9`, ~700KB, cached forever
   after first pull) is pulled by the docker backend itself.
3. A tiny **pause container** is started with normal Docker
   networking. It owns the netns and only exits on signal (mirrors
   upstream Kubernetes / Tekton's "infra container" model).
4. Every sidecar in declaration order is started, each with
   `network_mode: container:<pause-id>`. Steps reach any sidecar at
   `localhost:<port>` exactly the way they would in a real Tekton
   pod.
5. After all sidecars are started, tkn-act waits a fixed grace
   (`--sidecar-start-grace`, default `2s`) before launching the
   first Step. Override the flag for slower-starting sidecars.
6. Each Step container is also started with
   `network_mode: container:<pause-id>`.
7. Sidecars are sent SIGTERM after the last Step ends, with a
   `--sidecar-stop-grace` window (default `30s`, matches upstream
   Tekton's `terminationGracePeriodSeconds`) before SIGKILL. The
   pause container is then stopped (hard 1s grace).
8. Workspace teardown (existing).

Tasks with zero sidecars pay no extra cost — no pause container, no
extra container starts.

Failure semantics:

| Scenario | Task status |
|---|---|
| Sidecar fails to start (image pull fails / container exits before grace) | `infrafailed`. One terminal `sidecar-end` event with `status: "infrafailed"`. |
| Sidecar crashes mid-Task (any sidecar — there is no "privileged" sidecar in the pause-container model) | Task status unchanged. Steps continue (the pause container, not the sidecar, owns the netns). The crash surfaces as `sidecar-end` with `status: "failed"` and the exit code. Matches upstream "sidecars are best-effort". |

JSON event stream additions (stable contract):

| Kind | Payload |
|---|---|
| `sidecar-start` | `task`, `step` (= sidecar name), `time`. `stream: "sidecar"`. |
| `sidecar-end` | `task`, `step`, `exitCode`, `status` (`succeeded` / `failed` / `infrafailed`), `time`. |
| `sidecar-log` | `task`, `step`, `stream` (= `"sidecar-stdout"` / `"sidecar-stderr"`), `line`, `time`. |

The `step` field is reused for the sidecar's name so existing JSON
consumers don't need new fields; consumers that care about
step-vs-sidecar disambiguation can branch on `kind` or on `stream`.

Cluster pass-through is automatic — tkn-act inlines the full
sidecars list into `pipelineSpec.tasks[].taskSpec.sidecars`, and
Tekton's controller takes it from there. The cluster watch loop
diffs `taskRun.status.sidecars[]` between events and emits matching
`sidecar-start` / `sidecar-end` events so the JSON event stream is
shape-equivalent across backends.
