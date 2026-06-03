# tkn-act QA findings log

Append-only. One section per execution pass. Each pass records the
environment, the per-spec result table (or a pointer to it), the exploratory
session notes, and the GitHub issues opened for confirmed defects.

The newest pass goes at the top.

---

## Pass template (copy for each run)

```
## Pass YYYY-MM-DD — <runner / who>

### Environment
- tkn-act: <version> (commit <sha>)
- OS/arch: <darwin|linux>/<arm64|amd64>
- doctor: ok=<bool>, docker=<detail>, k3d=<detail>, kubectl=<detail>
- Scratch state-dir / cache: <paths>
- Tags exercised: DOCKER / CLUSTER / NET / SIG

### Regression results
| Spec | Result | Issue |
|---|---|---|
| CLI-001 | PASS | |
| ... | | |

### Exploratory session notes
- Charter A1: <observations>
- ...

### Issues filed this pass
- #<n> [S?-…/P?] <title>
```

---

<!-- passes appended below -->

## Pass 2026-06-03 — Claude (offline smoke) + codex (docker batch, in progress)

### Environment
- tkn-act: dev (commit `0c16f88`, built from branch `qa/exploratory-and-regression-tests`)
- OS/arch: darwin/arm64
- doctor: ok=true, docker=API 1.54, k3d=v5.8.3, kubectl=v1.36.0
- Scratch state-dir / cache: per-invocation `$(mktemp -d)`
- Tags exercised: offline + DOCKER (CLUSTER / SIG / NET deferred to a later batch)

### Regression results (Claude offline smoke — area A/C/D)
| Spec | Result | Exit | Note |
|---|---|---|---|
| CLI-001 | PASS | 0 | `{"name":"tkn-act","version":"dev"}` |
| CLI-002 | PASS | 0 | exit_codes == {0,1,2,3,4,5,6,130} |
| CLI-004 | **FAIL** | 1 | unknown flag exits 1, contract says 2 → **#55** |
| CLI-005 | PIN→FAIL | 1 | top-level unknown command exits 1 (subcommand groups exit 0 → **#56**) |
| CLI-006 | **FAIL** | 1 | `run --param` (needs arg) exits 1, should be 2 → **#55** |
| CLI-007 | PASS | 0 | 13 sections, 1:1 with `docs/agent-guide/*.md` |
| CLI-009 | PASS | 2 | unknown section → exit 2 + valid-list message (good) |
| CLI-010 | **FAIL** | 0 | `version --output garbage` ignored, exit 0 → **#57** |
| VAL-001 | PASS | 0 | `{"ok":true,"pipeline":"hello","errors":[]}` |
| VAL-012 | PIN | 4 | missing `-f` file → exit 4 (validate), not 2; deterministic |
| VAL-014 | PASS | 4 | non-Tekton YAML → exit 4 (message leaks Go type name — minor) |
| LST-002 | PIN | 2 | `list` in empty dir → exit 2 "no tekton YAML found" (not exit-0-empty) |
| CCH-005 | **FAIL** | 1 | `cache prune --older-than banana` exits 1, should be 2 → **#55** |

### Issues filed this pass
- **#55** [S2-major / P1] Usage errors from cobra/pflag exit 1, not the documented 2.
- **#56** [S2-major / P1] Unknown subcommand of cluster/cache/runs exits 0 (prints help) instead of erroring.
- **#57** [S3-minor / P2] Invalid `--output` value silently ignored (falls back to pretty, exit 0).

### Codex batch (complete)
`codex exec` (gpt-5.4) executed areas A/B/C/D/E/F/H/I/J (+ G allow-list/offline,
M offline abuse) against the local docker daemon. Full per-spec table in
[`exec-results-codex.md`](exec-results-codex.md). It independently re-confirmed
#55–#57 and surfaced 12 more candidates; each was triaged and the real ones
re-verified by hand before filing.

**Headline counts:** ~95 specs executed — most PASS; resolver happy-paths
(RES-006..012), signals (SIG-*), concurrency (CNC-*), cluster (K) and
remote-docker (L) BLOCKED/out-of-scope for this batch.

**Triage of codex's 12 new candidates → 8 filed, 3 spec false-positives, 1 folded:**

| Codex # | Verdict | Issue / action |
|---|---|---|
| dup task names | confirmed | **#58** S2/P0 |
| unknown `$(tasks.X.results.Y)` ref not validated | confirmed | **#59** S2/P1 |
| `validate -o json` no JSON on fatal load errors | confirmed | **#60** S2/P1 |
| CM/Secret key `..` traversal accepted | confirmed | **#61** S2/P1 (security) |
| image-pull failure emits no task event | confirmed | **#62** S2/P1 |
| empty file → "multiple pipelines loaded" | confirmed | **#63** S3/P1 |
| `list -o json` empty dir exits 2 | confirmed | **#64** S3/P2 |
| `runs list` order vs docs | confirmed | **#65** S4/P3 |
| missing-binary = `infrafailed` not `failed` | judgment call | folded into **#62** notes |
| `agent-guide --list` shows `overview` not `README` | **spec false-positive** | curated mapping is intentional; spec CLI-007 corrected |
| `volumes` fixture "not self-sufficient" | **spec false-positive** | needs harness-seeded configMap; spec VOL-001 corrected |
| `logs 0` not treated as ambiguous prefix | **spec false-positive** | all-digit id = sequence by design; spec LOG-008 corrected |

### Notable PIN decisions recorded
`--max-parallel 0`/`-1` accepted (no clamp/reject); duplicate `--param` is
last-wins; empty `--param k=` accepted; `-q`+`-v` are mutually exclusive
(exit 2); read-only state dir warns and runs without persistence (RNS-006);
`validate -f -` treats `-` as a literal path (no stdin). These are
deterministic and documented here rather than filed.

### Issues filed this pass (total 11)
- **#55** [S2/P1] cobra/pflag usage errors exit 1 not 2.
- **#56** [S2/P1] unknown subcommand of cluster/cache/runs exits 0.
- **#57** [S3/P2] invalid `--output` silently ignored.
- **#58** [S2/P0] duplicate PipelineTask names accepted.
- **#59** [S2/P1] unknown result-ref not validated.
- **#60** [S2/P1] `validate -o json` no JSON on fatal load errors.
- **#61** [S2/P1] CM/Secret key `..` traversal accepted (security).
- **#62** [S2/P1] image-pull failure emits no task event.
- **#63** [S3/P1] empty file → misleading "multiple pipelines loaded".
- **#64** [S3/P2] `list -o json` empty dir exits 2.
- **#65** [S4/P3] `runs list` order contradicts docs.

### Remaining for a follow-up pass
- Area G live-server resolvers (RES-006..012) — stand up local git/http/OCI.
- Area K cluster backend (CLS-001..008) — `cluster up` then the fixture diff.
- Area L remote-docker (RMT-001..004).
- SIG-001..003 (signal teardown, exit 130) and CNC-001/002 (concurrency).
