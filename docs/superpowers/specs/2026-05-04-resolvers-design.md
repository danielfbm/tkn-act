# Resolvers (Track 1 #9) — design spec

**Date:** 2026-05-04
**Status:** Draft for review
**One-liner:** Honor Tekton's `taskRef.resolver` / `pipelineRef.resolver` blocks across two operating modes (direct: tkn-act fetches; remote: tkn-act submits a Tekton `ResolutionRequest` to a real cluster), with lazy resolution at task-dispatch time so `resolver.params` can reference upstream-task results.

This spec extends `2026-05-01-tkn-act-design.md`, `2026-05-01-tkn-act-cluster-backend.md`, `2026-05-02-tkn-act-docker-fidelity-design.md`, and the v1.5 plans for ConfigMap/Secret loading, pipeline results, pipeline-level timeouts, and stepTemplate. Read those first.

---

## 1. Goal & non-goals

### Goal

Let users run any community / catalog Pipeline whose Tasks are referenced via `taskRef.resolver` (the dominant catalog-consumption pattern) without first hand-inlining every referenced Task. The same applies to `pipelineRef.resolver`. The result is fully-inlined `Task` / `Pipeline` YAML that flows into the existing engine + backend pipeline unchanged.

### In scope (v1.6)

| Resolver | Direct mode (tkn-act fetches) | Remote mode (delegate to cluster) |
|---|---|---|
| `git`     | shallow clone + read `pathInRepo` at `revision` | submit `ResolutionRequest` |
| `hub`     | Tekton Hub HTTP API (`hub.tekton.dev`, `artifacthub.io`) | submit `ResolutionRequest` |
| `http`    | plain `GET`, optional bearer-token via env | submit `ResolutionRequest` |
| `bundles` | OCI bundles via `go-containerregistry` | submit `ResolutionRequest` |
| `cluster` | read from kube cluster via `KUBECONFIG` (only meaningful if user has one) | submit `ResolutionRequest` |

### Out of scope (deferred)

- **Custom resolvers** (`resolver: my-private-resolver`) — only valid in remote mode, where the controller knows them. Direct mode rejects unknown resolver names with exit 4.
- **`StepActions` (`Step.ref`)** — separate Track 1 #8 work; resolution semantics overlap but the lifecycle (per-Step vs per-Task) is different. This spec does not solve it; the resolver layer added here is reusable when StepActions land.
- **Verifying signed Tekton bundles / Tekton Chains attestations**. Bytes are pulled opaquely; a follow-up spec can add cosign / sigstore verification with a `--verify-bundles` flag.
- **OCI image-pull credentials chained through Docker config** — v1.6 only honors `DOCKER_CONFIG` / `~/.docker/config.json` for `bundles`, plus a single `--resolver-config` file for hub/http tokens.
- **Caching invalidation by upstream tag changes.** A `git` resolver pinned to `revision: main` happily serves a stale cache hit; users invalidate by clearing `--resolver-cache-dir` or pinning a SHA.
- **Pipeline-result-substitution into resolver params.** Pipeline-level results land at run-end; `resolver.params` are bound at task-dispatch time. The substitution context for resolver params is the same as for `PipelineTask.Params`: `$(params.X)`, `$(tasks.X.results.Y)`, `$(context.*)`. Pipeline results never feed back in (Tekton itself doesn't either).

---

## 2. The dual-mode design

Tekton's resolver framework was built so the controller can negotiate with private hubs, custom auth providers, and mirror configurations the user can't replicate locally. tkn-act's "direct mode" is convenient for the common case (public catalog, plain git) but inherently limited. We expose both:

### Mode A — direct resolver (default)

`tkn-act` itself fetches and resolves. No external cluster required. Faster, works offline against cached bytes, supports the four common resolvers (git/hub/http/bundles) plus `cluster` if `KUBECONFIG` points at a real cluster.

### Mode B — remote resolver (opt-in)

User configures a remote cluster the same way `kubectl` does (`--remote-resolver-context <kubeconfig-context>` or a config file). For every `taskRef`/`pipelineRef` carrying a `resolver:` block, tkn-act:

1. Constructs a `ResolutionRequest` (`resolution.tekton.dev/v1beta1`) with `spec.params` mirroring the resolver's params (already substituted for `$(tasks.X.results.Y)` etc).
2. Submits it to the remote cluster's resolver controller.
3. Polls `status.conditions[Succeeded]` (default poll 1s, max wait `--remote-resolver-timeout`, default 60s).
4. Decodes `status.data` (base64 of the resolved YAML bytes).
5. Loads the bytes back through `internal/loader` and uses the resulting Task / Pipeline.

The local backend (docker or k3d) then runs the resolved spec exactly as if the user had pasted it in `-f`.

### Tradeoffs

| Aspect | Direct (A) | Remote (B) |
|---|---|---|
| Offline | Yes (with cache) | No |
| Private resolver | Only via custom config that tkn-act understands | Yes — controller already knows them |
| Auth | Tokens via `--resolver-config` | Cluster's existing service-account RBAC |
| Latency | Network round-trip per cache miss | Network round-trip + controller reconcile (typically 1-3s) |
| Fidelity to user's prod pipeline | Approximate | Exact (same controller produces same bytes) |
| Required external system | None (or kubeconfig for `cluster`) | A reachable Tekton cluster with the `resolution.tekton.dev` CRD |

The default is direct; remote is opt-in via a single flag.

---

## 3. Lazy-resolution engine architecture

This is the novel piece. Tekton's controller is a reconciler — it can defer fetching a `taskRef` until the upstream tasks have produced the results that feed `resolver.params`. tkn-act's engine is a pipeline-runner that resolves params before dispatch. To honor `resolver.params` that reference task results, we make resolution lazy: it happens at task-dispatch time, after the substitution context for that task is fully populated.

### Phases

1. **Load time** (`internal/loader`): identify every `PipelineTask` whose `taskRef.resolver` (or `pipelineRef.resolver`) is set. Don't fetch. Tag the PipelineTask with `Unresolved=true` so the validator + engine know to defer. Keep the raw resolver block on the type — `TaskRef.Resolver`, `TaskRef.ResolverParams`.
2. **Validate time** (`internal/validator`): for each unresolved ref, validate that:
   - `resolver` name is one of the in-scope direct list, OR `--remote-resolver-context` is set.
   - `params` only reference identifiers that *could* be available at dispatch time: `$(params.X)`, `$(tasks.X.results.Y)` where X is an earlier PipelineTask, `$(context.*)`.
   - `--offline` mode: every unresolved ref must already be in the cache by content-hash; otherwise exit 4.
3. **DAG build** (`internal/engine/dag`): **NEW PRECONDITION.** Today the engine builds DAG edges only from `pt.RunAfter` (`engine.go:85`); there is no implicit-edge inference from `pt.Params` references to upstream task results. Before resolver work can correctly schedule lazy resolution, the engine must walk *both* `pt.Params` and (after lazy-dispatch lands) `pt.TaskRef.ResolverParams` / `pt.PipelineRef.ResolverParams` for `$(tasks.X.results.Y)` substrings, adding an implicit edge `X → pt.Name` for each match. This matches upstream Tekton semantics (the controller already does this) and is a hard prerequisite for the lazy-resolution dispatcher — without it, a task whose `resolver.params: pathInRepo: $(tasks.discover.results.path)` would be scheduled at level 0, before `discover` ever runs. The plan implements baseline implicit-edge inference for `pt.Params` *first* (as its own task with its own test), then extends the same walker to `resolver.params` once that field exists.
4. **Dispatch time** (`internal/engine/engine.runOne`):
   - Substitute `resolver.params` against the current `resolver.Context` (same context the rest of the task uses).
   - Compute `cacheKey = sha256(resolver-name | sorted(resolved-params))`.
   - Look up the cache. Hit → use cached bytes.
   - Miss → call `refresolver.Resolve(ctx, kind, name, params) ([]byte, error)`. Direct mode dispatches by resolver name; remote mode delegates to the `ResolutionRequest` driver.
   - Decode bytes via `loader.LoadBytes`. Expect exactly one Task (for `taskRef`) or one Pipeline (for `pipelineRef`); reject otherwise.
   - Run the resolved Task/Pipeline through the validator (the same one the user's inline YAML goes through). Failure aborts the task with `status: "failed"` and `message: "resolver: ..."`.
   - Hand the inlined Task to the existing engine code path. Everything downstream — `applyStepTemplate`, `substituteSpec`, backend dispatch — is unchanged.
5. **Cleanup**: cache survives across runs (in `--resolver-cache-dir`).

### Why lazy is mandatory

A real-world example: `resolver.params: [{name: pathInRepo, value: $(tasks.discover.results.path)}]`. A `discover` Task scans the repo and emits a per-component Task path; only after `discover` ran does tkn-act know which `golang-build@v0.4` variant to fetch. Eager resolution can't satisfy this; lazy resolution falls out of the existing DAG dispatch.

### Per-run resolution cache

A small `map[cacheKey]ResolvedRef` lives on the engine for the duration of a run. Repeated references to the same `(resolver, params)` resolve once. The on-disk `--resolver-cache-dir` survives across runs. The cache value is the raw bytes the resolver returned, plus a metadata blob (`resolver`, `params`, `resolved-at`, `etag-or-sha-if-present`).

**Cache-key invariant (load-bearing).** The cache key is computed *after* the per-task `resolver.Context` substitution has been applied. Concretely: `cacheKey = sha256(resolver-name + "\x00" + sortedKVs(SUBSTITUTED-params))`. Two PipelineTasks that reference the same Pipeline / Task via the same resolver but whose `resolver.params` substitute to different upstream task results MUST yield different cache keys. The engine's per-run cache and the on-disk cache (§10) both key on the same hash. This rule is restated in §10 and is exercised by `TestRunOneCachesPerRun` (proves identical substituted params hit) and its sibling `TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams` (proves divergent substituted params miss).

### Resolution boundary in the cluster backend

When `--cluster` is in play, two sub-options exist:

1. **Resolve in tkn-act, submit inlined PipelineRun.** What this spec recommends. Consistent with how the cluster backend already inlines `taskRef.name` references via `inlineTaskSpec`. Local k3d doesn't have hub/git creds and would fail to resolve on its own.
2. **Submit unresolved PipelineRun, let local k3d's Tekton handle it.** Rejected — local k3d is ephemeral and unconfigured; it can't reach private hubs, doesn't know about `--resolver-config`, doesn't share the `--resolver-cache-dir`.

So the resolver layer sits **above** the backend interface. Mode B (remote) is the same in both backends because the inlined bytes flow into the backend identically.

---

## 4. Type additions

```go
// in internal/tektontypes/types.go

// TaskRef.Resolver and ResolverParams carry a Tekton "resolver" block.
// When Resolver is non-empty, Name is ignored (controllers vary; we
// match upstream by treating Resolver as authoritative).
type TaskRef struct {
    Name           string         `json:"name,omitempty"`
    Kind           string         `json:"kind,omitempty"` // Task|ClusterTask; default Task
    Resolver       string         `json:"resolver,omitempty"`
    ResolverParams []ResolverParam `json:"params,omitempty"` // YAML key is "params" *inside* the resolver block; see note
}

type PipelineRef struct {
    Name           string         `json:"name,omitempty"`
    Resolver       string         `json:"resolver,omitempty"`
    ResolverParams []ResolverParam `json:"params,omitempty"`
}

// ResolverParam is the substitution-eligible shape resolvers consume.
// Mirrors Tekton's tekton.dev/v1 resolver param: name + value (string-only
// in v1beta1 of the resolution CRD; the parent task's params are kept on
// PipelineTask.Params and not on the resolver block).
type ResolverParam struct {
    Name  string     `json:"name"`
    Value ParamValue `json:"value"`
}
```

**YAML-key collision note.** Tekton's schema nests `params` *inside* the `resolver` block (`taskRef: {resolver: git, params: [...]}`). The outer `PipelineTask.params` field already exists and is unrelated. The two layers don't collide because they live at different nesting levels; the JSON-tag `"params"` on `ResolverParams` is correct for the inner block.

---

## 5. Loader changes

`internal/loader/loader.go` already round-trips unknown fields; the only addition is to keep the new `Resolver` / `ResolverParams` fields populated in the `tektontypes.TaskRef` / `PipelineRef`. Multi-doc YAML, ConfigMap/Secret loading, and existing apiVersion gates are unchanged.

A new helper `loader.HasUnresolvedRefs(b *Bundle) []UnresolvedRef` returns the list of `(pipelineName, pipelineTaskName, kind, resolverName)` tuples for diagnostics — used by `validate -o json` to surface what `--offline` would need pre-cached.

---

## 6. Resolver interface

There's a name clash: `internal/resolver/` already exists for `$(...)` substitution. To avoid renaming the substitution package (which is referenced from many sites), the new package is named **`internal/refresolver`**. The naming choice is documented in the package doc and in `AGENTS.md` so future readers don't conflate the two.

```go
// Package refresolver fetches Tekton Tasks/Pipelines referenced via
// taskRef.resolver / pipelineRef.resolver. Distinct from internal/resolver,
// which performs $(...) variable substitution.
package refresolver

type Kind int
const (
    KindTask Kind = iota
    KindPipeline
)

type Request struct {
    Kind     Kind
    Resolver string             // git | hub | http | bundles | cluster | (custom in remote mode)
    Params   map[string]string  // already substituted; resolver-specific keys
}

type Resolved struct {
    Bytes      []byte // raw YAML; loader.LoadBytes consumes it
    Source     string // human-readable origin ("git: github.com/foo/bar@abc123 → task/x.yaml")
    SHA256     string // for cache key + invalidation diagnostics
}

type Resolver interface {
    Name() string
    Resolve(ctx context.Context, req Request) (Resolved, error)
}

// Registry routes a Request to one of the configured resolvers, with
// per-Request cache lookup on top.
type Registry struct {
    direct map[string]Resolver // Mode A
    remote *RemoteResolver     // Mode B; non-nil iff --remote-resolver-context set
    cache  *Cache
    offline bool
}

func (r *Registry) Resolve(ctx context.Context, req Request) (Resolved, error)
```

### Per-resolver implementation notes

| Resolver | Direct-mode params | Implementation |
|---|---|---|
| `git` | `url`, `revision` (default `HEAD`), `pathInRepo`, optional `token` | `git clone --depth=1 --branch=<rev>` to a tmpdir under `--resolver-cache-dir/git/<sha256(url+rev)>`; read `pathInRepo`. Reuse on cache hit. Subsequent runs `git fetch + git checkout` if `revision` changed. SSH URLs honored if `ssh-agent` is present (no in-tkn-act key handling). |
| `hub`  | `catalog` (default `tekton`), `kind` (`task`/`pipeline`), `name`, `version`, optional `type` (`artifact`, default) | HTTPS GET to `https://api.hub.tekton.dev/v1/resource/<catalog>/<kind>/<name>/<version>/yaml` (legacy hub) or `https://artifacthub.io/api/v1/packages/<...>` if `type=artifact`. Single retry on 5xx; exit-code 4 on 404 with hint. |
| `http` | `url`, optional `sha256` (verify) | Plain `http.Get`. Bearer-token via `TKN_ACT_HTTP_TOKEN` or `--resolver-config`. `sha256` mismatch → exit 4. |
| `bundles` | `bundle` (image ref), `name` (resource name inside the bundle), `kind` | Pull OCI artifact via `go-containerregistry/pkg/crane`; iterate manifest layers, find a Task/Pipeline whose `metadata.name == name`. Honor `~/.docker/config.json`. |
| `cluster` | `kind` (`task`/`pipeline`), `name`, `namespace` | Use the user's `KUBECONFIG` (or one named via `--cluster-resolver-context`); fetch the named resource via the dynamic client; serialize to YAML. Refuses if `KUBECONFIG` is unset. **Disabled by default** when `--cluster-resolver-context` isn't explicitly passed — `KUBECONFIG` may point at production. |

### Mode B — `ResolutionRequest` driver

```go
// internal/refresolver/remote.go
package refresolver

type RemoteResolver struct {
    dynamic   dynamic.Interface  // points at the user's chosen kubeconfig context
    namespace string             // default: "default"
    timeout   time.Duration      // --remote-resolver-timeout
}

var gvrResolutionRequest = schema.GroupVersionResource{
    Group: "resolution.tekton.dev", Version: "v1beta1", Resource: "resolutionrequests",
}
// Some real-world clusters (older Tekton Resolution installs, OpenShift
// Pipelines on long-term support channels) only expose v1alpha1. The
// remote resolver tries v1beta1 first and, on `NoKindMatchError`, falls
// back to v1alpha1 with a one-shot debug-level log line. Both shapes
// are wire-compatible for the fields we use (`spec.params`,
// `status.conditions`, `status.data`); the only thing that differs is
// the served `apiVersion`.

func (r *RemoteResolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
    rr := &unstructured.Unstructured{Object: map[string]any{
        "apiVersion": "resolution.tekton.dev/v1beta1",
        "kind":       "ResolutionRequest",
        "metadata": map[string]any{
            "generateName": "tkn-act-",
            "namespace":    r.namespace,
            "labels": map[string]any{
                "resolution.tekton.dev/type": req.Resolver,
            },
        },
        "spec": map[string]any{
            "params": resolverParamsToList(req.Params),
        },
    }}
    created, err := r.dynamic.Resource(gvrResolutionRequest).Namespace(r.namespace).Create(ctx, rr, metav1.CreateOptions{})
    if err != nil { return Resolved{}, err }
    defer r.dynamic.Resource(gvrResolutionRequest).Namespace(r.namespace).Delete(context.Background(), created.GetName(), metav1.DeleteOptions{})

    // Watch + poll until status.conditions[Succeeded]=True or timeout.
    // Decode status.data (base64) and return.
}
```

The chosen kubeconfig context's RBAC must include `create / get / watch / delete` on `resolutionrequests`. Diagnostic error if missing.

---

## 7. Engine changes

| Site | Change |
|---|---|
| `internal/engine/engine.go:lookupTaskSpec` | If `pt.TaskRef.Resolver != ""`, route through `refresolver.Registry` instead of looking up in `bundle.Tasks`. Returns the resolved `TaskSpec` plus updates the engine's per-run cache. |
| `internal/engine/engine.go:RunPipeline` | Before DAG build, if `pl.PipelineRef` (top-level run referencing a `Pipeline` via resolver) is set, resolve the Pipeline **eagerly at load time** — see "Top-level pipelineRef resolution timing" below. Substitute the resolved Pipeline as `pl`. (Pipeline-level resolver only happens at the top level — tkn-act doesn't recurse into resolved-Pipelines-that-reference-other-Pipelines in v1.6.) |
| `internal/engine/engine.go:uniqueImages` | Cannot pre-pull images for unresolved refs (the resolver's bytes aren't available yet). Skip those tasks; emit one `error` event per skipped pre-pull with severity `info` (`Kind: EvtError, Message: "skipping image pre-pull for resolver-backed task ..."`). The runtime image pull will fall back to per-step `IfNotPresent` semantics. |
| `internal/engine/engine.go:runOne` | After dispatch-time resolution, run the resolved spec through the existing `validator.ValidateTaskSpec` (lifted out of `validator.Validate` so it can be called per-task). Failure → `status: "failed"`, `message: "resolver: validate: ..."`. |
| `internal/engine/dag/...` | Walk `resolver.params` strings the same way it walks regular task params for `$(tasks.X.results.Y)` references; this creates implicit edges so a `discover → resolved-build` dependency is honored. |

The new event kinds (per below) are emitted from `runOne` around the resolver call.

### Top-level `pipelineRef.resolver` resolution timing

§3 says "all resolution is lazy at dispatch time." That rule applies to `taskRef.resolver` and to nested PipelineRefs *that don't exist yet in v1.6.* The top-level `pipelineRef.resolver` is the one exception: it is resolved **eagerly at load time, before DAG build**, because a top-level Pipeline reference cannot legally substitute `$(tasks.X.results.Y)` (no task has run, and the DAG it would define hasn't been parsed yet). The substitution context for top-level `pipelineRef.params` is the run-input scope only: `$(params.X)`, `$(context.*)`. This is consistent with how Tekton's controller treats top-level `PipelineRun.spec.pipelineRef.resolver` — there's no DAG context to resolve against. `--offline` cache lookup for top-level refs therefore happens at load time, not dispatch time, and a `--offline` cache miss surfaces as an exit-4 validate-style error before any task runs. The `resolver-start` / `resolver-end` events for the top-level resolution carry an empty `task` field (see §12).

### Cluster-backend lazy resolution path

The lazy-resolution machinery described above lives in `runOne`, which is the *docker* code path. The cluster path goes through `runViaPipelineBackend` (`engine.go:51-52, 483`) which serializes the Pipeline directly to a `PipelineRun` and submits it to the local k3d. Local k3d's Tekton controller has no hub credentials, no `--resolver-config`, and no access to the user's `--resolver-cache-dir`; if tkn-act submitted an unresolved `taskRef: {resolver: git, ...}` it would fail to fetch.

Therefore the cluster backend must *also* lazy-resolve before submission. The implementation lives in `runViaPipelineBackend`: walk the Pipeline (tasks ∪ finally), identify resolver-backed `taskRef`s, build the same `resolver.Context` accumulator the docker path uses (top-level params plus, for any task whose `resolver.params` reference upstream results, the bytes from a prior submission round), call `lookupTaskSpecLazy` for each, inline the returned `TaskSpec` directly into the PipelineRun map (mirroring `inlineTaskSpec` in `internal/backend/cluster/run.go:165-172`), and only *then* serialize the final PipelineRun. For pipelines whose `resolver.params` *do* reference upstream task results, the cluster backend submits one PipelineRun per dispatch level — same level-by-level rhythm the docker backend uses — so each subsequent submission's resolver substitution sees results from the prior level. Where every `resolver.params` references run-scope only (no upstream-result deps), the entire Pipeline can be inlined and submitted in one shot.

Both backends thus share `lookupTaskSpecLazy` and the `refresolver.Registry` instance. The result: the docker and cluster backends produce identical inlined `taskSpec` bytes for the same `(resolver, substituted params)` input, and a Pipeline with `taskRef.resolver: git` runs cleanly under `--cluster` because tkn-act inlines before submission.

---

## 8. CLI flags

All on `tkn-act run` (and inherited by `validate`/`list` where they affect parse-time errors):

| Flag | Default | Purpose |
|---|---|---|
| `--resolver-cache-dir <path>` | `$XDG_CACHE_HOME/tkn-act/resolved/` | On-disk cache of resolved bytes |
| `--remote-resolver-context <ctx>` | unset | Opt into Mode B; `<ctx>` is a kubeconfig context name |
| `--remote-resolver-namespace <ns>` | `default` | Namespace used to submit `ResolutionRequest`s |
| `--remote-resolver-timeout <d>` | `60s` | Per-request wait budget |
| `--resolver-config <path>` | unset | YAML/JSON file with per-resolver settings (auth tokens, mirror URLs, `cluster` context name); see schema below |
| `--offline` | `false` | Reject any cache miss; reject any resolver call. Useful for hermetic CI |
| `--resolver-allow git,hub,http,bundles` | the listed four | Allow-list of resolvers `tkn-act` will dispatch (security; see §11) |

`--resolver-config` schema (YAML, all sections optional):

```yaml
hub:
  baseURL: https://hub.example.com
  token:   ${HUB_TOKEN}        # ${...} interpolated from environment
http:
  bearerTokenEnv: TKN_ACT_HTTP_TOKEN
  ca: /path/to/ca.pem          # for self-signed corp endpoints
git:
  sshKnownHostsFile: /etc/ssh/ssh_known_hosts
bundles:
  dockerConfig: ~/.docker/config.json
cluster:
  context: prod-tekton          # which kubeconfig context cluster-mode reads from
```

---

## 9. Failure semantics & exit codes

| Where | Behavior | Exit code |
|---|---|---|
| Resolver failure during a run (Mode A or B) | The owning task ends with `status: "failed"`, `message: "resolver: <human-readable>"`. Downstream tasks become `not-run` per existing rules. | 5 (pipeline failure) — no new code |
| Resolver failure at validate time (`validate` command, or Phase 1 pre-flight) | Standard validate error. | 4 |
| `--offline` and a cache miss | Validate-style error before the run starts: `error: refresolver: cache miss for git@... (--offline set)`. | 4 |
| Mode B and the kubeconfig context can't be reached | Fails at `Prepare` time. Treated as an environment error. | 3 |
| Resolver returns YAML that fails our validator | Same as if the user pasted bad YAML inline: per-task `status: "failed"`, `message: "resolver: validate: <field>: <reason>"`. | 5 |

A new exit code for "resolver failure" was considered and rejected — the user-visible meaning is identical to "your task setup is wrong / your task failed," and the existing 4 / 5 split already captures the validate-vs-runtime distinction.

---

## 10. Caching

### Layout

```
$XDG_CACHE_HOME/tkn-act/resolved/
├── git/
│   └── <sha256(url+rev)>/
│       ├── meta.json    # {url, revision, fetched-at, head-sha}
│       └── repo/        # bare-cloned working tree
├── hub/
│   └── <sha256(catalog+kind+name+version)>.yaml
├── http/
│   └── <sha256(url)>.yaml
├── bundles/
│   └── <sha256(bundle+name)>.yaml
└── cluster/
    └── <sha256(context+ns+kind+name)>.yaml
```

### Invalidation

- Manual: `tkn-act cache prune --resolver` (new subcommand) wipes the directory.
- Per-run: `tkn-act run --resolver-cache-mode=bypass` reads but doesn't write the cache; `--resolver-cache-mode=refresh` ignores cached entries and re-fetches.
- Time-based: none in v1.6. A `git@main` entry can serve stale bytes indefinitely; document this loudly so users pin SHAs.

### Cache key

`sha256(resolver-name + "\x00" + sortedKVs(SUBSTITUTED-params))` — see the "Cache-key invariant" callout in §3. The hash is computed on `params` *after* the per-task `resolver.Context` substitution has been applied; two tasks resolving the same upstream Pipeline / Task with `resolver.params` that substitute to different values yield different keys and miss the cache independently. Resolver-specific keys (the second half) are documented per resolver above. The `meta.json` file alongside cached bytes records both the substituted key and the pre-substitution param shape for human debugging.

---

## 11. Security

The resolver layer can fetch arbitrary network bytes and execute the resulting YAML against the user's local Docker daemon (or local k3d). That's a meaningful attack surface even without `eval`-style code injection, since a malicious Task can `image: alpine && command: [rm, -rf, /workspace]` and trash whatever workspaces are mounted.

Decisions:

- **Default allow-list:** `git, hub, http, bundles`. `cluster` is **off by default** because it requires the user's `KUBECONFIG` to point at something real. Override via `--resolver-allow=...` (CSV).
- **`--offline` is safe by default:** useful in CI to guarantee no network egress unexpectedly happens.
- **No tkn-act-managed credential store.** Every credential comes from the user's environment (`KUBECONFIG`, `~/.docker/config.json`, `TKN_ACT_HTTP_TOKEN`, `--resolver-config`) so we never hold secrets.
- **No automatic execution sandbox.** If the user runs a Task they didn't author, it gets the same Docker / k3d access any other tkn-act run gets. Document this in `AGENTS.md` and the `run` `--help`.
- **Mode B uses the user's existing kubectl identity.** The `ResolutionRequest` runs under whatever service-account that kubeconfig context resolves to. tkn-act never elevates privileges.
- **`--resolver-config` paths are validated for symlink escapes** — the config file must be a regular file the calling user owns.
- **The HTTP resolver requires `https://` by default;** `--resolver-allow-insecure-http` opts into `http://` (a CI-only flag). The `--resolver-` prefix matches the rest of the resolver flag family.

---

## 12. JSON event additions

Two new event kinds, both emitted by the engine from `runOne` around the lazy resolution call. They appear *between* `task-start` and the first `step-start` for resolver-backed tasks; for inline tasks they don't fire. For the **top-level `pipelineRef.resolver`** resolution (which fires once at load time, before any task starts — see §7), the same two event kinds are emitted but with `task: ""` (empty); JSON consumers should treat the empty `task` field as "this is the top-level pipeline-ref resolution."

All four new fields on `Event` (`resolver`, `cached`, `sha256`, `source`) MUST be declared with `,omitempty` JSON tags in `internal/reporter/event.go`, matching the existing convention every other optional field on `Event` already uses (`runId`, `pipeline`, `task`, `step`, `status`, `exitCode`, `durationMs`, `message`, `attempt`, `results`). Plan Phase 1 Task 6 verifies this and the test asserts that an `Event` with these fields zero-valued does not serialize the keys.

```jsonc
{"kind":"resolver-start","time":"...","task":"build","resolver":"git",
 "params":{"url":"https://github.com/...","revision":"main","pathInRepo":"task/golang-build/0.4/golang-build.yaml"}}

{"kind":"resolver-end","time":"...","task":"build","resolver":"git",
 "status":"succeeded","duration":"1.234s","sha256":"abc123...","cached":false,
 "source":"git: github.com/tektoncd/catalog@abc123 → task/golang-build/0.4/golang-build.yaml"}
```

On failure:

```jsonc
{"kind":"resolver-end","time":"...","task":"build","resolver":"git",
 "status":"failed","duration":"4.567s","message":"clone failed: ..."}
```

These are additive — agents that don't recognize the new kinds can ignore them. Existing `task-start` / `task-end` event shapes are unchanged. The `task-end` for a task whose resolver failed still carries `status: "failed"` and `message: "resolver: ..."`, so legacy parsers that only watch `task-end` see the same outcome they always did.

---

## 13. Pretty output

For resolver-backed tasks, pretty output adds one indented line under the task name:

```
build
  ↳ resolver git github.com/tektoncd/catalog@abc123 → task/golang-build/0.4/golang-build.yaml (1.2s)
  step build/build:    ...
```

Cached resolutions append `(cached)` instead of a duration. On `-q`, the resolver line is suppressed.

**Windows console fallback.** The `↳` and `→` glyphs are non-ASCII; on consoles that can't render them (legacy Windows code pages, plain pipes when the printer detects a non-UTF-8 sink) the pretty printer falls back to ASCII: `↳` → `->` and `→` → `->`. The decision uses the same TTY/encoding probe the existing color-detection logic uses (per `AGENTS.md` "Pretty output" — `NO_COLOR` / `--color=never` and friends). Pretty output is for humans and may change at any time; agents must pass `-o json` and observe the structured `resolver-end.source` field instead.

---

## 14. Cluster backend integration

Recap: resolution happens *above* the backend. By the time the cluster backend's `RunPipeline` is called, every `taskRef.resolver` has been replaced with `taskSpec: {...}` inlined bytes. The existing `inlineTaskSpec` path serializes them to the PipelineRun unchanged.

One subtlety: `pipelineRef.resolver` (Pipeline-level) is resolved *before* the engine's `RunPipeline` returns the inlined Pipeline; the cluster backend then sees a Pipeline with no top-level `pipelineRef`, so it submits the inlined `pipelineSpec` exactly as today.

The only cluster-specific change is: when `--remote-resolver-context` happens to point at the same cluster `--cluster` is using (rare but possible), tkn-act still resolves locally and submits inlined bytes — we don't try to short-circuit by leaving the `taskRef.resolver` unresolved and letting Tekton in the local k3d resolve it. This keeps the docker/cluster behavior identical.

---

## 15. Test plan

### Unit tests

| Subject | What it asserts |
|---|---|
| `internal/refresolver/registry_test.go` | Registry dispatches by resolver name; cache hit short-circuits; unknown resolver → typed error; allow-list rejects denied resolvers |
| `internal/refresolver/git_test.go` | Uses a local bare-repo fixture; happy path; missing pathInRepo; revision mismatch; SSH URL form parses |
| `internal/refresolver/hub_test.go` | Mocks the hub HTTP API with `httptest.NewServer`; happy path; 404 with helpful hint; 5xx triggers single retry |
| `internal/refresolver/http_test.go` | `httptest.NewServer`; sha256 verify pass + fail; bearer token sent; rejects `http://` without `--allow-insecure-http` |
| `internal/refresolver/bundles_test.go` | Uses `crane.Append` to build an OCI bundle in-memory; happy path; missing resource-by-name |
| `internal/refresolver/cluster_test.go` | Uses fake dynamic client; rejects when KUBECONFIG unset and no `--cluster-resolver-context`; happy path |
| `internal/refresolver/remote_test.go` | Fake dynamic client emits a `ResolutionRequest` with `status.conditions=Succeeded` and `status.data=<base64>`; happy path; timeout; `Failed` condition surfaces message; `TestRemoteResolverDeletesAfterSuccess`; `TestRemoteResolverDeletesAfterFailure`; **`TestRemoteResolverDeletesOnContextCancel`** (SIGINT mid-resolution still triggers Delete via `context.Background()` — proves the cleanup race is closed) |
| `internal/refresolver/cache_test.go` | Disk layout; `--offline` rejects miss; `--resolver-cache-mode=refresh` re-fetches |
| `internal/engine/lazy_resolve_test.go` | DAG implicit edge created when `resolver.params` references `$(tasks.X.results.Y)`; resolver fires after upstream task; per-run cache reuses bytes; **`TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams`** (two tasks resolve same Pipeline ref via same resolver, but `resolver.params` substitute to different upstream-result-derived values; assert resolver is called *twice*); **`TestRunOneRejectsResolvedTaskWithBadSpec`** (resolver returns syntactically valid YAML but a Task with no steps; assert `task-end.status=failed` with a clear validator error in `message`); **`TestRunOneFinallyTaskWithResolverRef`** (a `finally` task with `taskRef.resolver: <stub>` resolves and runs after the main DAG completes, even if the main DAG failed) |
| `internal/engine/dag/dag_test.go` (or `internal/engine/engine_dag_test.go`) | **`TestImplicitEdgeFromParamResultRef`** (a PipelineTask whose `params` contains `$(tasks.checkout.results.commit)` is dispatched AFTER `checkout` even with no explicit `runAfter`); follow-up **`TestImplicitEdgeFromResolverParamResultRef`** (same assertion when the result reference appears in `resolver.params` instead of `params`) |

### e2e fixtures (cross-backend, via `internal/e2e/fixtures.All()`)

| Fixture | Resolver | Mocking strategy |
|---|---|---|
| `resolver-inline-fallback` | none — sanity | A baseline that the resolver work didn't break inline taskRefs |
| `resolver-git` | git | Local bare repo under `testdata/e2e/resolver-git/repo.git/`; `url: file://...` so no network |
| `resolver-http` | http | `httptest.NewServer` started by an `e2e` harness hook; URL injected via `-p` and a top-level pipeline param plumbing it into the Task |
| `resolver-bundles` | bundles | OCI bundle baked once into `testdata/e2e/resolver-bundles/bundle.tar` and loaded via in-memory registry |
| `resolver-lazy` | git | Two-task pipeline where `discover` outputs a `pathInRepo` consumed by the `build` task's `resolver.params.pathInRepo` |
| `resolver-remote` | remote (Mode B) | Cluster mode only; uses the `--cluster` k3d itself as the `--remote-resolver-context` so we exercise the real `ResolutionRequest` CRD without needing a second cluster |

Each fixture lands in `testdata/e2e/<name>/` and is registered in `internal/e2e/fixtures.All()` so docker + cluster harnesses both run it. The `resolver-remote` fixture is cluster-only (build-tag `cluster`); a stub entry in the docker harness records `WantSkip=true` with a reason.

### Limitations fixtures retired

`testdata/limitations/` does not currently have a `resolvers/` directory (the row simply has `none` in the limitations column). When this work ships, the `feature-parity` row flips from `gap` → `shipped` with no limitation deletion needed. If during implementation we discover a partial-shipping scenario (e.g., `cluster` resolver direct-mode is harder than expected), we add a `testdata/limitations/resolver-cluster/` and reference it from a new sub-row.

### Fuzz / property-based

Skipped for v1.6. The cache-key derivation is the only spot that warrants property tests later (collision resistance under unusual param shapes); tracked as a follow-up issue.

---

## 16. Docs updates required

When this lands:

- `AGENTS.md` — new section "Resolvers" covering the dual-mode design, lazy resolution, the new flags, the new event kinds, and the security stance.
- `cmd/tkn-act/agentguide_data.md` — mirror of the AGENTS.md section (kept in sync via `go generate`).
- `README.md` — single bullet under "Tekton features supported" + the `--remote-resolver-context` example.
- `docs/feature-parity.md` — flip the `Resolvers — git / hub / cluster / bundles` row from `gap` → `shipped`. Either one row per resolver or one row per resolver-mode pair (TBD; the simplest is to keep one row and list the resolvers in a parenthetical).
- `docs/short-term-goals.md` — mark Track 1 #9 done.
- `docs/test-coverage.md` — add the fixtures + the `internal/refresolver` package row.
- `docs/superpowers/specs/2026-05-04-resolvers-design.md` — this file (cross-link to the plan).
- `docs/superpowers/plans/2026-05-04-resolvers.md` — plan companion.

---

## 17. Open questions for human review

1. **Custom resolvers in remote mode — should we let users configure arbitrary resolver names, or hard-code the upstream-known set?** **RESOLVED.** Any resolver name is valid when `--remote-resolver-context` is set; the validator's `RemoteResolverEnabled` Option short-circuits the "is this resolver name in the direct-mode allow-list?" check. In direct mode, only the in-scope four-or-five names dispatch (the validator rejects unknown names with exit 4). This decision is locked before Phase 5 starts and the validator-side wiring lands in Phase 1 (so the validator's behavior is correct as soon as remote-mode flags appear, even though the dispatcher itself doesn't ship until Phase 5).
2. **Should `cluster` direct-mode default to off?** The risk surface is real (`KUBECONFIG` may point at production). Current spec says yes, off-by-default; users opt in via `--resolver-allow=...,cluster` or `--resolver-config` `cluster.context` setting. Confirm.
3. **`pipelineRef.resolver` at the top level — is this commonly used in catalogs?** If rare, we could ship resolver support for `taskRef` only in v1.6 and defer `pipelineRef`. Current spec includes both because the lazy-dispatch machinery is the hard part and it works at either level.
4. **Should `resolver-start` / `resolver-end` events be elevated to first-class lifecycle events (always emitted) or kept opt-in (`--emit-resolver-events`)?** Currently always-on; a hostile catalog YAML could spam them via cache misses, but the count is bounded by task count.
5. **Should the per-run resolution cache be exposed via `tkn-act doctor -o json` output?** `--offline` behavior depends on cache contents; surfacing what's cached helps users debug "why did `--offline` fail." Tentative: yes, as a `cache.resolved.entries` count plus paths, only when `--debug` is set.
6. **Should we ship a `tkn-act resolve` subcommand** that does just the resolution step (writes inlined YAML to stdout), so users can `tkn-act resolve -f pipeline.yaml | kubectl apply -f -`? Useful but increases surface area. Tentative: defer to v1.7.
7. **Phase boundary for `bundles`** — the OCI registry client (`go-containerregistry`) is a heavy dependency. We should confirm the binary-size delta (target: stay under +10MB) before locking it in; if too large, defer `bundles` to a separate PR.
8. **Mode B authentication for hub-style resolvers** — when remote mode is used, should we still consult `--resolver-config` (e.g., to forward a hub token), or is the cluster expected to have its own auth? Tentative: ignore `--resolver-config` in Mode B — the controller's secrets win.
