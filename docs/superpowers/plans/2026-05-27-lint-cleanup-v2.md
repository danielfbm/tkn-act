# Lint Cleanup + golangci-lint v2 Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the linter to golangci-lint v2.12.2, fix all 58 v2 findings with zero behavior change, and add a blocking `lint` CI gate.

**Architecture:** One category-scoped commit per linter (config → gofmt → revive → staticcheck → errcheck → gate → docs), each verified by re-running golangci-lint and the full test suite. No logic or structural changes.

**Tech Stack:** Go 1.25, golangci-lint v2.12.2, GitHub Actions.

**Branch:** `refactor/lint-cleanup-v2` (already created; PR #54). Spec: `docs/superpowers/specs/2026-05-27-lint-cleanup-v2-design.md`.

**Working invariant for every task:** after the task's fix, `go build ./...` and `go test -race -count=1 ./<touched-packages>` stay green, and `golangci-lint run ./...` reports strictly fewer findings (0 by the end). No `//nolint`.

---

### Task 1: Migrate golangci-lint config to v2 + bump the pipeline pin

**Files:**
- Modify: `.golangci.yml` (replace entirely)
- Modify: `pipelines/tasks.yaml` (the `golangci-lint` Task: `golangci-lint-version` default + install path)

- [ ] **Step 1: Replace `.golangci.yml` with the v2-format config**

```yaml
version: "2"
linters:
  enable:
    - misspell
    - revive
  exclusions:
    generated: lax
    presets:
      - comments
      - common-false-positives
      - legacy
      - std-error-handling
    paths:
      - testdata
      - third_party$
      - builtin$
      - examples$
formatters:
  enable:
    - gofmt
    - goimports
  exclusions:
    generated: lax
    paths:
      - testdata
      - third_party$
      - builtin$
      - examples$
```

(`errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused` are default-on in v2; `gosimple` is folded into `staticcheck`. The effective set matches the old v1 config.)

- [ ] **Step 2: Bump the pin and fix the install path in `pipelines/tasks.yaml`**

In the `golangci-lint` Task, change the param default:
```yaml
    - name: golangci-lint-version
      type: string
      default: "v2.12.2"
```
and the install line in the `lint` step's script (note the new `/v2/` module-path segment):
```sh
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}" || INSTALL_OK=0
```

- [ ] **Step 3: Verify v2 reads the config and reports the known baseline**

Run: `golangci-lint run ./... 2>&1 | grep -cE '\.go:[0-9]+'`
Expected: `58` (config is valid v2; no `unsupported version` error).

- [ ] **Step 4: Commit**

```bash
git add .golangci.yml pipelines/tasks.yaml
git commit -m "build(lint): migrate golangci-lint config to v2 + pin v2.12.2"
```

---

### Task 2: Fix gofmt findings (3)

**Files (Modify):** `cmd/tkn-act/cache.go`, `cmd/tkn-act/jsonout_test.go`, `internal/e2e/fixtures/resolver_harness.go`

- [ ] **Step 1: Format the three files**

Run: `gofmt -w cmd/tkn-act/cache.go cmd/tkn-act/jsonout_test.go internal/e2e/fixtures/resolver_harness.go`

- [ ] **Step 2: Verify no gofmt findings remain**

Run: `golangci-lint run ./... 2>&1 | grep -c '(gofmt)'`
Expected: `0`

- [ ] **Step 3: Commit**

```bash
git add cmd/tkn-act/cache.go cmd/tkn-act/jsonout_test.go internal/e2e/fixtures/resolver_harness.go
git commit -m "style(lint): gofmt three files"
```

---

### Task 3: Fix revive findings (26)

Three rule types. **For each builtin-shadow rename, open the site, pick a non-builtin synonym, and update every reference in that identifier's scope.** Suggested names below; confirm against the actual code.

**3a. `redefines-builtin-id` (7 sites):**
- `internal/backend/cluster/run_test.go:297` — `cap` → `capN`
- `internal/cmdrunner/runner.go:20` — `real` → `realRunner` (likely a type/var named `real`; rename the declaration + all uses in the package)
- `internal/engine/engine.go:1092` — `max` → `maxVal`
- `internal/engine/step_action.go:132` — `copy` → `copied` (or use the builtin via a different local name)
- `internal/reporter/pretty.go:385` — `max` → `maxVal`
- `internal/reporter/reporter_test.go:352` — `cap` → `capN`
- `internal/reporter/reporter_test.go:378` — `cap` → `capN`

**3b. `unused-parameter` (18 sites) — rename the named parameter to `_`** (do NOT remove it; several are interface-method or cobra `RunE`/`Args` signatures):
- `cmd/tkn-act/doctor.go:46` (`cmd`), `cmd/tkn-act/list.go:32` (`c`), `cmd/tkn-act/runs.go:44` (`c`), `cmd/tkn-act/runs.go:109` (`c`)
- `internal/backend/backend_test.go:23` (`t`), `internal/backend/cluster/cluster.go:179` (`ctx`), `internal/backend/cluster/debug_test.go:57` (`t`), `internal/backend/cluster/run.go:499` (`childRefIdx`), `internal/backend/cluster/run_namespace_test.go:63` (`action`)
- `internal/backend/docker/debug_test.go:75` (`t`), `internal/backend/docker/docker.go:308` (`ctx`), `internal/backend/docker/sidecars.go:376` (`ctx`)
- `internal/cmdrunner/runner.go:88` (`stderr`)
- `internal/engine/engine.go:859` (`parent`), `internal/engine/lazy_resolve_test.go:246` (`req`)
- `internal/refresolver/remote_test.go:763` (`action`), `internal/refresolver/remote_test.go:796` (`action`)
- `internal/validator/validator.go:140` (`providedParams`)

**3c. `empty-block` (1 site):**
- `internal/engine/engine.go:107` — remove the empty block. If it is an empty `for range ch {}` drain loop, replace with `for range ch {}`'s intent explicitly (e.g. `//nolint`-free: keep draining via `for range ch { }` is itself the finding — instead use a `for range ch {}`→ confirm; if it is `if cond {}`, delete the dead `if`). Open the site and remove the empty `{}` body in the way that preserves current control flow.

- [ ] **Step 1:** Apply 3a renames (one identifier at a time; `go build ./...` after each to confirm scope).
- [ ] **Step 2:** Apply 3b parameter renames to `_`.
- [ ] **Step 3:** Apply 3c empty-block removal.
- [ ] **Step 4: Verify**

Run: `golangci-lint run ./... 2>&1 | grep -c '(revive)'`  → Expected: `0`
Run: `go build ./... && go test -race -count=1 ./internal/engine/... ./internal/reporter/... ./internal/cmdrunner/... ./cmd/tkn-act/...`  → Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(lint): resolve revive findings (unused params, builtin shadows, empty block)"
```

---

### Task 4: Fix staticcheck findings (8)

**4a. ST1005 — error strings should not be capitalized (3):** lowercase the first word of the message (keep proper nouns).
- `internal/engine/step_action.go:98`
- `internal/loader/loader.go:239`
- `internal/loader/loader.go:242`

**4b. SA1019 — `tar.TypeRegA` deprecated (2):** replace `tar.TypeRegA` with `tar.TypeReg`.
- `internal/backend/docker/staging_test.go:202`
- `internal/refresolver/bundles.go:243`

**4c. SA4006 — value of `repo` never used (2):** in `internal/refresolver/git.go:231` and `:238`, the assignment to `repo` is dead. Open the function: either consume `repo` (if the later code should use it) or drop the assignment. **Preserve behavior** — confirm whether the subsequent clone/checkout was meant to use `repo`; if the value is genuinely unused, remove the assignment (assign to `_` only if the RHS has needed side effects).

**4d. QF1003 — tagged switch (1):** `internal/reporter/pretty.go:164` — convert the `if/else if` chain on `e.Status` into a `switch e.Status { case ...: }`.

- [ ] **Step 1:** Apply 4a, 4b, 4d (mechanical).
- [ ] **Step 2:** Apply 4c after reading the `git.go` clone path; preserve behavior.
- [ ] **Step 3: Verify**

Run: `golangci-lint run ./... 2>&1 | grep -c '(staticcheck)'`  → Expected: `0`
Run: `go test -race -count=1 ./internal/loader/... ./internal/reporter/... ./internal/refresolver/... ./internal/engine/...`  → Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(lint): resolve staticcheck findings (ST1005/SA1019/SA4006/QF1003)"
```

---

### Task 5: Fix errcheck findings (21)

**20 are in tests** — assign the result to `_ =` (or, where the value is needed, check it with `if err != nil { t.Fatal(err) }`). The single **production** site gets real handling.

**5a. Production (1):** `internal/reporter/pretty.go:317` — `p.w.Write(...)` return ignored. In a `Reporter` write path, propagate or record the error per how sibling writes in `pretty.go` handle theirs (match the existing pattern in the file — if other `p.w.Write` calls track a sticky `p.err`, do the same; otherwise return it).

**5b. Tests (20):** assign to `_ =` (teardown/`Finalize`/`Append`/`NewRun`) or `if err != nil { t.Fatalf(...) }` for `json.Unmarshal`:
- `cmd/tkn-act/persistence_test.go:259`
- `cmd/tkn-act/runs_test.go:25,27,89,93,95,106,107,160,269`
- `internal/reporter/persistsink_test.go:60` (`json.Unmarshal` → check with `t.Fatal`)
- `internal/runstore/gc_test.go:65,67,189,213`
- `internal/runstore/index_test.go:55,56,57`
- `internal/runstore/store_test.go:118,146`

(For the `json.Unmarshal` sites at `runs_test.go:95,107` and `persistsink_test.go:60`, prefer `if err := json.Unmarshal(...); err != nil { t.Fatalf("unmarshal: %v", err) }` — checking is cheap and strengthens the test.)

- [ ] **Step 1:** Fix the production site (5a) matching the file's existing error-tracking pattern.
- [ ] **Step 2:** Fix the 20 test sites (5b).
- [ ] **Step 3: Verify**

Run: `golangci-lint run ./... 2>&1 | grep -c '(errcheck)'`  → Expected: `0`
Run: `golangci-lint run ./... 2>&1 | grep -cE '\.go:[0-9]+'`  → Expected: `0`  (all 58 cleared)
Run: `go test -race -count=1 ./cmd/tkn-act/... ./internal/runstore/... ./internal/reporter/...`  → Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(lint): check error returns (errcheck) — 1 prod, 20 test sites"
```

---

### Task 6: Add the blocking `lint` CI job

**Files (Modify):** `.github/workflows/ci.yml`

- [ ] **Step 1: Add a `lint` job** after the `build-and-test` job:

```yaml
  lint:
    name: lint
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: true
      - name: install golangci-lint
        run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
      - name: golangci-lint run
        run: golangci-lint run ./...
```

(Direct `go install`, no third-party action — consistent with the repo's `actions/*`-only posture. `golangci-lint` exits non-zero on any finding, failing the job.)

- [ ] **Step 2: Validate the workflow parses**

Run: `yq eval '.jobs | keys' .github/workflows/ci.yml`
Expected: list includes `lint`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci(lint): add blocking golangci-lint v2 gate"
```

---

### Task 7: Doc convergence + final verification

**Files (Modify):** `docs/test-coverage.md`, `AGENTS.md`

- [ ] **Step 1: `docs/test-coverage.md`** — add the new blocking `lint` job to the workflow inventory/table (default test set, no docker dependency; golangci-lint v2.12.2) and note the v1→v2 migration. Keep consistent with how the doc lists the other `ci.yml` jobs.

- [ ] **Step 2: `AGENTS.md`** (Local development section) — update the pre-PR command block and any golangci-lint version reference to v2: contributors run `golangci-lint run ./...` (v2.12.2) and it is now a blocking gate.

- [ ] **Step 3: Full local gate run (the spec's verification contract)**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go test -race -count=1 ./...
golangci-lint run ./...
```
Expected: vet clean, tests PASS, golangci-lint reports **0** issues.

- [ ] **Step 4: Commit**

```bash
git add docs/test-coverage.md AGENTS.md
git commit -m "docs(lint): document v2 migration + blocking lint gate"
```

- [ ] **Step 5: Push**

```bash
git push
```
Then confirm PR #54's checks — the new `lint` job plus the existing gates — go green.

---

## Self-review notes

- **Spec coverage:** A=Task 1; B=Tasks 2–5 (gofmt/revive/staticcheck/errcheck, all 58 sites enumerated); C=Task 6; doc-rule updates=Task 7. All spec sections mapped.
- **Placeholder scan:** every finding has an exact `file:line`; builtin-shadow renames give a concrete suggested name with a "confirm scope" instruction (genuine per-site judgement, not a placeholder).
- **Behavior preservation:** SA4006 (`git.go`) and the production errcheck (`pretty.go:317`) are the only judgement sites; both carry explicit "preserve behavior / match existing pattern" guidance and are covered by the `-race` suite staying green.
