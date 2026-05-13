# Remote docker daemon support — Implementation Plan

> **Spec:** [`docs/superpowers/specs/2026-05-13-remote-docker-design.md`](../specs/2026-05-13-remote-docker-design.md)

**Goal:** Let `tkn-act run --docker` work against a remote dockerd
reached via `DOCKER_HOST=ssh://...` or `tcp://...`. All 28 e2e
fixtures pass with identical JSON event shape regardless of whether
the daemon is local or remote. CI runs the remote path on every PR
via a `docker:dind` service container — no external server, no
gated secret.

**Architecture:** auto-detect remote daemon at `Backend` construction
(or force via `--remote-docker={auto,on,off}`). When remote, swap
the bind-mount staging path for a per-Task docker volume + transient
"stager" container that `docker cp` inputs in and results out. Local
daemons keep the existing fast bind-mount path unchanged.

**Tech stack:** Go 1.25, one new direct dependency
(`golang.org/x/crypto/ssh` — already transitive via go-git). Reuses
the moby SDK `client.WithDialContext` hook, the existing
`internal/e2e/fixtures` table, and the four CI gates.

---

## Pre-flight (zero-cost sanity)

- [ ] `git status` clean on `feat/remote-docker-backend`.
- [ ] `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...` green on the branch head.
- [ ] `go test -race -count=1 ./...` green on the branch head.

---

## Phase 1 — SSH dialer + `ssh://` URL parsing

**Files:** `internal/backend/docker/sshdial.go` (new),
`internal/backend/docker/sshdial_test.go` (new),
`internal/backend/docker/docker.go` (wire the dialer into `New`).

- [ ] Add a dialer:
      ```go
      func newSSHDialer(host string) (func(context.Context, string, string) (net.Conn, error), error)
      ```
      Parses `ssh://[user@]host[:port]`. Reads `SSH_AUTH_SOCK` for agent
      auth; falls back to `~/.ssh/id_ed25519`, `id_rsa`. Loads
      `~/.ssh/known_hosts` unless `TKN_ACT_SSH_INSECURE=1`.
- [ ] Connects to the daemon's unix socket on the remote
      (`/var/run/docker.sock` by default; overridable via
      `TKN_ACT_DOCKER_SOCKET`).
- [ ] In `docker.New`, detect the `ssh://` scheme on `DOCKER_HOST`
      (or the new `--docker-host` value, which Phase 4 plumbs in;
      Phase 1 keeps it env-only) and pass the dialer via
      `client.WithDialContext(...)`.
- [ ] Unit test against a stub SSH server (`golang.org/x/crypto/ssh`
      provides server types) that accepts a publickey and returns a
      canned response on `/var/run/docker.sock`. Asserts the dialer
      handshakes correctly and the moby SDK can talk through it.
- [ ] Unit test asserting password auth is **not** attempted (we
      only do publickey) and that a missing key produces an actionable
      error mentioning `ssh-add`.

**Gate:** `go test ./internal/backend/docker/... -run 'TestSSH'` green.

---

## Phase 2 — Remote detection + `--remote-docker` flag

**Files:** `internal/backend/docker/docker.go`,
`cmd/tkn-act/run.go`.

- [ ] In `Backend.New`, after the moby client connects, call
      `cli.Info(ctx)`. Compute `remote := info.Name != os.Hostname() && !isUnixSocket(host)`.
      Store on the struct: `b.remote bool`.
- [ ] In `cmd/tkn-act/run.go`, add the flag:
      ```go
      var remoteDocker string  // "auto" | "on" | "off"
      cmd.Flags().StringVar(&remoteDocker, "remote-docker", "auto", ...)
      ```
      Resolution precedence (highest first): `--remote-docker`,
      `TKN_ACT_REMOTE_DOCKER`, auto-detection.
- [ ] When `remoteDocker=="on"`, force `b.remote=true` even if the
      daemon is local. When `"off"`, force `false`. Used by the dind
      integration workflow (which is technically the same kernel as
      the runner but we want to exercise the volume path).
- [ ] Unit test `TestResolveRemoteMode` covering the three values +
      env var precedence. Same shape as `cluster_test.go:TestResolveTektonVersion`.

**Gate:** new tests green; existing tests untouched.

---

## Phase 3 — Per-Task volume staging (Step path)

**Files:** `internal/backend/docker/staging.go` (new — extracted
helper),
`internal/backend/docker/docker.go` (call sites at lines 302-370),
`internal/backend/docker/staging_test.go`.

- [ ] New helper `Backend.stage(ctx, runID, taskName, items []stageItem) (volumeName string, err error)`:
      - `items` are `(localPath, key)` pairs.
      - Creates a volume `tkn-act-<runID>-<taskName>` with labels
        `tkn-act.run=<runID>` and `tkn-act.task=<taskName>` if not
        present.
      - Spawns a stager container with the volume mounted at
        `/staged` and command `sleep infinity`.
      - `cli.CopyToContainer(stagerID, "/staged", tar(items))` —
        bundle all items into one tar stream and ship in a single
        round-trip.
      - Caches the stager ID on the `Backend` keyed by `taskName`
        so subsequent stage calls within the same Task reuse it.
- [ ] At the Step container build site in `docker.go:302-370`, when
      `b.remote == true`:
      - Replace `mount.Mount{Type: Bind, Source: script.sh path, ...}`
        with `mount.Mount{Type: Volume, Source: vol, Target: "/tekton/scripts/script.sh"}`.
      - Same for workspace mounts (still backed by the same volume).
      - Same for results dir, step-results dirs, volume sources
        (configmap/secret).
- [ ] At Task end, before tearing the stager down, call
      `cli.CopyFromContainer(stagerID, "/staged/results")` to pull
      results back to `inv.ResultsHost`. Same for step-results dirs.
- [ ] Tear down: `cli.ContainerKill(stagerID, "KILL")` then
      `cli.VolumeRemove(volName)` after pulling outputs.
- [ ] Unit tests using moby's fake client: assert stager is reused,
      assert volume is labeled with `tkn-act.run`, assert tar
      contents match input bytes.

**Gate:** unit tests green; one fixture (`hello`) passes
end-to-end against a local daemon with `--remote-docker=on`.

---

## Phase 4 — Sidecars + `--docker-host` flag

**Files:** `internal/backend/docker/sidecars.go` (call sites at
lines 185-234), `cmd/tkn-act/run.go`.

- [ ] Sidecar mounts: same volume reuse as Step path. Sidecars live
      for the whole Task, so they mount the per-Task volume from the
      same `tkn-act-<runID>-<taskName>` name.
- [ ] Add `--docker-host` flag on `tkn-act run` (string, defaults
      to `""`). When set, overrides `DOCKER_HOST` for this invocation
      only (set via `os.Setenv` before `Backend.New`, restore in
      defer).
- [ ] Unit test asserting `--docker-host ssh://fake@host` is honored
      and the SSH dialer is reached.

**Gate:** unit tests green; `sidecars` fixture passes end-to-end
with `--remote-docker=on`.

---

## Phase 5 — Cross-backend fixture verification

### P5a — `docker:dind` integration workflow

**Files:** `.github/workflows/remote-docker-integration.yml` (new),
`internal/e2e/fixtures` consumer.

- [ ] New workflow `remote-docker-integration.yml`:
      ```yaml
      services:
        dind:
          image: docker:28-dind
          options: --privileged
          ports: ['2376:2376']
          env:
            DOCKER_TLS_CERTDIR: ''
      env:
        DOCKER_HOST: tcp://localhost:2376
        TKN_ACT_REMOTE_DOCKER: 'on'
      ```
- [ ] Job runs `go test -tags integration ./internal/dockere2e/...`
      across the full `fixtures.All()` table.
- [ ] Skip-list any fixture that genuinely cannot work remotely
      (none expected, but reserve the mechanism with a documented
      reason like the `DockerOnly` flag).
- [ ] Add a `docs/test-coverage.md` row for the new workflow.

### P5b — `ssh://` transport unit coverage

- [ ] Reuse Phase 1's stub SSH server in a higher-level test:
      one `Backend.New(...)` call against `ssh://test@127.0.0.1:<port>`
      asserts the moby SDK reaches the stub and round-trips a
      `cli.Ping` call. No e2e fixture needed at this layer — the
      dind workflow already covers volume staging.

**Gate:** workflow green on a probe PR; one fixture exercised in
the workflow.

---

## Phase 6 — Doc convergence + parity

**Files:**

- `docs/agent-guide/docker-backend.md` — new "Remote docker
  daemons" section: auto-detect behavior, `--remote-docker` flag,
  volume-staging trade-off, SSH key requirements.
- `docs/agent-guide/README.md` — bullet under "Operational flags"
  for `--remote-docker` + `--docker-host`.
- `README.md` — example using `DOCKER_HOST=ssh://...`.
- `docs/feature-parity.md` — header note "Docker backend supports
  remote daemons since v1.7." Bump `Last updated:`.
- `docs/test-coverage.md` — `remote-docker-integration` workflow
  row.
- `AGENTS.md` — public-contract-stability rows for `--remote-docker`,
  `--docker-host`, `TKN_ACT_REMOTE_DOCKER`, `TKN_ACT_SSH_INSECURE`.

- [ ] `go generate ./cmd/tkn-act/` — refresh embedded agent-guide tree.
- [ ] `make check-agentguide` (or `.github/scripts/check-agentguide.sh`) green.
- [ ] `.github/scripts/parity-check.sh` green.

**Gate:** all four CI gates (`tests-required`, `coverage`,
`parity-check`, `agentguide-freshness`) green on the implementation
PR.

---

## Out of band

- Image-sideloading for air-gapped remote daemons — separate spec.
- `docker context` parsing — separate spec.
- Multi-host scheduling — separate spec.

Each is referenced from this plan but does **not** block shipping
the remote-docker feature.
