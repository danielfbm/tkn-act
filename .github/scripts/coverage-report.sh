#!/usr/bin/env bash
# coverage-report.sh — compute an aggregated total-coverage number across the
# test suites CI actually runs, and publish it as a GitHub Actions job summary
# plus uploadable artifacts.
#
# Unlike coverage-check.sh (the per-package no-drop *gate*, default tags only),
# this script is a *report*: it merges coverage from the default test set AND
# the `-tags integration` docker-backend suite into one total, using Go 1.20+
# binary coverage (`-test.gocoverdir` + `go tool covdata`). The integration
# suite is what lifts the otherwise-invisible docker backend (docker.go /
# sidecars.go) into the measured number.
#
# Cluster (`-tags cluster`) coverage is reported separately by
# cluster-integration.yml — it needs a k3d cluster and runs on its own matrix,
# so folding it into this single-runner total would couple two workflows.
#
# Usage: coverage-report.sh
#
# Environment:
#   COVERAGE_INTEGRATION=0   skip the -tags integration run (default: 1).
#                            Set to 0 for a fast unit-only number locally or on
#                            a runner without a docker daemon.
#   GITHUB_STEP_SUMMARY      if set (GitHub Actions), a Markdown summary is
#                            appended to it. Otherwise summary goes to stdout.
#
# Outputs (under ./coverage/):
#   coverage.txt    merged profile in `go tool cover` text format
#   coverage.html   browsable HTML report (go tool cover -html)
#   per-package.txt merged per-package percentages (go tool covdata percent)
#
# Exit codes:
#   0  report produced (a failing/flaky integration suite does NOT fail this
#      script — the integration result is best-effort for the *number*; the
#      docker-integration workflow remains the correctness gate).
#   1  the default (unit) test run failed, or merge/format tooling failed —
#      we cannot produce a trustworthy number, so surface it.

set -uo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

want_integration="${COVERAGE_INTEGRATION:-1}"

# Integration package set — mirror docker-integration.yml so the number
# reflects what that gate exercises (engine end-to-end + docker backend).
integration_pkgs=(./internal/e2e/... ./internal/backend/docker/...)

out_dir="$repo_root/coverage"
rm -rf "$out_dir"
mkdir -p "$out_dir"

covroot=$(mktemp -d -t tkn-act-covdata.XXXXXX)
trap 'rm -rf "$covroot"' EXIT
unit_dir="$covroot/unit"
integ_dir="$covroot/integration"
merged_dir="$covroot/merged"
mkdir -p "$unit_dir" "$integ_dir" "$merged_dir"

echo "coverage-report: measuring default (unit) test set with binary coverage" >&2
# -coverpkg=./... instruments every package so cross-package execution (e.g.
# cmd/tkn-act tests exercising internal/*) is attributed. No -race: this is a
# coverage-shape measurement, and -race ~doubles wall time.
if ! go test -coverpkg=./... -count=1 ./... -args -test.gocoverdir="$unit_dir"; then
  echo "coverage-report: default test set FAILED — cannot produce a trustworthy total." >&2
  exit 1
fi

contributing="default (\`go test ./...\`)"
if [ "$want_integration" = "1" ]; then
  echo "coverage-report: measuring -tags integration docker suite (best-effort)" >&2
  # Best-effort: a flaky integration run still wrote coverage for whatever
  # executed before it failed. We capture rc for the summary but never let it
  # fail the report — docker-integration.yml is the correctness gate.
  if go test -tags integration -coverpkg=./... -count=1 -timeout 20m \
        "${integration_pkgs[@]}" -args -test.gocoverdir="$integ_dir"; then
    integ_status="passed"
  else
    integ_status="FAILED (coverage still merged from what ran)"
  fi
  contributing="$contributing + integration (\`-tags integration ${integration_pkgs[*]}\`, $integ_status)"
  merge_inputs="$unit_dir,$integ_dir"
else
  echo "coverage-report: COVERAGE_INTEGRATION=0 — unit-only number" >&2
  merge_inputs="$unit_dir"
fi

echo "coverage-report: merging coverage data" >&2
if ! go tool covdata merge -i="$merge_inputs" -o="$merged_dir"; then
  echo "coverage-report: covdata merge failed." >&2
  exit 1
fi

# Text profile (for HTML + the authoritative total via `go tool cover -func`).
if ! go tool covdata textfmt -i="$merged_dir" -o="$out_dir/coverage.txt"; then
  echo "coverage-report: covdata textfmt failed." >&2
  exit 1
fi

# `go tool covdata percent` is tab-formatted and *glues* zero-statement
# packages (no coverage suffix, no trailing newline) onto the front of the
# next package's line. Parse defensively: per line, the owning package is the
# last `github.com/...` field that precedes the `coverage: NN.N%` field.
# Zero-statement packages (interface-only, e.g. internal/backend) carry no
# percent and are intentionally omitted from the table.
go tool covdata percent -i="$merged_dir" 2>/dev/null \
  | awk -F'\t' '
      {
        pkg = ""; pct = ""
        for (i = 1; i <= NF; i++) {
          if ($i ~ /^github\.com\//)        pkg = $i
          if ($i ~ /coverage: [0-9.]+%/) {
            s = $i; sub(/.*coverage: /, "", s); sub(/%.*/, "", s); pct = s
          }
        }
        if (pkg != "" && pct != "") printf "%s\t%s%%\n", pkg, pct
      }' \
  | sort > "$out_dir/per-package.txt"

go tool cover -html="$out_dir/coverage.txt" -o "$out_dir/coverage.html" 2>/dev/null || true

# Authoritative total: the "total:" line from `go tool cover -func`.
total=$(go tool cover -func="$out_dir/coverage.txt" 2>/dev/null | awk '/^total:/{print $NF}')
total="${total:-n/a}"

# ---- Render the summary -----------------------------------------------------
summary_to() {
  local sink="$1"
  {
    echo "## Coverage report"
    echo
    echo "**Total: \`${total}\`**  (statements, merged across suites)"
    echo
    echo "Contributing suites: ${contributing}."
    echo
    echo "Cluster (\`-tags cluster\`) coverage is reported separately by the cluster-integration workflow."
    echo
    echo "<details><summary>Per-package coverage (merged)</summary>"
    echo
    echo "| Package | Coverage |"
    echo "|---|---|"
    # Shorten the import-path prefix for readability.
    sed -E 's#github.com/danielfbm/tkn-act/##' "$out_dir/per-package.txt" \
      | awk -F'\t' '{printf "| %s | %s |\n", $1, $2}'
    echo
    echo "</details>"
  } >> "$sink"
}

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  summary_to "$GITHUB_STEP_SUMMARY"
fi

echo
echo "coverage-report: total = ${total}  (${contributing})"
echo "coverage-report: artifacts in $out_dir/ (coverage.txt, coverage.html, per-package.txt)"
echo
echo "Per-package (merged):"
if command -v column >/dev/null 2>&1; then
  sed -E 's#github.com/danielfbm/tkn-act/##' "$out_dir/per-package.txt" | column -t -s $'\t'
else
  sed -E 's#github.com/danielfbm/tkn-act/##' "$out_dir/per-package.txt"
fi
