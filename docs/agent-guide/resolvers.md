## Resolvers (Track 1 #9, shipped)

`taskRef.resolver` and `pipelineRef.resolver` (the Tekton catalog-consumption
pattern) are **fully supported** in v1.6. All six phases of the plan have
shipped: Phase 1 (scaffolding), Phase 2 (direct `git`), Phase 3 (`hub` +
`http`), Phase 4 (`bundles` + off-by-default `cluster`), Phase 5 (Mode B
remote resolver via `ResolutionRequest` CRD), and Phase 6 (`--offline`
end-to-end + on-disk cache + `tkn-act cache` subcommands):

- New types `TaskRef.Resolver` / `TaskRef.ResolverParams` and the
  `PipelineRef` counterparts. Existing inline-only YAML is unaffected
  (the new fields are `omitempty`).
- A new `internal/refresolver` package distinct from `internal/resolver`
  (which performs `$(...)` variable substitution). The two senses of
  "resolver" are intentionally separated.
- **Lazy resolution at task-dispatch time**: `resolver.params` may
  reference `$(tasks.X.results.Y)` and the engine schedules X before
  the resolver-backed task. Implicit DAG edges are now inferred from
  every `$(tasks.X.results.Y)` substring in `pt.Params` AND
  `pt.TaskRef.ResolverParams` (a baseline behavior change — see the
  PR description for one observable effect on previously-broken
  Pipelines).
- **Eager top-level resolution** for `pipelineRef.resolver` on a
  PipelineRun: resolved synchronously at load time before any DAG
  build, since a top-level ref cannot legally substitute upstream
  task results.
- **Cluster backend inlines** resolver-backed taskRefs into the
  submitted PipelineRun before sending it to the local k3d (which
  has no resolver credentials of its own).
- **Two new event kinds**: `resolver-start` and `resolver-end`,
  carrying optional `resolver`, `cached`, `sha256`, `source` fields,
  all `omitempty` so non-resolver events ignore them. The top-level
  pipelineRef resolution emits these two events with an empty `task`
  field — JSON consumers disambiguate via the absence of `task`.
- **`git` resolver (Phase 2, supported in v1.6)**: shallow clones a
  repo via `go-git/v5` and reads `pathInRepo` at the requested
  revision. Direct-mode params are:
  | Param | Required | Default | Notes |
  |---|---|---|---|
  | `url` | yes | — | `file://`, `https://`, `ssh://`, or `git@host:path`. Plain `http://` refused unless `--resolver-allow-insecure-http`. |
  | `revision` | no | `main` | Branch, tag, or full SHA. SHAs trigger a full clone + ResolveRevision fallback path. |
  | `pathInRepo` | yes | — | Relative path to the YAML inside the repo. `..` traversal refused. |
  Cache layout: `<--resolver-cache-dir>/git/<sha256(url+revision)>/repo/`.
  A second call with identical `(url, revision)` reuses the cached
  working tree without any network IO and emits `resolver-end` with
  `cached: true`. SSH delegation honors `ssh-agent`; tkn-act never
  reads SSH keys directly.
- **CLI flags**: `--resolver-cache-dir`,
  `--resolver-allow=git,hub,http,bundles` (default — `cluster` is
  intentionally absent and must be opted in explicitly),
  `--resolver-config`, `--offline`, `--remote-resolver-context`,
  `--resolver-allow-insecure-http` (now also opens HTTP for the
  bundles resolver against non-loopback registries),
  **`--cluster-resolver-context=<ctx>`** (Phase 4) opts the
  off-by-default `cluster` resolver in by naming the kubeconfig
  context to read from, and **`--cluster-resolver-kubeconfig=<path>`**
  overrides the kubeconfig path. Setting either flag implicitly flips
  the registry's `AllowCluster=true` so the cluster resolver registers
  in the default registry. **`--remote-resolver-context=<ctx>`**
  (Phase 5) flips the registry into Mode B; pair with
  `--remote-resolver-namespace=<ns>` (default `default`) and
  `--remote-resolver-timeout=<duration>` (default `60s`) to control
  where the `ResolutionRequest` lands and how long tkn-act waits for
  the controller to reconcile.
- **Validator** rejects unknown resolver names in direct mode (unless
  `--remote-resolver-context` is set, which short-circuits the
  allow-list); rejects `resolver.params` that reference a task not in
  `spec.tasks ∪ spec.finally`; rejects any cache-miss when `--offline`
  is set.

### Direct resolvers — what's wired

| Resolver | Status | `resolver.params` | Notes |
|---|---|---|---|
| `inline` | shipped (test harness) | `name` | Magic test resolver. The test harness preloads `(name → bytes)` pairs and the engine looks them up. |
| `git` | shipped (Phase 2) | `url` (req), `revision` (default `main`), `pathInRepo` (req) | Shallow clone via `go-git/v5`. Cache layout: `<--resolver-cache-dir>/git/<sha256(url+revision)>/repo/`. SSH delegation honors `ssh-agent`. |
| `hub` | shipped (Phase 3) | `name` (req), `version` (default `latest`), `kind` (default `task`), `catalog` (default `tekton`) | HTTPS GET to `<BaseURL>/v1/resource/<catalog>/<kind>/<name>/<version>/yaml`. Default BaseURL `https://api.hub.tekton.dev`. HTTPS-only (no opt-out for hub). 5xx retries once. Bearer token via `HubOptions.Token` library API; the standard `--resolver-config` file is read for `hub.token`. |
| `http` | shipped (Phase 3) | `url` (req) | Plain HTTPS GET. 5xx retries once. HTTPS-only by default; `--resolver-allow-insecure-http` opts plain http:// non-loopback URLs in (loopback always allowed for unit tests with `httptest.NewServer`). Bearer token via `HTTPOptions.Token` (library) or env `TKNACT_HTTP_RESOLVER_TOKEN` (CLI escape hatch). |
| `bundles` | shipped (Phase 4) | `bundle` (req — OCI ref like `gcr.io/foo/bar:v1`), `name` (req — resource `metadata.name` to extract), `kind` (default `task`) | Pulls a Tekton OCI bundle via `go-containerregistry`. Walks layers in declaration order, matches on the conventional `dev.tekton.image.{name,kind,apiVersion}` annotations, and returns the YAML embedded in the matching layer's tar. HTTPS-only by default; loopback registries always permit HTTP (so unit tests with `pkg/registry` work); `--resolver-allow-insecure-http` extends that to non-loopback registries. Auth honors `~/.docker/config.json` via `authn.DefaultKeychain`. |
| `cluster` | shipped (Phase 4) — **OFF BY DEFAULT** | `name` (req), `kind` (default `task`, also accepts `pipeline`), `namespace` (default `default`) | Reads from the user's KUBECONFIG via the kube dynamic client. Strips server-side bookkeeping (`status`, `metadata.uid`/`resourceVersion`/`generation`/`creationTimestamp`/`managedFields`) before serializing back to YAML. **Off by default in `NewDefaultRegistry`** — `KUBECONFIG` may point at production. Opt-in either by adding `cluster` to `--resolver-allow` or by setting `--cluster-resolver-context=<ctx>` (which also names the kubeconfig context). Both require explicit user consent before the resolver registers. |

Failure surfaces as `task-end` `status: "failed"` with `message: "resolver: hub: ..."` / `"resolver: http: ..."` / `"resolver: bundles: ..."` / `"resolver: cluster: ..."`, which routes through the standard pipeline-failure exit code (5).

### Mode B — remote resolver via `ResolutionRequest` (Phase 5, shipped)

Setting `--remote-resolver-context=<kubeconfig-context>` flips the
registry into **Mode B**: every `taskRef.resolver:` /
`pipelineRef.resolver:` block is dispatched by submitting a
`resolution.tekton.dev/v1beta1` `ResolutionRequest` CRD to the
remote Tekton cluster, watching `status.conditions[Succeeded]`, and
decoding `status.data` (base64) once the controller fills it in. The
direct-mode allow-list is short-circuited — any resolver name the
remote cluster's controller knows is dispatchable, including
arbitrary custom resolver names.

Wire-compat:
- v1beta1 first; falls back to **v1alpha1** on `NoKindMatchError`
  for older Tekton Resolution installs (long-term-support OpenShift
  Pipelines, etc.). Both API versions share the fields tkn-act
  reads (`spec.params`, `status.conditions`, `status.data`).

Cleanup discipline:
- The `ResolutionRequest` is **always Deleted** on the way out
  (success, controller-reported failure, timeout, **or
  `context.Cancel`** — the deferred Delete uses `context.Background()`
  so SIGINT mid-resolution still triggers cleanup).

Security stance:
- Mode B uses **the user's kubeconfig identity** — whatever
  service-account that context resolves to is the one the remote
  cluster sees. tkn-act never elevates privileges, never stores
  credentials of its own, and never modifies the kubeconfig file.
- The chosen kubeconfig context's RBAC must include
  `create / get / delete` on `resolutionrequests` in the namespace
  named by `--remote-resolver-namespace`.

Flags (all on `tkn-act run`):

| Flag | Default | Purpose |
|---|---|---|
| `--remote-resolver-context <ctx>` | unset | Kubeconfig context to dispatch ResolutionRequests through. Setting non-empty flips registry into Mode B. |
| `--remote-resolver-namespace <ns>` | `default` | Namespace where ResolutionRequests are submitted. |
| `--remote-resolver-timeout <duration>` | `60s` | Per-request wait budget for the controller's reconcile. Failure surfaces as `task-end` `status: "failed"` with a `remote: timeout after ...` message. |

### `--offline` mode + on-disk cache (Phase 6, shipped)

Every direct resolver writes resolved bytes + a small JSON metadata
sidecar to `--resolver-cache-dir` (default
`$XDG_CACHE_HOME/tkn-act/resolved/`) under
`<root>/<resolver>/<sha256(resolver+sortedKVs(SUBSTITUTED-params))>.{yaml,json}`.

Cross-run hits (a Pipeline that ran before, with the same
substituted resolver-params) skip the network entirely on the
second run, surface as `cached: true` on the `resolver-end` JSON
event, and as `(cached)` instead of a duration in pretty output.
The same DiskCache is used by both backends because resolution
happens above the backend layer.

`--offline` rejects every cache miss with a clear error:

- At validate time, the validator's `CacheCheck` callback queries
  the same DiskCache. A cache miss surfaces as exit code 4 before
  any task starts.
- At run time (e.g. for the eager top-level `pipelineRef.resolver`
  path that bypasses the validator), the registry's `Resolve`
  re-checks the cache and emits a `resolver-end` `status: failed`
  with a "cache miss while --offline is set" message; the parent
  task ends `failed` (exit code 5).

Use `--offline` in CI to guarantee no network egress unexpectedly
happens after the cache has been seeded.

### Cache management — `tkn-act cache <list|prune|clear>`

```sh
# List every cached entry (resolver, key, size, age).
tkn-act cache list
tkn-act cache list -o json    # stable JSON shape: {root, entries: [{resolver, key, path, size, mod_time}]}

# Delete entries older than a duration; default 30 days.
tkn-act cache prune                        # --older-than 720h
tkn-act cache prune --older-than 168h      # 7 days
tkn-act cache prune -o json                # JSON: {root, older_than, pruned}

# Wipe everything; -y required to confirm.
tkn-act cache clear -y
tkn-act cache clear -y -o json             # JSON: {root, cleared}
```

All three subcommands honor `--resolver-cache-dir` (the same flag
`tkn-act run` uses). The cache root may not exist yet — `cache
list` returns zero entries; `prune` and `clear` are no-ops.

See `docs/superpowers/plans/2026-05-04-resolvers.md` for the full plan
and `docs/superpowers/specs/2026-05-04-resolvers-design.md` for the spec.
