# Plan: QA round 3 — backend event emission + signal cancellation

Fixes two QA-pass defects whose root causes touch engine/backend internals and
(for #67) the public status contract. Implemented test-first.

## #62 — image-pull failure emits no task-start/task-end event

**Problem.** `Engine.RunPipeline` calls `backend.Prepare` (`internal/engine/engine.go:158`)
once per run, before the task loop. The docker backend's `Prepare`
(`internal/backend/docker/docker.go:295-299`) eager-pulls **every** image and
returns the first pull error, which aborts the whole run before any
`task-start`/`task-end` is emitted — the JSON stream shows only `run-start`
then `run-end status:"failed"`, so an agent can't attribute the failure.

**Fix.** Make `Prepare`'s pre-pull **best-effort** (pull as an optimization,
ignore errors). RunTask's per-step `ensureImage` (`docker.go:412`) is already
the authoritative pull: on failure it sets `res.Status = TaskInfraFailed` and
returns `(res, nil)`. With Prepare no longer aborting, the engine's normal
wrapping emits `task-start` (engine.go:264) → runs the task → `task-end` with
`status:"infrafailed"` (engine.go:290). No new event plumbing needed.

**Test (integration, real docker).** Add a case in
`internal/backend/docker/docker_integration_test.go` (or a small e2e fixture):
a pipeline with an unpullable image (`does-not-exist.invalid/nope:nope`) must
produce a `task-start` AND a terminal `task-end` with `status:"infrafailed"`,
and the run exits 5. Runs under `-tags integration` (docker-integration.yml).

**Risk.** A transient pull error for a *good* image is no longer caught in
Prepare — but RunTask re-pulls per task and surfaces it with proper attribution,
which is strictly better. The pre-pull remains as a warm-the-cache optimization.

## #67 — SIGINT/SIGTERM classified as timeout (exit 6) not cancelled (exit 130)

**Problem.** `run.go:245` builds the run `ctx` via `signal.NotifyContext`, so the
root ctx is cancelled **only** by a signal (the pipeline/tasks/finally timeout
budgets are *child* contexts via `withMaybeBudget`). On signal, `RunPipeline`
returns `err==nil` with `res.Status=="timeout"`, so `run.go`'s `res.Status`
switch (line 307) maps it to exit 6 and the run-end event says `status:"timeout"`
with message `pipeline "X" timeout` — both wrong (no timeout was declared).

**Fix.**
1. Engine: after computing the overall status, if `errors.Is(ctx.Err(),
   context.Canceled)` (signal — the root ctx is never given a deadline), set
   the run overall status to the new value `"cancelled"`. This is additive to
   the status contract.
2. `run.go`: map `res.Status == "cancelled"` → `exitcode.Cancelled` (130).
   (The existing `err != nil && ctx.Err() != nil` path at run.go:295 already
   returns Cancelled; this covers the `err==nil` + timeout-status path.)

**Test (unit).** `internal/engine`: with a fake backend that blocks until ctx
is done, cancel the run ctx mid-flight and assert `RunResult.Status ==
"cancelled"` and the terminal `run-end` event carries `status:"cancelled"`
(not `"timeout"`). Plus a `cmd/tkn-act` assertion that a `"cancelled"` run
status maps to exit 130 if reachable via the existing exit-test harness.

**Risk.** Must not regress real timeouts: a declared pipeline/tasks/finally
timeout cancels a *child* budget ctx (DeadlineExceeded), leaving the root
`ctx.Err()==nil`, so those still classify as `"timeout"`/exit 6. The existing
timeout fixtures (`timeout`, `pipeline-timeout`, `tasks-timeout`,
`finally-timeout`) must stay green — this is the key regression guard.

## Doc convergence

- `docs/agent-guide/` — add `"cancelled"` to the documented run/task status
  list (the `run -o json` section), then `go generate ./cmd/tkn-act/` (or
  `make agentguide`) so the embedded copy matches (agentguide-freshness gate).
- `docs/qa/regression-suite.md` — flip the `SIG-001`/`SIG-002` annotations from
  "Currently FAILS" to fixed once #67 lands; add a `RUN-0xx` for #62's
  task-event-on-infra-failure.
- Exit code 130 is already documented; no exit-code table change.

## Out of scope (kept for later)

- Re-classifying the *task-level* status of the interrupted task (currently
  `infrafailed` / "context canceled") to `"cancelled"` — the run-level fix is
  what the contract (exit 130) needs; task-level can follow if desired.
- Double-Ctrl-C / SIGKILL escalation (SIG-003).
