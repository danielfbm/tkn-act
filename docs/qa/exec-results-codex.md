# `tkn-act` docker-backed regression execution

Date: `2026-06-03`

- `tkn-act version`: `tkn-act dev`
- OS/arch: `darwin arm64`
- Scratch `TKN_ACT_STATE_DIR`: `/tmp/claude-501/tkn-act-state-auq6itu4/state`
- Scratch `XDG_CACHE_HOME`: `/tmp/claude-501/tkn-act-cache-9n6j9ij5/cache`
- `doctor -o json` summary: `ok=true`; `cache_dir` and `docker` passed; `k3d` and `kubectl` also passed on this host.

## Results

### Area A — CLI

| Spec | Result | Exit | Note |
|---|---|---:|---|
| CLI-001 | PASS | 0 | `version -o json` emitted only `{"name":"tkn-act","version":"dev"}`. |
| CLI-002 | PASS | 0 | `help-json` parsed; exit codes were exactly `0,1,2,3,4,5,6,130`. |
| CLI-003 | PASS | 0 | `help-json` mirrored `run` flags including `file,param,pipeline,workspace,dir`. |
| CLI-004 | FAIL | 1 | `--frobnicate` returned `unknown flag` but exited `1`, not documented usage `2`. |
| CLI-005 | FAIL | 1 | Unknown top-level subcommand returned `unknown command` but exited `1`, not `2`. |
| CLI-006 | FAIL | 1 | `run --param` returned `flag needs an argument` but exited `1`, not `2`. |
| CLI-007 | FAIL | 0 | `agent-guide --list` printed section names including `overview` and omitted `README`. |
| CLI-008 | PASS | 0 | `agent-guide --section resolvers` printed only the resolvers section. |
| CLI-009 | PASS | 2 | Invalid section rejected with a valid-sections list. |
| CLI-010 | PIN | 0 | `version --output garbage` silently fell back to pretty: `tkn-act dev`. |
| CLI-011 | PASS | 0 | `--no-color` beat `--color=always`; no ANSI escapes observed. |
| CLI-012 | PASS | 0 | `agent-guide | head -1` had no broken-pipe panic. |
| CLI-X01 | PIN | 1 | `run --bogus` returned `unknown flag: --bogus`. |
| CLI-X02 | PIN | 0 | `cluster bogus` printed group help and exited `0`. |
| CLI-X03 | PIN | 1 | `totallybogus` returned `unknown command ...` and exited `1`. |

### Area B — doctor

| Spec | Result | Exit | Note |
|---|---|---:|---|
| DOC-001 | PASS | 0 | JSON shape matched `{ok,version,os,arch,cache_dir,checks[]}`. |
| DOC-002 | PASS | 3 | `DOCKER_HOST=tcp://127.0.0.1:1` made only the docker check fail; top-level `ok=false`. |
| DOC-003 | PASS | 0 | `PATH=/usr/bin` hid `k3d`/`kubectl`; top-level `ok` stayed `true`. |
| DOC-004 | PASS | 0 | Pretty output mentioned every check from JSON. |
| DOC-X01 | PIN | 0 | `doctor --output garbage` silently fell back to pretty output. |

### Area C — validate

| Spec | Result | Exit | Note |
|---|---|---:|---|
| VAL-001 | PASS | 0 | Valid hello fixture returned `{"ok":true,"errors":[]}`. |
| VAL-002 | FAIL | 4 | Unsupported `apiVersion` exited `4` but emitted only stderr, not JSON `errors[]`. |
| VAL-003 | FAIL | 0 | Duplicate PipelineTask names validated cleanly instead of rejecting. |
| VAL-004 | PASS | 4 | Unknown `runAfter` task `ghost` rejected. |
| VAL-005 | PASS | 4 | DAG cycle rejected. |
| VAL-006 | PASS | 4 | Invalid `when` operator rejected. |
| VAL-007 | FAIL | 0 | `$(tasks.nope.results.x)` validated cleanly instead of rejecting. |
| VAL-008 | PASS | 4 | Negative `retries` rejected. |
| VAL-009 | PASS | 4 | `timeout: banana` rejected. |
| VAL-010 | PASS | 4 | Invalid `onError` rejected. |
| VAL-011 | PASS | 4 | `matrix.include` overlap limitation rejected as documented. |
| VAL-012 | PIN | 4 | Missing file returned a file-open error with exit `4`. |
| VAL-013 | FAIL | 2 | Empty file returned `multiple pipelines loaded; specify -p` instead of parse/validate `4`. |
| VAL-014 | PASS | 4 | `README.md` rejected without a crash. |
| VAL-015 | PIN | 4 | `validate -f -` treated `-` as a literal path and failed to open it. |
| VAL-016 | PASS | 4 | Multi-doc stream with broken doc 2 rejected. |
| VAL-017 | FAIL | mixed | `run` disagreed with `validate`: duplicate-task case ran green, bad result ref failed at runtime with `5`, empty file exited `2`. |
| VAL-X01 | PIN | 0 | `validate --output garbage` silently fell back to pretty `ok`. |

### Area D — list

| Spec | Result | Exit | Note |
|---|---|---:|---|
| LST-001 | PASS | 0 | Hello fixture auto-discovered as JSON. |
| LST-002 | FAIL | 2 | Empty dir returned `no tekton YAML found...` instead of `{"pipelines":[],"tasks":[]}`. |
| LST-003 | PASS | 0 | ConfigMap-only dir was not auto-discovered. |
| LST-004 | PASS | 0 | Root `pipeline.yaml` and `.tekton/` contents were both surfaced. |
| LST-005 | PIN | 2 | Nonexistent dir returned `no tekton YAML found...`. |
| LST-006 | PIN | 4 | Invalid `pipeline.yaml` returned a parse error. |
| LST-X01 | PIN | 0 | `list -o garbage` silently fell back to pretty output. |

### Area E — run engine

| Spec | Result | Exit | Note |
|---|---|---:|---|
| RUN-001 | PASS | 0 | First event `run-start`; last event `run-end` with `status:"succeeded"`. |
| RUN-002 | PASS | 0 | Event grammar/kinds were balanced and within the documented set. |
| RUN-003 | PASS | 5 | Failure-propagation fixture ended failed with downstream skip. |
| RUN-004 | PASS | 6 | Task timeout fixture ended with timeout. |
| RUN-005 | PASS | 0 | Saw `2` `task-retry` events; terminal `task-end` had `attempt:3`. |
| RUN-006 | PASS | 0 | `onError: continue` fixture succeeded. |
| RUN-007 | PASS | 0 | Saw `task-skip` for `prod-only`; `notify` finally task ran. |
| RUN-008 | PASS | 0 | Downstream step logged `got 1.2.3`. |
| RUN-009 | PASS | 0 | Re-run with an existing temp workspace path; read step logged `first` then `second`. |
| RUN-010 | PASS | 6 | Pipeline-level timeout fixture timed out. |
| RUN-011 | PASS | 6 | `tasks` timeout fired and finally task `c` still completed. |
| RUN-012 | PASS | 6 | Main task succeeded; finally task timed out under finally budget. |
| RUN-013 | PIN | 0 | Empty param value is accepted; step logged `k=`. |
| RUN-014 | PASS | 2 | `--param noequalshere` returned `--param expects key=value`. |
| RUN-015 | PIN | 0 | Duplicate params are last-wins; step logged `k=b`. |
| RUN-016 | PIN | 5 | Missing required param failed at run-time with `param "k" is required`. |
| RUN-017 | FAIL | 5 | Missing image emitted only `run-start`/`run-end failed`; no `task-end status:"infrafailed"`. |
| RUN-018 | FAIL | 5 | Missing binary produced `task-end status:"infrafailed"`, not `failed`. |
| RUN-019 | PASS | 0 | Default run overlapped `alpha`/`beta`; `--max-parallel 1` serialized them. |
| RUN-020a | PIN | 0 | `--max-parallel 0` was accepted and the run succeeded. |
| RUN-020b | PIN | 0 | `--max-parallel -1` was accepted and the run succeeded. |
| RUN-021 | PASS | 0/5 | `--cleanup` left `run/workspaces` empty after both success and failure. |
| E2E-display-name-description | PASS | 0 | Extra shipped fixture succeeded. |
| E2E-pipeline-results | PASS | 0 | Extra shipped fixture succeeded. |
| E2E-step-results | PASS | 0 | Extra shipped fixture succeeded. |
| E2E-multilog | PASS | 0 | Extra shipped fixture succeeded. |
| E2E-resolver-bundles | BLOCKED | - | Live local registry/server excluded from this batch. |
| E2E-resolver-git | BLOCKED | - | Live local repo/server excluded from this batch. |
| E2E-resolver-http | BLOCKED | - | Live local HTTP server excluded from this batch. |
| E2E-resolver-hub | BLOCKED | - | Live local HTTP server excluded from this batch. |

### Area F — volumes/templates/sidecars/matrix

| Spec | Result | Exit | Note |
|---|---|---:|---|
| VOL-001 | FAIL | 5 | Shipped `volumes` fixture failed immediately: missing configMap source `app-config`. |
| VOL-002 | PASS | 0 | Embedded ConfigMap mounted and read correctly. |
| VOL-003 | PASS | 0 | Secret `data` and `stringData` both projected; `stringData` visible. |
| VOL-004 | PASS | 0 | Layering precedence observed as `inline`. |
| VOL-005 | PASS | 2 | Malformed `--configmap name` rejected. |
| VOL-006 | PASS | 4 | ConfigMap `binaryData` rejected at load time. |
| VOL-007 | FAIL | 0 | Secret key `../escape` was not rejected; mount came up empty and run still succeeded. |
| TPL-001 | PASS | 0 | StepTemplate inheritance/override worked (`from-template`, then `from-step`). |
| SA-001 | PASS | 0 | StepAction default override/result flow worked. |
| SA-002 | PASS | 4 | Missing StepAction rejected. |
| SC-001 | PASS | 0 | Sidecar start/end events appeared; redis reachable on localhost. |
| SC-002 | PASS | 0 | `--sidecar-start-grace 0` completed without hanging. |
| MX-001 | PASS | 0 | Matrix fixture spawned `4` task instances. |
| MX-002 | PASS | 0 | Include rows produced `linux-amd64`, `linux-arm64`, `linux-armv7`. |
| MX-003 | PASS | 4 | 257-row matrix cap enforced. |
| MX-004 | PIN | 4 | Empty matrix list rejected as `must be a non-empty string list`. |

### Area G — resolvers

| Spec | Result | Exit | Note |
|---|---|---:|---|
| RES-001 | PASS | 4 | Default allow-list rejected `cluster` resolver at validation. |
| RES-002 | PASS | 5 | With opt-in, dispatch reached `resolver-start`/`resolver-end` before failing downstream. |
| RES-003 | PASS | 5 | Non-loopback `http://` was refused before network use. |
| RES-004 | PASS | 5 | `--resolver-allow-insecure-http` bypassed the policy gate; request then failed downstream. |
| RES-005 | PASS | 5 | Loopback `http://127.0.0.1:9` was allowed and reached connection-refused. |
| RES-006 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-007 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-008 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-009 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-010 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-011 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-012 | BLOCKED | - | Live local git/http/OCI setup excluded from this batch. |
| RES-013 | PASS | 4 | Cold-cache `--offline` miss was rejected at validation. |

### Area H — cache

| Spec | Result | Exit | Note |
|---|---|---:|---|
| CCH-001 | BLOCKED | - | No in-scope successful resolver run was allowed to seed cache entries. |
| CCH-002 | PASS | 0 | Empty cache returned `entries: []`. |
| CCH-003 | PIN | 0 | `prune --older-than 0s` on empty cache returned `pruned: 0`. |
| CCH-004 | PIN | 0 | `prune --older-than 9999h` on empty cache returned `pruned: 0`. |
| CCH-005 | FAIL | 1 | Invalid duration `banana` exited `1`, not usage `2`. |
| CCH-006 | PASS | 2 | `cache clear` without `-y` refused. |
| CCH-007 | PIN | 0 | `cache clear -y` on empty cache returned `cleared: 0`. |
| CCH-X01 | PIN | 0 | `cache bogus` printed group help and exited `0`. |

### Area I — logs/runs

| Spec | Result | Exit | Note |
|---|---|---:|---|
| LOG-001 | PASS | 0 | `run -o json` and `logs latest -o json` were byte-identical. |
| LOG-002 | FAIL | 0 | `runs list` was oldest-first (`1,2,3`), not newest-first. |
| LOG-003 | PASS | 0 | `logs 1 -o json` replayed run `#1`. |
| LOG-004 | PASS | 0 | Unique ULID prefix `01KT7167H` replayed the matching run. |
| LOG-005 | PASS | 2 | Empty store returned a clear non-zero error. |
| LOG-006 | PASS | 2 | `logs 999` returned clear `no matching run`. |
| LOG-007 | PASS | 5/0 | Failing run persisted and replayed with `status:"failed"`. |
| LOG-008 | FAIL | 2 | `logs 0` was parsed as invalid sequence number, not ambiguous ULID prefix. |
| LOG-009 | PASS | 0 | Filtered replay matched filtered live bytes exactly. |
| RNS-001 | PASS | 0 | `TKN_ACT_KEEP_RUNS=2` pruned down to the newest `2`. |
| RNS-002 | PASS | 0 | `TKN_ACT_KEEP_RUNS=0` pruned `0` runs. |
| RNS-003 | PASS | 2 | `runs prune --all` without `-y` refused. |
| RNS-004 | PASS | 0 | `runs prune --all -y` wiped the store. |
| RNS-005 | PASS | 0 | Verified `--state-dir > TKN_ACT_STATE_DIR > XDG_DATA_HOME`. |
| RNS-006 | PIN | 0 | Read-only state dir warns and runs without persistence. |
| RNS-X01 | PIN | 0 | `runs bogus` printed group help and exited `0`. |

### Area J — output/filters

| Spec | Result | Exit | Note |
|---|---|---:|---|
| OUT-001 | PASS | 0 | Pretty and JSON agreed on hello success; pretty used `task/step` prefixes. |
| OUT-002 | PASS | 0 | `-q` suppressed header and step logs. |
| OUT-003 | PASS | 0 | `-v` added `▸ greet`. |
| OUT-004 | PASS | 0 | Pretty lines were prefixed with `[HH:MM:SS.mmm]`. |
| OUT-005 | PASS | 0 | `--color=always` wrote ANSI escapes to a file. |
| OUT-006 | PASS | 0 | `--color=always` beat `NO_COLOR=1`. |
| OUT-007 | PIN | 2 | `--quiet` and `--verbose` are mutually exclusive. |
| OUT-008 | PASS | 0 | 200KB line/CR/no-newline case completed; JSON output stayed parseable. |
| FLT-001 | PASS | 0 | `--task alpha` passed only `alpha` plus run-boundary events. |
| FLT-002 | PIN | 0 | `--task ghost` yielded only `run-start`/`run-end`. |
| FLT-003 | PASS | 0 | `--task alpha --step world` applied AND semantics. |

### Area M — abuse/env

| Spec | Result | Exit | Note |
|---|---|---:|---|
| SIG-001 | BLOCKED | - | Signal case intentionally skipped in this batch. |
| SIG-002 | BLOCKED | - | Signal case intentionally skipped in this batch. |
| SIG-003 | BLOCKED | - | Signal case intentionally skipped in this batch. |
| ABU-001 | PASS | 4 | 50MB YAML rejected without panic. |
| ABU-002 | PASS | 4 | Alias bomb rejected without hanging. |
| ABU-003 | PIN | 0 | Duplicate keys are accepted; later key wins. |
| ABU-004 | PASS | 0 | BOM + CRLF valid pipeline parsed successfully. |
| ABU-005 | PASS | 0 | 1000-task skip-heavy pipeline validated and ran deterministically. |
| ABU-006 | PASS | 0 | Unicode/emoji task/param names validated cleanly. |
| ENV-001 | PASS | 0 | `HOME` unset still ran hello successfully. |
| ENV-002 | PASS | 3 | `XDG_CACHE_HOME` file caused `cache_dir` check failure. |
| CNC-001 | BLOCKED | - | Timing-sensitive concurrency case intentionally skipped. |
| CNC-002 | BLOCKED | - | Timing-sensitive concurrency case intentionally skipped. |

## Defects

### 1. Usage-style CLI errors exit `1` instead of documented usage `2`

- Severity/Priority: `S2 / P1`
- Affected: `CLI-004`, `CLI-005`, `CLI-006`, `CCH-005`; also reproduced on exploratory `run --bogus`.
- Repro:
  - `bin/tkn-act --frobnicate`
  - `bin/tkn-act nonexistent-subcommand`
  - `bin/tkn-act run --param`
  - `bin/tkn-act cache prune --older-than banana`
- Expected: usage/flag parsing errors exit `2`.
- Actual:
  - `--frobnicate` -> exit `1`, stderr `unknown flag: --frobnicate`
  - `nonexistent-subcommand` -> exit `1`, stderr `unknown command "nonexistent-subcommand" for "tkn-act"`
  - `run --param` -> exit `1`, stderr `flag needs an argument: --param`
  - `cache prune --older-than banana` -> exit `1`, stderr `invalid argument "banana" ...`
- Root-cause guess: Cobra parse/dispatch errors are being surfaced through the generic exit mapper instead of the contract’s usage exit path.

### 2. Unknown subcommands under command groups print help and exit `0`

- Severity/Priority: `S3 / P1`
- Affected exploratory checks: `cluster bogus`, `cache bogus`, `runs bogus`.
- Repro:
  - `bin/tkn-act cluster bogus`
  - `bin/tkn-act cache bogus`
  - `bin/tkn-act runs bogus`
- Expected: unknown subcommand error and non-zero exit.
- Actual: each command printed group help and exited `0`.
- Root-cause guess: command-group parents likely have a help-printing default `Run` path that returns `nil` when child resolution fails.

### 3. Invalid `--output` values are silently ignored on leaf commands

- Severity/Priority: `S3 / P2`
- Affected exploratory checks: `CLI-010`, `DOC-X01`, `VAL-X01`, `LST-X01`.
- Repro:
  - `bin/tkn-act version --output garbage`
  - `bin/tkn-act doctor --output garbage`
  - `bin/tkn-act validate -f testdata/e2e/hello/pipeline.yaml --output garbage`
  - `cd testdata/e2e/hello && ../../../bin/tkn-act list --output garbage`
- Expected: usage error `2` for unknown output mode.
- Actual: each command fell back to pretty/human output and exited `0`.
- Root-cause guess: output mode validation is skipped on several leaf commands and only the JSON-vs-pretty branch is selected.

### 4. `agent-guide --list` does not match on-disk section basenames

- Severity/Priority: `S4 / P2`
- Affected: `CLI-007`
- Repro: `bin/tkn-act agent-guide --list`
- Expected: list matches `docs/agent-guide/*.md` basenames.
- Actual: output included synthetic `overview` and omitted `README`.
- Root-cause guess: `--list` is derived from embedded ordering metadata instead of the docs tree contract the regression spec expects.

### 5. `validate -o json` rejection path is not actually JSON for unsupported apiVersion

- Severity/Priority: `S3 / P1`
- Affected: `VAL-002`
- Repro: `bin/tkn-act validate -f /tmp/.../val-002.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: tekton.dev/v99
  kind: Task
  metadata: {name: greet}
  spec:
    steps:
      - name: hi
        image: alpine:3
        script: echo hi
  ```
- Expected: exit `4` with JSON body containing non-empty `errors[]`.
- Actual: exit `4`; stdout empty; stderr `unsupported apiVersion "tekton.dev/v99" ...`
- Root-cause guess: validate’s fatal parse/schema error path bypasses the JSON formatter.

### 6. Duplicate PipelineTask names are accepted by both `validate` and `run`

- Severity/Priority: `S2 / P0`
- Affected: `VAL-003`, `VAL-017`
- Repro:
  - `bin/tkn-act validate -f /tmp/.../val-003.yaml -o json`
  - `bin/tkn-act run -f /tmp/.../val-003.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: tekton.dev/v1
  kind: Task
  metadata: {name: greet}
  spec:
    steps:
      - name: hi
        image: alpine:3
        script: echo hi
  ---
  apiVersion: tekton.dev/v1
  kind: Pipeline
  metadata: {name: dup}
  spec:
    tasks:
      - name: build
        taskRef: {name: greet}
      - name: build
        taskRef: {name: greet}
  ```
- Expected: exit `4` with duplicate-name error.
- Actual: `validate` exited `0` with `{"ok":true,"pipeline":"dup","errors":[]}`; `run` exited `0` and executed the pipeline.
- Root-cause guess: duplicate task names are being collapsed or ignored during DAG/load construction instead of being validated.

### 7. Unknown task-result references validate green and only fail at runtime

- Severity/Priority: `S2 / P1`
- Affected: `VAL-007`, `VAL-017`
- Repro:
  - `bin/tkn-act validate -f /tmp/.../val-007.yaml -o json`
  - `bin/tkn-act run -f /tmp/.../val-007.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: tekton.dev/v1
  kind: Task
  metadata: {name: greet}
  spec:
    params:
      - name: v
        type: string
    steps:
      - name: hi
        image: alpine:3
        script: echo $(params.v)
  ---
  apiVersion: tekton.dev/v1
  kind: Pipeline
  metadata: {name: bad-result-ref}
  spec:
    tasks:
      - name: greet
        taskRef: {name: greet}
        params:
          - name: v
            value: $(tasks.nope.results.x)
  ```
- Expected: validate-time exit `4`.
- Actual: `validate` exited `0`; `run` exited `5` with task message `no results for task "nope"`.
- Root-cause guess: validator is not traversing `$(tasks.*.results.*)` references inside PipelineTask params.

### 8. Empty files take the wrong validation path

- Severity/Priority: `S3 / P1`
- Affected: `VAL-013`, `VAL-017`
- Repro:
  - `bin/tkn-act validate -f /tmp/.../val-013-empty.yaml`
  - `bin/tkn-act run -f /tmp/.../val-013-empty.yaml -o json`
- Expected: parse/validate exit `4`.
- Actual:
  - `validate` -> exit `2`, stderr `multiple pipelines loaded; specify -p`
  - `run` -> exit `2`, stderr `no Pipeline found in loaded files`
- Root-cause guess: explicit file load is falling through pipeline-selection/discovery code instead of being treated as a parse/semantic validation failure.

### 9. `list -o json` in an empty directory exits `2` instead of returning empty arrays

- Severity/Priority: `S3 / P2`
- Affected: `LST-002`
- Repro: in an empty temp dir, run `bin/tkn-act list -o json`
- Expected: exit `0` with `{"pipelines":[],"tasks":[]}`.
- Actual: exit `2`, stderr `no tekton YAML found in . ...`
- Root-cause guess: the discoverer treats “found nothing” as usage error instead of a valid empty result.

### 10. Missing image failure never emits a task-level terminal event

- Severity/Priority: `S3 / P2`
- Affected: `RUN-017`
- Repro: `bin/tkn-act run -f /tmp/.../missing-image.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: tekton.dev/v1
  kind: Task
  metadata: {name: badimg}
  spec:
    steps:
      - name: bad
        image: does-not-exist:nope
        script: echo nope
  ---
  apiVersion: tekton.dev/v1
  kind: Pipeline
  metadata: {name: badimg}
  spec:
    tasks:
      - name: t
        taskRef: {name: badimg}
  ```
- Expected: exit `5` with `task-end status:"infrafailed"`.
- Actual: exit `5`; stderr pull error; JSON contained only `run-start` then `run-end status:"failed"`.
- Root-cause guess: image-pull failures are short-circuiting the task event emitter.

### 11. Missing binary is classified as `infrafailed`, not `failed`

- Severity/Priority: `S3 / P2`
- Affected: `RUN-018`
- Repro: `bin/tkn-act run -f /tmp/.../missing-bin.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: tekton.dev/v1
  kind: Task
  metadata: {name: badbin}
  spec:
    steps:
      - name: bad
        image: alpine:3
        command: [/no/such/bin]
  ---
  apiVersion: tekton.dev/v1
  kind: Pipeline
  metadata: {name: badbin}
  spec:
    tasks:
      - name: t
        taskRef: {name: badbin}
  ```
- Expected: task-level `status:"failed"`.
- Actual: exit `5`; task ended `status:"infrafailed"` with OCI runtime exec error.
- Root-cause guess: backend treats container-start exec errors as infrastructure failures rather than step/task failures.

### 12. Shipped `testdata/e2e/volumes` fixture is not self-sufficient

- Severity/Priority: `S2 / P1`
- Affected: `VOL-001`
- Repro: `bin/tkn-act run -f testdata/e2e/volumes/pipeline.yaml -o json`
- Expected: exit `0`; inline configMap + emptyDir readable.
- Actual: exit `5`; task `infrafailed` with `source "app-config" has no keys`.
- Root-cause guess: the shipped fixture/spec assumes out-of-band `--configmap` or on-disk configmap state that the regression command does not provide.

### 13. Secret key path traversal is not rejected

- Severity/Priority: `S2 / P1`
- Affected: `VOL-007`
- Repro: `bin/tkn-act run -f /tmp/.../vol7.yaml -o json`
- Minimal fixture:
  ```yaml
  apiVersion: v1
  kind: Secret
  metadata:
    name: escape-secret
  type: Opaque
  stringData:
    ../escape: nope
  ---
  apiVersion: tekton.dev/v1
  kind: Task
  metadata: {name: reads}
  spec:
    volumes:
      - name: app-secret
        secret:
          secretName: escape-secret
    steps:
      - name: read
        image: alpine:3
        volumeMounts:
          - { name: app-secret, mountPath: /etc/app-secret }
        script: ls -la /etc/app-secret
  ---
  apiVersion: tekton.dev/v1
  kind: Pipeline
  metadata: {name: reads}
  spec:
    tasks:
      - name: t
        taskRef: {name: reads}
  ```
- Expected: validation/load rejection with exit `4`.
- Actual: exit `0`; mount directory was empty; run succeeded.
- Root-cause guess: unsafe keys are silently dropped or normalized instead of causing a hard validation failure.

### 14. `runs list` order is oldest-first, not newest-first

- Severity/Priority: `S4 / P2`
- Affected: `LOG-002`
- Repro: after three runs, `bin/tkn-act runs list`
- Expected: newest-first ordering.
- Actual:
  ```text
  1 hello
  2 hello
  3 failprop
  ```
  Older runs appear first.
- Root-cause guess: list rendering iterates ascending sequence/index order.

### 15. `logs 0` is interpreted as a bad sequence number, not an ambiguous ULID prefix

- Severity/Priority: `S4 / P2`
- Affected: `LOG-008`
- Repro: with multiple runs present, `bin/tkn-act logs 0`
- Expected: ambiguous-prefix error.
- Actual: exit `2`, stderr `run sequence number must be positive (got 0): no matching run`.
- Root-cause guess: numeric parsing takes precedence over ULID-prefix lookup, even for ambiguous short prefixes.

## PIN decisions

- `CLI-010`: invalid output on `version` falls back to pretty (`tkn-act dev`), exit `0`.
- `DOC-X01`: invalid output on `doctor` falls back to pretty, exit `0`.
- `VAL-012`: missing file exits `4` with file-open error.
- `VAL-015`: `validate -f -` is not stdin; it treats `-` as a path.
- `VAL-X01`: invalid output on `validate` falls back to pretty `ok`, exit `0`.
- `LST-005`: nonexistent dir exits `2` with `no tekton YAML found`.
- `LST-006`: invalid discovered `pipeline.yaml` exits `4`.
- `LST-X01`: invalid output on `list` falls back to pretty, exit `0`.
- `RUN-013`: empty `--param k=` is accepted.
- `RUN-015`: duplicate params are last-wins.
- `RUN-016`: missing required param fails at run-time with exit `5`.
- `RUN-020a`: `--max-parallel 0` is accepted.
- `RUN-020b`: `--max-parallel -1` is accepted.
- `MX-004`: empty matrix param list is rejected with exit `4`.
- `CCH-003`: empty-cache prune with `0s` returns `pruned: 0`.
- `CCH-004`: empty-cache prune with `9999h` returns `pruned: 0`.
- `CCH-007`: empty-cache clear returns `cleared: 0`.
- `RNS-006`: read-only state dir warns and runs without persistence.
- `OUT-007`: `--quiet` and `--verbose` are mutually exclusive, exit `2`.
- `FLT-002`: nonexistent task filter produces only `run-start`/`run-end`, exit `0`.
- `ABU-003`: duplicate YAML keys are accepted; later key wins.

## Blocked

- `RES-006`..`RES-012`: live local git/http/OCI happy-path resolver setup was explicitly out of scope for this batch.
- `E2E-resolver-bundles`, `E2E-resolver-git`, `E2E-resolver-http`, `E2E-resolver-hub`: same live-server restriction as above.
- `CCH-001`: no in-scope successful resolver run was allowed to seed cache entries.
- `SIG-001`..`SIG-003`: signal teardown cases intentionally skipped.
- `CNC-001`..`CNC-002`: timing-sensitive concurrency cases intentionally skipped.
- Area `K` (`CLS-001`..`CLS-008`): cluster backend excluded from this run by scope.
- Area `L` (`RMT-001`..`RMT-004`): remote-docker backend excluded from this run by scope.
