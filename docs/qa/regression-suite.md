# tkn-act regression suite

Numbered, reproducible, black-box specs. Each is a contract: given the
**Pre** state, running **Cmd** must produce **Expect**. Specs are the durable
counterpart to [`exploratory-test-plan.md`](exploratory-test-plan.md) — run
them on every release to confirm nothing regressed.

Conventions:

- IDs are `AREA-NNN`, stable forever. Never renumber; retire with a
  `~~strikethrough~~` + reason if a spec becomes obsolete.
- **Exit** is the process exit code; it is part of the public contract
  ([`../agent-guide/README.md`](../agent-guide/README.md) §Exit codes).
- Tags: `DOCKER` needs a daemon · `CLUSTER` needs k3d+kubectl · `NET` needs
  egress · `SIG` sends signals · untagged = runs offline & daemon-free.
- `$B` = the binary under test (`bin/tkn-act`). All `run`/`logs`/`runs` specs
  assume a per-pass scratch `TKN_ACT_STATE_DIR` and `XDG_CACHE_HOME` (see
  README). Fixtures referenced as `testdata/e2e/<name>` already exist in-tree;
  inline YAML is given where a spec needs a fixture that doesn't.

Result columns to fill per pass in `findings-log.md`: `PASS` / `FAIL` /
`BLOCKED` (+ issue id on FAIL).

---

## A — CLI surface & introspection

| ID | Cmd | Expect |
|---|---|---|
| CLI-001 | `$B version -o json` | Exit 0; JSON `{"name":"tkn-act","version":"<v>"}` and nothing else on stdout. |
| CLI-002 | `$B help-json` | Exit 0; valid JSON; `.exit_codes[].code` is exactly the set {0,1,2,3,4,5,6,130}; `.commands[].path` includes run, validate, list, doctor, cluster, cache, logs, runs, version, agent-guide, help-json. |
| CLI-003 | `$B help-json \| jq -e '.commands[] \| select(.path=="tkn-act run") \| .flags[].name'` | Lists at least file, param, pipeline, workspace, dir — i.e. every `run --help` local flag is mirrored. |
| CLI-004 | `$B --frobnicate` | Exit 2 (usage); stderr names the unknown flag; no panic/stacktrace. |
| CLI-005 | `$B nonexistent-subcommand` | Exit 2; stderr `unknown command`; no panic. |
| CLI-006 | `$B run --param` (missing value) | Exit 2; message indicates a value is required. |
| CLI-007 | `$B agent-guide --list` | Exit 0; lists section names that 1:1 match `docs/agent-guide/*.md` basenames. |
| CLI-008 | `$B agent-guide --section resolvers` | Exit 0; prints the resolvers section only. |
| CLI-009 | `$B agent-guide --section does-not-exist` | Non-zero exit; message lists/points to valid sections; no panic. |
| CLI-010 | `$B version --output garbage` | Defined behaviour: either exit 2 (unknown format) **or** documented fallback — pin the observed behaviour; must not panic or emit half-JSON. |
| CLI-011 | `$B --color=always --no-color run -f testdata/e2e/hello/pipeline.yaml` `DOCKER` | `--no-color` wins per precedence: output has **no** ANSI escapes. |
| CLI-012 | `$B agent-guide \| head -1` | Exit 0 (or 0/141 by shell) with no Go panic / `broken pipe` stacktrace on stderr. |

## B — doctor

| ID | Cmd | Expect |
|---|---|---|
| DOC-001 | `$B doctor -o json` `DOCKER` | Exit 0; `.ok==true`; shape `{ok,version,os,arch,cache_dir,checks[]}`; each check has `name,ok,detail,required_for`. |
| DOC-002 | `DOCKER_HOST=tcp://127.0.0.1:1 $B doctor -o json` | Exit 3; `.ok==false`; the `docker` check `ok==false`; `required_for=="default"`. |
| DOC-003 | `PATH=/usr/bin $B doctor -o json` (k3d/kubectl absent) `DOCKER` | Exit 0; `.ok==true` (cluster checks are optional); `k3d`/`kubectl` checks `ok==false` with `required_for=="cluster"`. |
| DOC-004 | `$B doctor` (pretty) `DOCKER` | Exit 0; human-readable lines consistent with the JSON (no check silently dropped). |

## C — validate (exit 4 on rejection)

Inline fixtures are minimal; paste into a scratch file `bad.yaml` per spec.

| ID | Fixture / Cmd | Expect |
|---|---|---|
| VAL-001 | `$B validate -f testdata/e2e/hello/pipeline.yaml -o json` | Exit 0; `{"ok":true,...,"errors":[]}`. |
| VAL-002 | apiVersion `tekton.dev/v99` | Exit 4; `errors[]` non-empty; never exit 1/5/panic. |
| VAL-003 | Pipeline with two tasks both named `build` | Exit 4; error mentions duplicate. |
| VAL-004 | `runAfter: [ghost]` (no such task) | Exit 4; error names `ghost`. |
| VAL-005 | Cycle A `runAfter` B, B `runAfter` A | Exit 4; error mentions cycle. |
| VAL-006 | `when` operator `equals` (invalid) | Exit 4; error names the bad operator. |
| VAL-007 | `$(tasks.nope.results.x)` reference | Exit 4. |
| VAL-008 | `retries: -1` | Exit 4. |
| VAL-009 | `timeout: banana` | Exit 4. |
| VAL-010 | `onError: retry` (invalid; only continue/stopAndFail) | Exit 4. |
| VAL-011 | `matrix.include` row overlapping a cross-product param (see `testdata/limitations/matrix-include-overlap`) | Exit 4 (documented limitation). |
| VAL-012 | `-f /no/such/file.yaml` | Pin: usage (2) vs validate (4). Must be deterministic, documented, no panic. |
| VAL-013 | `-f` an empty file | Exit 4 (parse). |
| VAL-014 | `-f README.md` (valid text, not Tekton) | Exit 4; not a crash. |
| VAL-015 | `printf '{}' \| $B validate -f -` | Pin stdin behaviour; deterministic exit (4 expected). |
| VAL-016 | Multi-doc stream where doc 2 is malformed | Exit 4; identifies the offending doc if possible. |
| VAL-017 | For every VAL-00x rejecting fixture: `$B run -f <same>` `DOCKER` | Same exit 4, **before** any container starts (no daemon side-effects). |

## D — list & discovery

| ID | Cmd | Expect |
|---|---|---|
| LST-001 | `cd testdata/e2e/hello && $B list -o json` | Exit 0; `{"pipelines":[...],"tasks":[...]}`; the fixture's pipeline appears. |
| LST-002 | `$B list -o json` in an empty dir | Exit 0; `{"pipelines":[],"tasks":[]}` (pin: empty-but-success vs exit 2). |
| LST-003 | dir containing only a `kind: ConfigMap` YAML | ConfigMap **not** listed as a pipeline/task (no auto-discovery for CM/Secret). |
| LST-004 | dir with both `pipeline.yaml` and `.tekton/` | Both sources' contents surface; no crash. |
| LST-005 | `$B list -C /no/such/dir` | Deterministic non-panic exit (pin 2 vs 0-empty). |
| LST-006 | dir with an invalid `pipeline.yaml` | Pin: partial list vs exit 4; no panic. |

## E — run engine semantics `DOCKER`

| ID | Cmd | Expect |
|---|---|---|
| RUN-001 | `$B run -f testdata/e2e/hello/pipeline.yaml -o json` | Exit 0; first event `run-start`, last `run-end` with success status; no event after `run-end`. |
| RUN-002 | Event grammar check on RUN-001 stream | Every `task-start` has a matching `task-end`; `step-start`/`step-end` balanced; kinds ⊆ documented set. |
| RUN-003 | `$B run -f testdata/e2e/failure-propagation/pipeline.yaml -o json` | Exit 5; a `task-end` with `status:"failed"`; downstream task `not-run`/`skipped`. |
| RUN-004 | `$B run -f testdata/e2e/timeout/pipeline.yaml -o json` | Exit 6; a `task-end` with `status:"timeout"`. |
| RUN-005 | `$B run -f testdata/e2e/retries/pipeline.yaml -o json` | Exit 0; number of `task-retry` events == declared retries before success; terminal `task-end` carries `attempt:N`. |
| RUN-006 | `$B run -f testdata/e2e/onerror/pipeline.yaml -o json` | Exit 0; `onError: continue` step's non-zero exit does not fail the task. |
| RUN-007 | `$B run -f testdata/e2e/when-and-finally/pipeline.yaml -o json` (dev path) | A `task-skip` event for the gated task; `finally` task runs. |
| RUN-008 | `$B run -f testdata/e2e/params-and-results/pipeline.yaml -o json` | Exit 0; `$(tasks.X.results.Y)` value visible downstream. |
| RUN-009 | `$B run -f testdata/e2e/workspaces/pipeline.yaml -w <decl>=<tmp> -o json` | Exit 0; file written by task A read by task B. |
| RUN-010 | `$B run -f testdata/e2e/pipeline-timeout/pipeline.yaml -o json` | Exit 6 (run-level pipeline timeout). |
| RUN-011 | `$B run -f testdata/e2e/tasks-timeout/pipeline.yaml -o json` | Exit 6; `finally` block still runs to completion. |
| RUN-012 | `$B run -f testdata/e2e/finally-timeout/pipeline.yaml -o json` | Exit 6 from the finally budget; tasks block already succeeded. |
| RUN-013 | `--param k=` (empty value) on a param-consuming pipeline | Pin: accepted-as-empty vs exit 2; deterministic. |
| RUN-014 | `--param noequalshere` | Exit 2; message "expects key=value". |
| RUN-015 | `--param k=a --param k=b` | Deterministic (last-wins expected); pin and document. |
| RUN-016 | param referenced but neither passed nor defaulted | Deterministic exit 4 (validate) or 5 (run); pin which, before/after container start. |
| RUN-017 | image `does-not-exist:nope` | Status `infrafailed`; pin exit code (5 expected); clear message; no hang. |
| RUN-018 | `command: [/no/such/bin]` in a valid image | Task `failed`; exit 5. |
| RUN-019 | `--max-parallel 1` vs default on a multi-task level | Both exit 0; ordering still correct; with `1`, no two tasks overlap. |
| RUN-020 | `--max-parallel 0` / `-1` | Deterministic: either rejected (exit 2) or clamped; pin and document; no deadlock. |
| RUN-021 | `--cleanup` on success and on failure | Workspace tmpdirs removed in both cases (inspect state/cache dirs after). |

## F — volumes, templates, step shapes `DOCKER`

| ID | Cmd | Expect |
|---|---|---|
| VOL-001 | `$B run -f testdata/e2e/volumes/pipeline.yaml -o json` | Exit 0; inline configMap + emptyDir mounted & readable. |
| VOL-002 | `$B run -f testdata/e2e/configmap-from-yaml/pipeline.yaml -o json` | Exit 0; embedded `kind: ConfigMap` mounted. |
| VOL-003 | `$B run -f testdata/e2e/secret-from-yaml/pipeline.yaml -o json` | Exit 0; both `data` (base64) and `stringData` projected; stringData wins. |
| VOL-004 | inline + dir + embedded same `(name,key)` | Precedence inline > dir > embedded observed in mounted value. |
| VOL-005 | `--configmap name` (no `=k=v`) | Exit 2; clear parse error. |
| VOL-006 | ConfigMap with `binaryData` | Exit 4 at load (rejected). |
| VOL-007 | secret key with `../escape` | Rejected (no path traversal); exit 4. |
| TPL-001 | `$B run -f testdata/e2e/step-template/pipeline.yaml -o json` | Exit 0; steps inherit image+env; overriding step's env wins. |
| SA-001 | `$B run -f testdata/e2e/step-actions/pipeline.yaml -o json` | Exit 0; caller param overrides StepAction default; result via `$(steps.X.results.Y)`. |
| SA-002 | `Step.ref:{name: missing}` | Exit 4; clear "StepAction not found". |
| SC-001 | `$B run -f testdata/e2e/sidecars/pipeline.yaml -o json` | Exit 0; `sidecar-start`+`sidecar-end` events; step reaches sidecar on localhost. |
| SC-002 | `--sidecar-start-grace 0` on sidecars fixture | Deterministic; either passes or fails with a clear race message — no hang. |
| MX-001 | `$B run -f testdata/e2e/matrix/pipeline.yaml -o json` | Exit 0; 4 task instances; `$(tasks.X.results.Y[*])` aggregates per-row results. |
| MX-002 | `$B run -f testdata/e2e/matrix-include/pipeline.yaml -o json` | Exit 0; include rows added; array order preserved. |
| MX-003 | matrix with 257 rows | Exit 4 (256-row cap). |
| MX-004 | matrix with an empty param list | Deterministic (0 rows → no-op or exit 4); pin. |

## G — resolvers

Use **local** servers (bare git repo, `httptest`, in-mem OCI registry) so
these run offline. `NET` only where a public endpoint is unavoidable.

| ID | Cmd | Expect |
|---|---|---|
| RES-001 | resolver ref `cluster` with no opt-in | Refused; clear "cluster resolver off by default" message; exit 4 (validate) or resolver error. |
| RES-002 | same + `--resolver-allow=git,hub,http,bundles,cluster` | Opt-in accepted (resolver dispatches). |
| RES-003 | `http://` non-loopback resolver, no flag | Refused (HTTPS-only default). |
| RES-004 | `http://` non-loopback + `--resolver-allow-insecure-http` | Allowed. |
| RES-005 | `http://127.0.0.1:<port>` resolver | Allowed (loopback exempt) without the insecure flag. |
| RES-006 | git resolver, happy path (local bare repo) | Exit 0; `resolver-start`+`resolver-end`; `resolver-end` `cached:false` first run. |
| RES-007 | git resolver, missing `pathInRepo` | Resolver error; deterministic exit; clear message. |
| RES-008 | git resolver, nonexistent revision | Resolver error naming the revision. |
| RES-009 | hub/http resolver returning 404 | Error with a helpful hint; deterministic exit. |
| RES-010 | hub/http resolver returning 5xx | Single retry then give-up; deterministic error. |
| RES-011 | re-run RES-006 against same cache dir | `resolver-end` `cached:true`; no network call. |
| RES-012 | `--offline` after RES-006 warmed the cache | Cache hit served; exit 0. |
| RES-013 | `--offline` with a cold cache | Miss rejected; deterministic non-zero exit; clear "offline" message. |

## H — cache subcommand

| ID | Cmd | Expect |
|---|---|---|
| CCH-001 | `$B cache list -o json` (warm cache) | Exit 0; entries with size + age fields. |
| CCH-002 | `$B cache list -o json` (empty/nonexistent dir) | Exit 0; empty list; no panic. |
| CCH-003 | `$B cache prune --older-than 0s` | Removes all entries; exit 0. |
| CCH-004 | `$B cache prune --older-than 9999h` | Removes nothing; exit 0. |
| CCH-005 | `$B cache prune --older-than banana` | Exit 2; duration parse error. |
| CCH-006 | `$B cache clear` (no `-y`) | Does **not** wipe; exit non-zero or explicit refusal message. |
| CCH-007 | `$B cache clear -y` | Wipes; exit 0. |

## I — logs & runs (persistence / replay)

| ID | Cmd | Expect |
|---|---|---|
| LOG-001 | `$B run -f testdata/e2e/hello/pipeline.yaml -o json > live.jsonl; $B logs latest -o json > replay.jsonl` `DOCKER` | `diff live.jsonl replay.jsonl` is **byte-identical**. |
| LOG-002 | `$B runs list` after one run | Newest-first listing; the run appears. |
| LOG-003 | `$B logs 1 -o json` | Replays run #1. |
| LOG-004 | `$B logs <ulid-prefix> -o json` | Replays the matching run. |
| LOG-005 | `$B logs` with empty store | Deterministic non-zero exit; clear message; no panic. |
| LOG-006 | `$B logs 999` (out of range) | Deterministic non-zero exit; clear "not found". |
| LOG-007 | `$B run` a **failing** pipeline, then `$B logs latest -o json` `DOCKER` | Failed run persisted and replays with the failure status intact. |
| LOG-008 | ambiguous ulid prefix `$B logs 0` | Error "ambiguous" (not arbitrary pick). |
| LOG-009 | `$B logs latest --task <t> --step <s> -o json` | Same filtering on replay as live. |
| RNS-001 | `TKN_ACT_KEEP_RUNS=2 $B runs prune` after ≥3 runs | Keeps newest 2. |
| RNS-002 | `TKN_ACT_KEEP_RUNS=0 $B runs prune` | Count gate disabled; nothing pruned by count. |
| RNS-003 | `$B runs prune --all` (no `-y`) | Refuses (confirmation required). |
| RNS-004 | `$B runs prune --all -y` | Wipes all runs; exit 0. |
| RNS-005 | `--state-dir X` overrides `TKN_ACT_STATE_DIR` overrides XDG | Run record lands in X; precedence holds. |
| RNS-006 | run with `TKN_ACT_STATE_DIR` on a read-only fs | Pin (charter I3): does the run still execute & report, or hard-fail? Must be deterministic + documented. |

## J — output formatting & filters

| ID | Cmd | Expect |
|---|---|---|
| OUT-001 | hello fixture pretty vs `-o json` `DOCKER` | Same task/step outcomes; pretty prefixes `task/step`. |
| OUT-002 | `-q` on hello `DOCKER` | No step logs / no header; task+run summaries only. |
| OUT-003 | `-v` on hello `DOCKER` | Step-start/step-end markers present. |
| OUT-004 | `--timestamps` on hello (pretty) `DOCKER` | Lines prefixed `[HH:MM:SS.mmm]`. |
| OUT-005 | `--color=always` piped to a file `DOCKER` | ANSI escapes present in file. |
| OUT-006 | `NO_COLOR=1 --color=always` `DOCKER` | Flag wins per docs precedence: ANSI present (`--color=always` > `NO_COLOR`). |
| OUT-007 | `-q -v` together | Deterministic resolution; pin which wins; no crash. |
| OUT-008 | step emitting a 200KB line / no trailing newline / embedded CR `DOCKER` | No truncation panic; line stays atomic in pretty; valid JSON in `-o json`. |
| FLT-001 | `--task <real>` on a multi-task pipeline `DOCKER` | Live output limited to that task; run-boundary + error events still pass. |
| FLT-002 | `--task ghost` (no such task) | Deterministic: empty-but-valid stream + exit follows the run; no crash. |
| FLT-003 | `--task a --step b -o json` | AND semantics; identical filtering as pretty. |

## K — cluster backend `CLUSTER`

| ID | Cmd | Expect |
|---|---|---|
| CLS-001 | `$B cluster up` then again | Second call idempotent (no error, no double-install). |
| CLS-002 | `$B cluster status -o json` (up) | `{name,exists:true,running:true,detail,kubeconfig}`; exit 0. |
| CLS-003 | `$B cluster status -o json` (none) | `exists:false`; exit 0. |
| CLS-004 | `TKN_ACT_TEKTON_VERSION=v1.3.0 $B cluster up --tekton-version v1.12.0` | Flag wins (v1.12.0 installed); precedence flag>env. |
| CLS-005 | `$B cluster up --tekton-version v0.0.0-bogus` | Clean error (no half-installed cluster); deterministic non-zero exit. |
| CLS-006 | `$B cluster down` (no `-y`) | Pin: prompt vs refuse; non-destructive without confirm. |
| CLS-007 | `$B run --cluster -f testdata/e2e/hello/pipeline.yaml -o json` (cluster down) | Deterministic: either auto-ups or exit 3 telling you to `cluster up`. |
| CLS-008 | run each e2e fixture with `--cluster`, diff vs docker outcome `DOCKER`+`CLUSTER` | Same exit code + terminal status + event kinds, modulo documented `DockerOnly`/`ClusterOnly`. |

## L — remote docker `DOCKER`

| ID | Cmd | Expect |
|---|---|---|
| RMT-001 | `--remote-docker=on` against a reachable daemon, hello fixture | Exit 0; per-run volume staging used (not bind mounts). |
| RMT-002 | `--docker-host tcp://127.0.0.1:1` (unreachable) | Exit 3; clear connection error; no hang. |
| RMT-003 | `--docker-host bogus://x` (bad scheme) | Exit 2/3 deterministic; clear message. |
| RMT-004 | `TKN_ACT_PAUSE_IMAGE=<mirror> --remote-docker=on` | Stager uses the override image; precedence flag>env>default. |

## M — cross-cutting & abuse

| ID | Cmd | Expect |
|---|---|---|
| SIG-001 | Ctrl-C (SIGINT) during a long step `DOCKER`+`SIG` | Exit 130; coherent terminal output; containers/volumes/networks torn down (`docker ps -a` shows no leaked `tkn-act-*`). |
| SIG-002 | SIGTERM during image pull `DOCKER`+`SIG` | Exit 130; no orphaned resources. |
| SIG-003 | double Ctrl-C `DOCKER`+`SIG` | Still exits 130; no panic; teardown best-effort. |
| ABU-001 | 50MB YAML to `validate` | Bounded memory; deterministic exit; no OOM/panic. |
| ABU-002 | YAML alias/anchor bomb | Loader bounds expansion; no hang/OOM; exit 4. |
| ABU-003 | YAML with duplicate keys | Deterministic (reject vs last-wins); pin. |
| ABU-004 | UTF-8 BOM + CRLF pipeline.yaml | Parses correctly; exit 0/4 as content dictates. |
| ABU-005 | Pipeline with 1000 tasks | Validates/runs without quadratic blowup; deterministic. |
| ABU-006 | Unicode/emoji in task & param names `DOCKER` | Handled or rejected cleanly; no mojibake in JSON (valid UTF-8 escapes). |
| ENV-001 | `$HOME` unset, run hello `DOCKER` | Deterministic: clear error about cache/state dir, or sane fallback; no panic. |
| ENV-002 | `XDG_CACHE_HOME` pointing at a regular file | Clear error (not a dir); exit 3; no panic. |
| ENV-003 | full env-precedence matrix (charter M5) | For each flag/env pair, flag>env>default holds; one assertion each. |
| CNC-001 | two `$B run` in same dir concurrently `DOCKER` | Distinct run records; workspace tmpdirs don't collide; both deterministic. |
| CNC-002 | `$B run` + `$B runs prune` concurrently `DOCKER` | No corruption of the store; both exit deterministically. |

---

## Maintenance

- When a charter in the exploratory plan finds a reproducible behaviour worth
  guarding, add a spec here with the next free `AREA-NNN`.
- When a filed issue is fixed, the spec that caught it becomes the
  regression guard — link the issue and PR in the row's notes in
  `findings-log.md`.
- Keep this file in sync with the stable-contract tables in `AGENTS.md` and
  `../agent-guide/README.md`; a new event kind, exit code, or flag should
  appear as a spec here in the same PR that adds it.
