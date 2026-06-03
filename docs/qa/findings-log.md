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
