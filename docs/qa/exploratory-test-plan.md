# tkn-act exploratory test plan

Breadth-first map of where bugs are likely to hide. Each **charter** is a
timeboxed mission ("explore X using Y to discover Z"), with concrete *normal*
cases and *edge* cases to provoke. This is the hunting ground; confirmed,
reproducible behaviours get distilled into [`regression-suite.md`](regression-suite.md)
and, when wrong, into a GitHub issue.

Legend: `NET` needs network egress · `DOCKER` needs a docker daemon ·
`CLUSTER` needs k3d+kubectl · `SIG` exercises signals · everything else runs
offline and daemon-free.

How to read a charter: skim the *Probe* questions, then run the *Normal* row
to establish the baseline, then attack with the *Edge* rows. A surprise is
anything where the observed exit code, JSON shape, or message contradicts
[`../agent-guide/README.md`](../agent-guide/README.md) or a reasonable user's
expectation.

---

## Area A — CLI surface & introspection

**Charter A1 — help / help-json / version contract.**
Probe: Is `help-json` a faithful, parseable mirror of the real flag set? Does
every documented exit code appear? Are short forms (`-o -p -q -v -f -w -C`)
all present and unique?

- Normal: `help-json | jq .` parses; `exit_codes[]` lists 0,1,2,3,4,5,6,130;
  every subcommand from `--help` appears under `commands[]`; `version -o json`
  → `{"name":"tkn-act","version":...}`.
- Edge: every flag in `run --help` text has a matching entry in `help-json`'s
  `tkn-act run` flags (no drift); no duplicate short-flag letters within a
  command; `help-json -o pretty` (does an unsupported `-o` on an
  always-JSON command error or silently ignore?); `version --output garbage`
  (unknown format → exit 2 or silent?); `tkn-act` with no args and no
  discoverable pipeline (help vs error vs exit?).

**Charter A2 — agent-guide embedding.**
Probe: Does the embedded guide match the on-disk `docs/agent-guide/` tree?
- Normal: `agent-guide --list` enumerates sections matching the `.md` files;
  `agent-guide --section resolvers` prints that one; bare `agent-guide` prints
  the curated concatenation.
- Edge: `--section nonexistent` (exit code? helpful message listing valid
  names?); `--section ""`; `--list --section x` (contradictory — which wins?);
  output piped to a closed pipe (`agent-guide | head -1` — broken-pipe panic?).

**Charter A3 — global flag parsing & precedence.**
Probe: Do contradictory/duplicate flags resolve predictably?
- Edge: `--color=always --no-color` (precedence per docs: never wins);
  `--output json --output pretty` (last wins? error?); `-o JSON` (case
  sensitivity); `--output=` (empty); unknown global flag `--frobnicate` →
  exit 2; `--max-parallel 0` / `-1` / `99999` on a real run; flag after
  positional vs before.

---

## Area B — doctor

**Charter B1 — doctor truthfulness & exit code. `DOCKER`**
Probe: Does `ok` exactly track the `default`-required checks? Does exit code
follow `ok`?
- Normal: with Docker up → `ok:true`, exit 0; JSON shape matches the
  documented `{ok,version,os,arch,cache_dir,checks[]}` with each check carrying
  `name/ok/detail/required_for`.
- Edge: `DOCKER_HOST=tcp://127.0.0.1:1` (unreachable) → docker check `ok:false`,
  top-level `ok:false`, **exit 3**; cache dir made unwritable (`chmod 000`) →
  cache_dir check fails; k3d/kubectl absent (`PATH=/usr/bin`) → those checks
  `ok:false` but top-level still `ok:true` and **exit 0** (cluster is
  optional); pretty (non-JSON) doctor output is human-readable and consistent
  with the JSON.

---

## Area C — validate

**Charter C1 — schema, DAG, when, results rejection (exit 4).**
Probe: Does every documented validation class reject with exit 4 and a useful
`errors[]`? Does valid YAML pass with exit 0?
- Normal: a known-good fixture (`testdata/e2e/hello`) → `{ok:true,errors:[]}`,
  exit 0.
- Edge (each must be exit 4, never 1/5/panic):
  - Unknown `apiVersion` / `kind`.
  - Duplicate task names in a Pipeline.
  - `runAfter` referencing a non-existent task.
  - A DAG cycle (A→B→A).
  - `when` with an unknown operator (not `in`/`notin`).
  - `$(tasks.X.results.Y)` where X doesn't exist or doesn't declare Y.
  - Negative `retries`.
  - Malformed `timeout` (`"banana"`, `-5s`).
  - Unknown `onError` value (not `continue`/`stopAndFail`).
  - `matrix.include` overlapping a cross-product param (documented exit-4 limitation).
  - Volume with unknown kind / multiple sources / undeclared volumeMount.
- Edge (parse-level, still exit 4): truncated YAML, tabs in YAML, a non-YAML
  file (`-f README.md`), an empty file, a file that is valid YAML but not a
  Tekton doc (`{}`), a multi-doc stream where one doc is broken.
- Edge (input plumbing): `-f /does/not/exist` (exit 2 usage or 4? — probe and
  pin); `-f -` stdin; `-f a.yaml -f b.yaml` multi-file; a directory passed to
  `-f`.

**Charter C2 — validate vs run agreement.**
Probe: Does anything that `validate` passes then fail to *load* at `run` time
(or vice-versa)? Inconsistency here is a contract smell.
- Edge: take each rejecting fixture from C1 and confirm `run` rejects it the
  same way (same exit 4, before any container starts).

---

## Area D — list & discovery

**Charter D1 — discovery rules.**
Probe: Does auto-discovery find `pipeline.yaml` and `.tekton/` and nothing it
shouldn't?
- Normal: dir with `pipeline.yaml` → listed; `.tekton/` dir → listed; JSON
  shape `{pipelines:[],tasks:[]}`.
- Edge: empty dir (`{pipelines:[],tasks:[]}` + exit 0? or exit 2?); dir with
  only a ConfigMap/Secret YAML (must NOT be auto-discovered per docs);
  `.tekton/` with nested subdirs; a `pipeline.yaml` that is invalid (does list
  surface a partial result or error?); symlinked pipeline.yaml; both
  `pipeline.yaml` and `.tekton/` present (union? precedence?); `-C nonexistent`.

---

## Area E — run: core engine semantics `DOCKER`

**Charter E1 — happy paths across feature fixtures.**
Probe: Every `shipped` feature fixture runs green on docker and produces the
documented JSON event stream.
- Normal: run each `testdata/e2e/*` fixture with `-o json`; assert terminal
  `run-end` status and exit 0 where expected, exit 5 for `failure-propagation`,
  exit 6 for `timeout`/`*-timeout` fixtures.
- Edge per fixture: re-run with `-o json` and validate the event *grammar* —
  every `task-start` has a matching `task-end`; `step-start`/`step-end` nest;
  `task-retry` count matches `retries`; `attempt:N` on terminal `task-end`;
  `run-start` first, `run-end` last; no event after `run-end`.

**Charter E2 — params (string / array / object).**
- Normal: `--param k=v`; array via repeated `--param`? or comma? (pin the real
  syntax); object params from YAML defaults; `$(params.x)` and `$(params.x[*])`
  substitution lands in step output.
- Edge: `--param` with no `=` (exit 2, message "expects key=value"); `--param
  k=` (empty value — allowed?); `--param k=v=w` (value contains `=`); duplicate
  `--param k=a --param k=b` (last wins? error?); param referenced but neither
  passed nor defaulted (exit 4 at validate or 5 at run?); param name with
  special chars; very long value (10KB); value with newlines / quotes / `$()`
  injection attempt (`--param k='$(tasks.evil.results.x)'` — is it treated as
  data, not re-expanded?).

**Charter E3 — results & cross-task data flow.**
- Normal: a task writes `/tekton/results/foo`, downstream reads
  `$(tasks.A.results.foo)`; implicit DAG edge created by the reference.
- Edge: result file never written (downstream sees empty? run fails?); result
  larger than Tekton's 4KB convention (does tkn-act cap or pass through?);
  binary bytes in a result; result name with dashes; a `when` clause gated on a
  result value; result referenced by a `finally` task.

**Charter E4 — workspaces.**
- Normal: `-w shared=./build` shared across two tasks; file written by task A
  visible to task B.
- Edge: `-w name=` (empty path); `-w name=/nonexistent`; workspace declared by
  pipeline but not provided on CLI (exit code?); same host dir to two
  workspaces; relative vs absolute path; path with spaces; read-only host dir;
  `--cleanup` actually removes tmpdirs on both success and failure.

**Charter E5 — DAG, when, finally.**
- Normal: `runAfter` ordering honoured; `when in/notin` skips a task
  (`task-skip` event); `finally` runs even when a DAG task failed.
- Edge: a `when` that skips a task whose result a later task needs (downstream
  `not-run`?); `finally` task that itself fails (exit 5? does it mask a prior
  success?); diamond DAG; a task with `runAfter` on a skipped task; `finally`
  with `when`.

**Charter E6 — failure & onError propagation.**
- Normal: task fails → downstream `not-run`/`skipped`, exit 5;
  `onError: continue` lets the step's non-zero exit pass.
- Edge: first step of a multi-step task fails (later steps run? per Tekton, no —
  confirm); `onError: continue` on the *last* step; a task that exits 0 but
  writes garbage; an image that doesn't exist (`image: nope:nope` → infrafailed,
  which exit code?); a command that isn't in the image (`command: [nope]`).

**Charter E7 — timeouts (exit 6). `DOCKER`**
- Normal: `Task.spec.timeout`, `Pipeline.spec.timeouts.{pipeline,tasks,finally}`
  each fire and yield exit 6 with status `timeout`.
- Edge: timeout of `0s` (infinite per Tekton? or immediate?); timeout shorter
  than image pull; `tasks` budget exhausted but `finally` still runs (documented
  fixture — confirm finally completes); pipeline timeout vs task timeout racing;
  a retried task whose total attempts exceed the task timeout.

---

## Area F — run: volumes, templates, step shapes `DOCKER`

**Charter F1 — configMap / secret (three layers).**
- Normal: inline `--configmap n=k=v`; `--configmap-dir`; embedded `kind:
  ConfigMap`/`kind: Secret` in the `-f` stream; mounted and readable in a step.
- Edge: layering precedence (inline > dir > embedded for the same `(name,key)`);
  Secret `stringData` wins over `data`; ConfigMap `binaryData` rejected at load
  (exit 4); malformed inline (`--configmap n` with no `=k=v`); base64-invalid
  secret `data`; items-projection; `..` traversal in a key (must be rejected);
  a volumeMount referencing an undeclared volume (exit 4).

**Charter F2 — stepTemplate inheritance.**
- Normal: steps inherit image/env/workingDir; a step overriding env wins; env
  merged by name.
- Edge: stepTemplate with no steps; a step that clears an inherited value;
  conflicting `command` between template and step.

**Charter F3 — StepAction inlining.**
- Normal: `Step.ref:{name}` inlines a local `StepAction`; caller `params:`
  override defaults; results flow via `$(steps.X.results.Y)`.
- Edge: `ref` to a missing StepAction (exit 4?); required StepAction param not
  bound and no default (exit 4?); resolver-form `ref` (hub/git) — documented as
  unsupported (Track 1 #9), what's the error?; param name collision.

**Charter F4 — sidecars. `DOCKER`**
- Normal: redis sidecar reachable at `localhost:6379` from a step;
  `sidecar-start`/`sidecar-end` events fire; pause-container netns shared.
- Edge: `--sidecar-start-grace 0`; sidecar that never becomes ready; sidecar
  that crashes mid-step; `--sidecar-stop-grace 1s` vs a sidecar ignoring
  SIGTERM (SIGKILL window); two sidecars on the same port (conflict surfaced?);
  step finishes before sidecar grace elapses.

**Charter F5 — matrix fan-out.**
- Normal: 2×2 cross-product → 4 task instances; per-row results aggregated into
  `$(tasks.X.results.Y[*])`; `matrix.include` adds named rows.
- Edge: a single-element matrix list; an empty matrix list (0 rows — error or
  no-op?); cardinality cap at 256 (257 rows → exit 4?); `include` overlapping a
  cross-product cell (documented exit-4 limitation); per-row `when`; result
  array ordering determinism across runs; matrix param referencing another
  task's result array.

---

## Area G — resolvers `NET`/local

**Charter G1 — allow-list & security defaults.**
Probe: Is `cluster` truly off by default? Is plain `http://` refused?
- Normal: default `--resolver-allow=git,hub,http,bundles`; a `cluster` resolver
  ref with no opt-in → refused with a clear message.
- Edge: `--resolver-allow=cluster` opts in; `--cluster-resolver-context=x` also
  opts in; an unknown resolver name; `http://` non-loopback refused unless
  `--resolver-allow-insecure-http`; `http://127.0.0.1` (loopback) always
  allowed; bearer token via env `TKNACT_HTTP_RESOLVER_TOKEN` vs `--resolver-config`.

**Charter G2 — direct resolvers happy/edge (use local servers).**
- Normal (local, no real net): `git` against a local bare repo (file://);
  `http` against an `httptest`-style local server; `bundles` against a local
  OCI registry. Each emits `resolver-start`/`resolver-end`.
- Edge: missing `pathInRepo`; nonexistent revision; 404 from hub/http (helpful
  hint?); 5xx single-retry budget then give-up; missing required resolver
  params (fast-fail before network); malformed URL.

**Charter G3 — cache & offline.**
- Normal: first run fetches (`cached:false` on `resolver-end`); second run from
  the same cache dir serves `cached:true` without network; `--offline` serves a
  cache hit and **rejects** a miss.
- Edge: `--offline` on a cold cache (every ref a miss → exit code?); corrupted
  cache file (does it re-fetch or error?); `cache list` shows entries with size
  + age; `cache prune --older-than 0s` clears all; `cache prune --older-than
  9999h` clears none; `cache clear` without `-y` (no-op + prompt?) vs with `-y`;
  cache dir on a read-only fs.

---

## Area H — cache subcommand

**Charter H1 — cache CLI shapes.**
- Normal: `cache list -o json` shape; `cache prune --older-than <dur>`; `cache
  clear -y`.
- Edge: `cache list` on an empty/nonexistent cache dir; `--older-than` with a
  bad duration (`banana`) → exit 2; `cache clear` without `-y` (does it refuse,
  prompt, or wipe?); `cache prune --older-than -1h`.

---

## Area I — logs & runs (persistence / replay)

**Charter I1 — record/replay byte-fidelity.**
Probe: Does `logs` replay reproduce the live JSON stream exactly?
- Normal: `run -o json` then `logs latest -o json` — byte-equal event stream;
  `runs list` shows the run newest-first; `logs N` by sequence; `logs <ulid-prefix>`.
- Edge: `logs` with empty store (exit code? message?); `logs 999` (out of
  range); `logs latest` after a *failed* run (does a failure run still persist
  and replay?); ambiguous ulid prefix (error?); `--task`/`--step`/`--timestamps`
  filters apply identically on replay as live; `logs` while a run is in flight.

**Charter I2 — retention & prune.**
- Normal: `TKN_ACT_KEEP_RUNS=2` keeps the 2 newest after `runs prune`;
  `TKN_ACT_KEEP_DAYS` age gate; `runs prune --all`.
- Edge: `KEEP_RUNS=0` (disables count gate — nothing pruned by count);
  `KEEP_DAYS=0` (disables age gate); both gates active (intersection vs union —
  which?); prune when store is empty; concurrent runs writing while prune runs;
  `--state-dir` precedence over `TKN_ACT_STATE_DIR` over XDG default; a
  state-dir on a read-only fs (graceful? or does the run itself fail?).

**Charter I3 — does a run *require* a writable store?**
Probe: If persistence fails, does the run still execute and report, or does it
hard-fail? Pin this — agents care whether `run` is coupled to the store.

---

## Area J — output formatting & filters

**Charter J1 — pretty vs json parity.**
- Normal: same run in pretty and json; pretty prefixes `task/step`; `-q`
  suppresses step logs + header; `-v` adds step boundaries; `--timestamps`
  prepends `[HH:MM:SS.mmm]`.
- Edge: `-q -v` together (contradictory — which wins?); `--color=always` into a
  non-TTY emits ANSI; `NO_COLOR=1 --color=always` (flag should win per docs);
  step output with embedded ANSI / carriage returns / very long lines / no
  trailing newline; interleaving of parallel task logs stays line-atomic.

**Charter J2 — `--task` / `--step` filters.**
- Normal: `--task build` limits live output to that task; `--step compile` AND
  semantics; run-boundary + error events always pass through.
- Edge: `--task nonexistent` (empty but valid? error?); `--task a --task b`
  (union); `--task a --step x` (AND); filters on `-o json` affect the stream
  identically to pretty; an empty-Task envelope event bypasses the filter.

---

## Area K — cluster backend `CLUSTER`

**Charter K1 — cluster lifecycle.**
- Normal: `cluster up` (idempotent? re-running while up?); `cluster status -o
  json` shape `{name,exists,running,detail,kubeconfig}`; `cluster down -y`.
- Edge: `cluster up --tekton-version v1.3.0` and `TKN_ACT_TEKTON_VERSION` env
  (flag > env precedence); `cluster up` with an invalid version tag (clean
  error?); `cluster down` without `-y` (prompt vs refuse); `cluster status`
  when no cluster exists (`exists:false`, exit 0); `run --cluster` when cluster
  is down (auto-up? or exit 3 telling you to `cluster up`?); two `cluster up`
  concurrently.

**Charter K2 — cross-backend parity.**
Probe: Does every fixture that passes on docker pass identically on cluster
(modulo documented `DockerOnly`/`ClusterOnly`)? Same exit codes, same terminal
status, same event kinds?
- Run the e2e fixtures with `--cluster` and diff outcomes vs docker.

---

## Area L — remote docker `DOCKER`

**Charter L1 — remote-mode detection & staging.**
- Normal: `--docker-host tcp://…` against a reachable remote daemon; `--remote-docker=on`
  forces per-run volume staging; workspaces staged on a volume not bind-mounted.
- Edge: `--remote-docker=on` with a *local* socket (does staging still work?);
  `--docker-host` with a bad scheme; `ssh://` without keys (clear error?);
  `TKN_ACT_PAUSE_IMAGE` honoured for the stager; precedence flag > env > default.

---

## Area M — cross-cutting & abuse

**Charter M1 — signals (exit 130). `SIG`**
Probe: SIGINT/SIGTERM mid-run → exit 130, graceful container teardown, no
orphaned containers/volumes, a coherent terminal event.
- Edge: Ctrl-C during step execution; SIGTERM during image pull; double Ctrl-C;
  signal during `cluster up`; signal during a resolver fetch; verify no leaked
  `tkn-act-*` containers/volumes/networks afterwards (`docker ps -a`).

**Charter M2 — malformed & hostile inputs.**
- Edge: 50MB YAML file; deeply nested YAML (billion-laughs / alias bomb — does
  the loader bound it?); YAML with duplicate keys; UTF-8 BOM; CRLF line endings;
  non-UTF-8 bytes; a pipeline with 1000 tasks; a task name of 300 chars;
  Unicode/emoji in names and params; `-f` the same file twice; circular
  `-f`-included… (n/a, no includes) — focus on the loader's robustness.

**Charter M3 — filesystem & permissions.**
- Edge: read-only cwd; `XDG_CACHE_HOME` pointing at a file (not a dir); cache
  dir full disk (simulate via small tmpfs if feasible); `$HOME` unset;
  `TMPDIR` unset/odd; running from inside a path with spaces/unicode.

**Charter M4 — concurrency.**
- Edge: two `tkn-act run` in the same dir simultaneously (shared workspace
  tmpdirs collide? distinct run dirs in the store?); a run + `runs prune`
  concurrently; a run + `cache clear` concurrently.

**Charter M5 — env-var precedence matrix.**
Probe: For every documented flag-with-env-and-default, confirm flag > env >
default exactly once: `--docker-host`/`DOCKER_HOST`,
`--remote-docker`/`TKN_ACT_REMOTE_DOCKER`, `--pause-image`/`TKN_ACT_PAUSE_IMAGE`,
`--state-dir`/`TKN_ACT_STATE_DIR`, `--tekton-version`/`TKN_ACT_TEKTON_VERSION`,
`--color`/`NO_COLOR`/`FORCE_COLOR`/`CLICOLOR_FORCE`. Each combination is a
one-liner that prints which value won.

---

## Coverage map (charter → known in-tree test)

This is deliberately *outside-in*; many charters overlap an existing Go test,
but the point is to drive the **binary** and find seams the unit tests don't
cover (flag parsing, exit-code mapping, signal teardown, env precedence, the
record→replay round-trip through the real CLI). Where a charter merely
re-confirms a green unit test, note "covered" in the findings log and move the
time budget to the edge rows, which are where the unit suites are thin (see
[`../test-coverage.md`](../test-coverage.md) §3 "What is NOT covered").
