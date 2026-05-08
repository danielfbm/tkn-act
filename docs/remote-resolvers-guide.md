# Remote resolvers — how-to guide

This guide walks through running `tkn-act` against an existing Tekton
cluster so that `taskRef.resolver:` / `pipelineRef.resolver:` blocks
resolve **on the remote cluster**, while the resolved Tasks still run
**locally on Docker** (or on the local k3d under `--cluster`).

This is "Mode B" — dispatch via `ResolutionRequest` CRD. It works with
any resolver the remote Tekton controller already has installed
(`hub`, `git`, `http`, `bundles`, `cluster`, plus any custom resolver
the cluster operator has registered).

For the design background and the catalog of direct resolvers, see
[`AGENTS.md`](../AGENTS.md#resolvers-track-1-9-shipped) and
[`docs/superpowers/plans/2026-05-04-resolvers.md`](superpowers/plans/2026-05-04-resolvers.md).

---

## When to use Mode B

Use Mode B when:

- Your team's Tekton catalog is only published through a Tekton hub or
  bundle registry that's reachable from a cluster, not from your laptop.
- Resolver auth (SSH keys, registry credentials, in-cluster catalog
  endpoints) is already configured cluster-side and you don't want to
  replicate it locally.
- You want bit-for-bit identical resolution between local runs and
  production runs — the same Tekton resolver controller answers both.

Use direct resolvers (the default) when:

- You only need `git`, `http`, `hub`, `bundles`, or `cluster` against
  publicly-reachable endpoints, or against a kubeconfig you trust.
- You want zero round-trips to a remote cluster on the resolution path.

The two modes are mutually exclusive per run: setting
`--remote-resolver-context=<ctx>` flips the registry into Mode B and
short-circuits the direct-resolver allow-list.

---

## Prerequisites

1. A reachable Tekton install on the remote cluster — at minimum the
   `tekton-pipelines-controller` and `tekton-pipelines-remote-resolvers`
   deployments. Verify:

   ```sh
   kubectl --context <CTX> -n tekton-pipelines get deploy \
     tekton-pipelines-controller \
     tekton-pipelines-remote-resolvers
   ```

2. The Resolution API CRD installed. Either v1beta1 (preferred) or
   v1alpha1 — `tkn-act` tries v1beta1 first and falls back on
   `NoKindMatchError`:

   ```sh
   kubectl --context <CTX> api-resources | grep resolutionrequests
   # resolutionrequests   resolution.tekton.dev/v1beta1   true   ResolutionRequest
   ```

3. A kubeconfig context that authenticates as a principal with
   `create / get / delete` on `resolutionrequests` in the namespace
   you'll target. **The default `default` namespace is rarely the
   right choice on shared clusters** — verify against your team's
   namespace:

   ```sh
   export KUBECONFIG=<path-to-your-kubeconfig>
   NS=<your-namespace>
   kubectl --context <CTX> auth can-i create resolutionrequests -n "$NS"
   kubectl --context <CTX> auth can-i get    resolutionrequests -n "$NS"
   kubectl --context <CTX> auth can-i delete resolutionrequests -n "$NS"
   # all three must print "yes"
   ```

   `tkn-act` never modifies your kubeconfig and never elevates
   privileges — whatever service-account the chosen context resolves
   to is what the cluster sees.

4. (Optional, for `hub` resolver) Know which hub the cluster's
   resolver points at. The default cluster install reads it from
   `tekton-pipelines/configmaps/hubresolver-config`:

   ```sh
   kubectl --context <CTX> -n tekton-pipelines \
     get cm hubresolver-config -o jsonpath='{.data.tekton-hub-api}{"\n"}'
   ```

   If it's an in-cluster hub URL (`http://tekton-hub-api...`), you
   won't be able to hit it from your laptop directly — Mode B is the
   only way.

---

## Walkthrough: hub resolver

### 1. Write a pipeline that references a remote task

`pipeline-hub.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: hub-resolver-demo
spec:
  params:
    - {name: greeting, default: hello}
  tasks:
    - name: scripted
      taskRef:
        resolver: hub
        params:
          - {name: catalog, value: catalog}     # hub catalog name
          - {name: kind,    value: task}
          - {name: name,    value: run-script}  # task in the catalog
          - {name: version, value: "0.1"}
      params:
        # The resolved task's defaults may reference a private registry.
        # Override to something the local Docker can pull:
        - {name: image,           value: docker.io/library/alpine:3}
        - {name: imagePullPolicy, value: IfNotPresent}
        - name: script
          value: |
            echo "GREETING: $(params.greeting)"
            uname -a
```

### 2. Run it

```sh
export KUBECONFIG=<path-to-your-kubeconfig>

tkn-act run -f pipeline-hub.yaml \
  --remote-resolver-context=<CTX> \
  --remote-resolver-namespace=<your-namespace> \
  --remote-resolver-timeout=120s \
  --param greeting='hi from manual run'
```

What you should see in pretty output:

```text
▶ hub-resolver-demo
  ↳ scripted resolver hub remote: <ns>/tkn-act-xxxxx (v1beta1)
  scripted/run-script │   GREETING: hi from manual run
  scripted/run-script │   Linux ... aarch64 Linux
✓ scripted  (~1s)
✓ PipelineRun succeeded
```

The `remote: <ns>/tkn-act-xxxxx (v1beta1)` line is the
ResolutionRequest the controller answered. It is always Deleted after
resolution, even on failure or `Ctrl+C`.

### 3. Verify the JSON event stream (for scripts / agents)

```sh
tkn-act run -f pipeline-hub.yaml \
  --remote-resolver-context=<CTX> \
  --remote-resolver-namespace=<your-namespace> \
  -o json | jq -c 'select(.kind | startswith("resolver-"))'
```

Expected events:

```json
{"kind":"resolver-start","task":"scripted","resolver":"hub", ...}
{"kind":"resolver-end","task":"scripted","status":"succeeded",
 "resolver":"hub","cached":false,
 "sha256":"<digest of resolved bytes>",
 "source":"remote: <ns>/tkn-act-xxxxx (v1beta1)", ...}
```

---

## Cache + `--offline`

Every successful resolution is written to
`<--resolver-cache-dir>/<resolver>/<sha256>.yaml` (default cache root
`$XDG_CACHE_HOME/tkn-act/resolved/`). Re-running the same pipeline
hits the cache and does not touch the network:

```sh
# First run (cold) — touches the cluster
tkn-act run -f pipeline-hub.yaml \
  --remote-resolver-context=<CTX> \
  --remote-resolver-namespace=<your-namespace>

# Second run — cached, microsecond resolution
tkn-act run -f pipeline-hub.yaml \
  --remote-resolver-context=<CTX> \
  --remote-resolver-namespace=<your-namespace>
# resolver-end shows "cached":true

# Third run — proves no network traffic happens at all
tkn-act run -f pipeline-hub.yaml --offline
```

`--offline` rejects every cache miss with exit code 4 (validate-time)
or a `resolver-end status:failed` event (run-time), so you can use it
in CI to guarantee no network egress after the cache has been seeded.

Inspect or prune the cache:

```sh
tkn-act cache list                # human-readable
tkn-act cache list -o json        # stable JSON
tkn-act cache prune --older-than 168h
tkn-act cache clear -y
```

---

## Walkthrough: git resolver via Mode B

The same flow works for any resolver the remote controller knows.
Example with the cluster's `git` resolver fetching a Task from a
public repo:

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: git-resolver-demo
spec:
  tasks:
    - name: clone-and-run
      taskRef:
        resolver: git
        params:
          - {name: url,        value: https://github.com/<org>/<repo>.git}
          - {name: revision,   value: main}
          - {name: pathInRepo, value: task/<name>/<ver>/<name>.yaml}
      # ... params for the resolved task ...
```

If the cluster's git resolver needs auth for a private repo, that
auth is configured cluster-side (typically in
`tekton-pipelines/configmaps/git-resolver-config` plus a referenced
Secret) — `tkn-act` does not transmit credentials.

A failure such as `authentication required: Repository not found`
surfaces as a `resolver-end status:failed` event with the controller's
message, and the parent task ends `failed` (exit code 5):

```json
{"kind":"resolver-end","task":"clone-and-run","status":"failed",
 "message":"remote: ResolutionRequest failed: reason=ResolutionFailed
  message=error getting \"Git\" \"<ns>/tkn-act-xxxxx\":
  clone error: authentication required: Repository not found.",
 "resolver":"git", ...}
```

---

## Multi-task pipelines

Multiple resolver-fetched tasks in one pipeline work the same way
each has its own `resolver-start` / `resolver-end` event pair, and
results flow between them via the standard `$(tasks.X.results.Y)`
substitution:

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: multi-resolver-demo
spec:
  tasks:
    - name: greet
      taskRef: { resolver: hub, params: [...] }
      params: [...]
    - name: compute
      runAfter: [greet]
      taskRef: { resolver: hub, params: [...] }
      params:
        - name: script
          value: |
            project="$(tasks.greet.results.string-result)"
            ...
    - name: report
      runAfter: [compute]
      taskSpec:           # plain inline task — no resolver
        steps: [...]
  results:
    - {name: project,     value: $(tasks.greet.results.string-result)}
    - {name: project_len, value: $(tasks.compute.results.string-result)}
```

Two hub fetches with identical resolved params share one cache entry
(the cache key is a SHA-256 of the substituted params, not of the
calling task name).

---

## Flag reference

All flags are on `tkn-act run`. The defaults are tuned for direct
mode; setting any of the `--remote-resolver-*` flags flips the
registry into Mode B.

| Flag | Default | Purpose |
|---|---|---|
| `--remote-resolver-context <ctx>` | unset | Kubeconfig context to dispatch ResolutionRequests through. **Setting this flips into Mode B.** |
| `--remote-resolver-namespace <ns>` | `default` | Namespace where ResolutionRequests are submitted. Override on shared clusters where you don't have RBAC in `default`. |
| `--remote-resolver-timeout <duration>` | `60s` | Per-request wait budget for the controller's reconcile. Bump for slow first-time hub fetches. |
| `--resolver-cache-dir <path>` | `$XDG_CACHE_HOME/tkn-act/resolved` | On-disk cache root. Shared with direct-mode runs. |
| `--offline` | off | Reject every cache miss. Validate-time + run-time. |
| `--resolver-config <file>` | unset | Optional YAML config for direct resolvers (hub bearer token, etc.). Ignored in Mode B. |

`--resolver-allow=...` and `--resolver-allow-insecure-http` are
direct-mode flags only — Mode B always uses whatever the remote
controller has registered.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `forbidden: cannot create resolutionrequests` | Kubeconfig context lacks RBAC in target namespace. | Use a namespace where `auth can-i create resolutionrequests` is `yes`. Set `--remote-resolver-namespace` accordingly. |
| `context "<ctx>" does not exist` | KUBECONFIG not exported, or pointing at the wrong file. | Export `KUBECONFIG=<path>` and re-check `kubectl --context <ctx> get ns`. |
| `remote: timeout after 60s` | Controller still reconciling on a cold-cache hub fetch. | Bump `--remote-resolver-timeout=120s` or longer. |
| `no matches for kind "ResolutionRequest"` | Tekton install too old / Resolution API not installed. | Upgrade Tekton, or check whether v1alpha1 is the only kind present (`tkn-act` falls back automatically). |
| Resolved task fails to pull its image | The remote task's default `image:` points at a private registry your laptop can't reach. | Override `params.image` in the calling pipeline (see the hub walkthrough). |
| `securityContext: runAsNonRoot` ignored on docker | Expected — `securityContext` is parsed but not enforced on `--docker` mode. | Use `--cluster` for higher-fidelity Tekton semantics (k3d will honor the field). |
| `cache miss while --offline is set` | First run wasn't recorded, or `--resolver-cache-dir` differs between runs. | Run once without `--offline` first to seed the cache; keep the cache-dir flag consistent. |

---

## Security notes

- Mode B uses **the user's kubeconfig identity** — whatever
  service-account the chosen context resolves to is what the cluster
  sees. `tkn-act` never elevates privileges and never stores
  credentials of its own.
- The chosen kubeconfig context's RBAC must include `create / get /
  delete` on `resolutionrequests` in `--remote-resolver-namespace`.
- The `ResolutionRequest` is **always Deleted** on the way out
  (success, controller-reported failure, timeout, **or
  `context.Cancel`** — `Ctrl+C` mid-resolution still triggers cleanup
  via `context.Background()` in the deferred `Delete`).
- The `cluster` direct resolver is **off by default** for the same
  reason your kubeconfig may point at production. Mode B does not
  require opting into it: the remote cluster's controller answers
  `cluster:` refs against its own in-cluster API.
- The `resolver-end` event's `sha256` field digests the bytes the
  controller returned — agents that need provenance can pin or
  compare these across runs.

---

## See also

- [`AGENTS.md`](../AGENTS.md#resolvers-track-1-9-shipped) — full
  field-by-field semantics, including direct-mode resolver tables.
- [`docs/superpowers/plans/2026-05-04-resolvers.md`](superpowers/plans/2026-05-04-resolvers.md)
  — implementation plan for all six resolver phases.
- [`docs/superpowers/specs/2026-05-04-resolvers-design.md`](superpowers/specs/2026-05-04-resolvers-design.md)
  — design rationale and trade-offs.
- [`docs/feature-parity.md`](feature-parity.md) — single source of
  truth for what's `shipped` vs. `gap`.
