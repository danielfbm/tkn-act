## Cluster backend (`tkn-act run --cluster`)

`--cluster` submits the whole Pipeline as one `PipelineRun` (with every
Task spec inlined) to a real Tekton controller running in the
project's ephemeral k3d cluster, then watches it to completion.

### Run lifecycle

- Each run gets its own namespace, `tkn-act-<first 8 chars of the run
  ID>`. ConfigMap / Secret volume sources are copied into it from the
  `--configmap*` / `--secret*` stores (and `kind: ConfigMap` /
  `kind: Secret` documents in `-f`) before the PipelineRun is created.
  Non-UTF-8 ConfigMap values are stored in `binaryData`. A source that
  can't be resolved fails the run before submit, unless every volume
  referencing it is `optional: true` — then nothing is copied and
  Kubernetes mounts the volume empty, as on docker.
- The PipelineRun watch is re-established automatically when the API
  server closes it (it does so after `--min-request-timeout`, typically
  30–60 minutes), so long runs no longer fail with "watch closed before
  terminal status".
- Step and sidecar logs are attached once each container is running
  (or has already terminated), retrying while the pod is still
  initializing, so slow-to-start pods keep their logs. Lines longer
  than 1 MiB are truncated with a marker. The last lines are flushed
  (bounded 5s) before `run-end`.
- **Ctrl-C / cancellation**: `tkn-act` patches the PipelineRun's
  `spec.status` to `Cancelled` (best effort, 10s budget) so Tekton
  stops its pods, then exits with code `130`.

### What is left behind

The run namespace — PipelineRun, TaskRuns, pods, workspace PVCs and the
copied ConfigMaps / Secrets — is **not deleted** when the run ends; it
stays for inspection (`kubectl -n tkn-act-<id> get all`). There is no
automatic garbage collection. Clean up one run with
`kubectl delete namespace tkn-act-<id>`, or everything with
`tkn-act cluster down -y`, which deletes the whole ephemeral cluster. Note that copied Secrets
persist in that namespace until then.

### Spec fidelity

Task specs are forwarded verbatim in Tekton v1 shape: Step / Sidecar /
`stepTemplate` resources are sent as `computeResources` (the v1beta1
`resources` key is accepted on input as an alias), and
`securityContext`, `envFrom`, `env[].valueFrom` and `Step.timeout` are
passed through unchanged.
