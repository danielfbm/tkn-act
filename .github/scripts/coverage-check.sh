#!/usr/bin/env bash
# coverage-check.sh — fail if any Go package's test coverage on HEAD is
# lower than the same package's coverage on BASE. Sibling to
# tests-required.sh. Override per-PR by including the literal token
# "[skip-coverage-check]" in any commit message in the PR.
#
# Usage: coverage-check.sh <base-ref> <head-ref>
#
# Behavior:
#   - Runs `go test -cover -count=1 ./...` (default tags) on BASE and on
#     HEAD, parses the per-package "coverage: NN.N% of statements" line
#     emitted by `go test`, and compares per-package.
#   - A package on HEAD is a "drop" iff it existed on BASE with measurable
#     coverage and its HEAD coverage is lower than BASE by more than the
#     rounding tolerance (0.1%).
#   - New packages on HEAD that didn't exist on BASE: skipped (no baseline).
#   - Packages removed on HEAD: not a drop; ignored.
#   - Packages with `[no statements]` (no measurable code) are treated as
#     100.0% on both sides — they can never drop.
#   - Packages with `[no test files]` are skipped entirely (no measurement
#     possible, no baseline to enforce against).
#   - Tests failing on either side aborts with a clear message; we do not
#     paper over failures by treating them as 0% coverage.
#
# This is the default (no-tag) test set — same as `ci.yml`'s build-and-test
# job. We do not measure coverage from `-tags integration` / `-tags
# cluster` here; those run in their own workflows and would double CI time.

set -euo pipefail

base_ref="${1:?base ref required}"
head_ref="${2:?head ref required}"

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

# Resolve refs to short SHAs early so log messages are stable even if the
# refs are symbolic ("origin/main", "HEAD").
base_sha=$(git rev-parse --short "$base_ref")
head_sha=$(git rev-parse --short "$head_ref")

# 1. Override token in any commit message between base..head.
if git log --format=%B "$base_ref..$head_ref" 2>/dev/null | grep -qF '[skip-coverage-check]'; then
  echo "coverage-check: skipped via [skip-coverage-check] in commit message"
  exit 0
fi

tolerance="0.1"   # percentage points

tmpdir=$(mktemp -d -t tkn-act-coverage.XXXXXX)
trap 'rm -rf "$tmpdir"' EXIT

base_out="$tmpdir/base.txt"
head_out="$tmpdir/head.txt"
base_pkgs="$tmpdir/base.pkgs"
head_pkgs="$tmpdir/head.pkgs"

# measure_one <ref> <out-file> <pkgs-file>
#
# Checks out <ref> into a separate worktree, runs `go test -cover -count=1
# ./...`, captures every line that `go test` emits, then writes per-package
# "<pkg>\t<pct>" lines to <pkgs-file>. Aborts if any test failed.
measure_one() {
  local ref="$1" out_file="$2" pkgs_file="$3"
  local label sha
  sha=$(git rev-parse --short "$ref")
  label="$ref ($sha)"

  local wt
  wt=$(mktemp -d -t tkn-act-cov-wt.XXXXXX)
  echo "coverage-check: measuring $label in $wt" >&2

  # Use a detached worktree so we can leave the caller's checkout alone.
  git worktree add --quiet --detach "$wt" "$ref"

  # Run the tests. We capture stdout+stderr; `go test -cover` prints the
  # per-package "coverage:" line to stdout. We don't want -race here —
  # this is a coverage-shape measurement and -race ~doubles wall time.
  local rc=0
  ( cd "$wt" && go test -cover -count=1 ./... ) >"$out_file" 2>&1 || rc=$?

  git worktree remove --force "$wt" >/dev/null 2>&1 || true
  rm -rf "$wt" 2>/dev/null || true

  if [ "$rc" -ne 0 ]; then
    echo >&2
    echo "coverage-check: tests failed on $label — coverage measurement aborted." >&2
    echo "  Last 40 lines of output:" >&2
    tail -n 40 "$out_file" >&2
    return 1
  fi

  # Parse `go test -cover` output. Lines we care about:
  #
  #   ok  \t<pkg>\t<dur>s\tcoverage: NN.N% of statements
  #   ok  \t<pkg>\t<dur>s\tcoverage: [no statements]
  #   ?   \t<pkg>\t[no test files]
  #
  # We map:
  #   - "[no statements]" -> 100.0   (nothing to lose)
  #   - "[no test files]" -> skip    (no baseline either way)
  #   - "NN.N%"           -> NN.N
  #
  # The tab separators are the format `go test` actually uses, but be
  # defensive: split on any whitespace and look for the keywords by name.
  awk '
    /^ok[ \t]/ {
      pkg = $2
      pct = "skip"
      for (i = 3; i <= NF; i++) {
        if ($i == "coverage:") {
          val = $(i + 1)
          if (val == "[no") { pct = "100.0" }
          else { sub(/%$/, "", val); pct = val }
          break
        }
      }
      if (pct != "skip") print pkg "\t" pct
      next
    }
    # `?   <pkg> [no test files]` — skip; no measurement either side.
    # `FAIL <pkg> ...` should already have been caught by `go test` exit
    # code. Defensively, surface it on stderr.
    /^FAIL[ \t]/ {
      print "coverage-check: unexpected FAIL line: " $0 > "/dev/stderr"
    }
  ' "$out_file" | sort > "$pkgs_file"
}

measure_one "$base_ref" "$base_out" "$base_pkgs"
measure_one "$head_ref" "$head_out" "$head_pkgs"

# Build a comparison table.
#
# For each package present on HEAD that is also present on BASE, compute
# the delta. A drop is `head < base - tolerance`.
declare -i drops=0
report="$tmpdir/report.txt"
: > "$report"

# Read both files into associative arrays.
declare -A base_cov=()
declare -A head_cov=()

while IFS=$'\t' read -r pkg pct; do
  [ -z "$pkg" ] && continue
  base_cov["$pkg"]="$pct"
done < "$base_pkgs"

while IFS=$'\t' read -r pkg pct; do
  [ -z "$pkg" ] && continue
  head_cov["$pkg"]="$pct"
done < "$head_pkgs"

# Iterate the union, sorted, so the table is stable.
all_pkgs=$( ( cut -f1 "$base_pkgs"; cut -f1 "$head_pkgs" ) | sort -u )

# Use awk for the float math so we don't depend on `bc` being installed.
classify() {
  # args: base head tolerance
  awk -v b="$1" -v h="$2" -v t="$3" '
    BEGIN {
      if (b == "") b = -1
      if (h == "") h = -1
      if (b < 0 && h < 0) { print "skip"; exit }
      if (b < 0)          { print "new"; exit }
      if (h < 0)          { print "removed"; exit }
      d = h - b
      if (d < -t)         { print "drop"; exit }
      if (d > t)          { print "gain"; exit }
      print "same"
    }
  '
}

# Render: <pkg>  <base>  <head>  <delta>  <verdict>
{
  printf "PACKAGE\tBASE\tHEAD\tDELTA\tSTATUS\n"
  while IFS= read -r pkg; do
    [ -z "$pkg" ] && continue
    b="${base_cov[$pkg]:-}"
    h="${head_cov[$pkg]:-}"
    verdict=$(classify "$b" "$h" "$tolerance")
    case "$verdict" in
      drop)
        delta=$(awk -v b="$b" -v h="$h" 'BEGIN{printf "%+.1f", h - b}')
        printf "%s\t%s%%\t%s%%\t%s\tDROP\n" "$pkg" "$b" "$h" "$delta"
        drops=$((drops + 1))
        ;;
      gain)
        delta=$(awk -v b="$b" -v h="$h" 'BEGIN{printf "%+.1f", h - b}')
        printf "%s\t%s%%\t%s%%\t%s\tgain\n" "$pkg" "$b" "$h" "$delta"
        ;;
      same)
        printf "%s\t%s%%\t%s%%\t  -\tsame\n" "$pkg" "$b" "$h"
        ;;
      new)
        printf "%s\t-\t%s%%\t  -\tnew (no baseline)\n" "$pkg" "$h"
        ;;
      removed)
        printf "%s\t%s%%\t-\t  -\tremoved\n" "$pkg" "$b"
        ;;
    esac
  done <<< "$all_pkgs"
} > "$report"

# Print the table (column-aligned for humans).
echo
echo "coverage-check: per-package coverage, $base_sha -> $head_sha (default test set, no build tags)"
echo
if command -v column >/dev/null 2>&1; then
  column -t -s $'\t' "$report"
else
  cat "$report"
fi
echo

if [ "$drops" -gt 0 ]; then
  cat <<EOF >&2
coverage-check: $drops package(s) dropped coverage vs $base_ref by more than ${tolerance}pp.

Add tests for the affected package(s) to bring HEAD coverage back to at
least the BASE level. If this PR genuinely cannot or should not maintain
coverage (e.g. a deletion that removes a covered code path along with its
tests, or a refactor that intentionally drops dead code), include the
literal token

  [skip-coverage-check]

in any commit message in the PR. The check will then pass.

See AGENTS.md "Contribution rule: tests required" / "Coverage gate" for
the full rationale.
EOF
  exit 1
fi

echo "coverage-check: no per-package coverage drops vs $base_ref — OK"
