#!/usr/bin/env bash
# parity-check.sh — assert that docs/feature-parity.md doesn't lie.
#
# Invariants enforced:
#   1. Every `shipped` row's e2e-fixture column names a directory that
#      exists under testdata/e2e/.
#   2. Every `shipped` row's limitations-fixture column is `none` (the
#      graduation rule — when a feature ships, we delete its limitations
#      example in the same PR).
#   3. Every `shipped` row's e2e fixture appears in
#      internal/e2e/fixtures/fixtures.go's All() table, so it runs on
#      both backends.
#   4. Every `gap` / `in-progress` / `by-design` row whose limitations
#      column names a directory has that directory present on disk.
#   5. Every directory under testdata/limitations/ (other than the
#      README) is referenced by at least one row's limitations column.
#
# A failure prints the offending row and the invariant it broke. Run
# locally before opening a PR; CI runs it as the `parity-check` job.

set -euo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
parity_doc="$repo_root/docs/feature-parity.md"
e2e_dir="$repo_root/testdata/e2e"
lim_dir="$repo_root/testdata/limitations"
fixtures_go="$repo_root/internal/e2e/fixtures/fixtures.go"

[ -f "$parity_doc" ] || { echo "parity-check: $parity_doc not found"; exit 2; }
[ -f "$fixtures_go" ] || { echo "parity-check: $fixtures_go not found"; exit 2; }

fail=0
fail_msg() {
  echo "parity-check FAIL: $*" >&2
  fail=1
}

# Extract the feature rows: lines that look like `| Feature ... | shipped | both | ... |`.
# A feature row has at least 7 pipe-separated columns and column 3 is one
# of the recognized statuses. We strip leading/trailing whitespace per
# column and ignore the markdown header / separator lines.
declare -a rows=()
while IFS= read -r line; do
  case "$line" in
    "| Feature"*|"|---"*|"|--"*) continue ;;
  esac
  # Quick filter: must have at least one of the status keywords as a cell.
  case "$line" in
    *"| shipped |"*|*"| in-progress |"*|*"| gap |"*|*"| out-of-scope |"*|*"| by-design |"*) ;;
    *) continue ;;
  esac
  rows+=("$line")
done < "$parity_doc"

if [ "${#rows[@]}" -eq 0 ]; then
  fail_msg "no feature rows parsed from $parity_doc — table format may be broken"
fi

# Collect the e2e fixture names referenced by `shipped` rows.
declare -A shipped_e2e=()
declare -A all_referenced_lims=()

for line in "${rows[@]}"; do
  # Split by pipe; trim each cell.
  IFS='|' read -ra cells <<< "$line"
  # cells[0] is the empty string before the first leading `|`. Bash's
  # `read` strips the trailing empty cell after the closing `|`, so a
  # 7-content-column row produces 8 cells (cells[1]..cells[7]).
  # Anything shorter is one of the trailing 3- or 4-column
  # informational tables (Backends, Agent contract) — skip it.
  if [ "${#cells[@]}" -lt 8 ]; then
    continue
  fi

  feature=$(echo "${cells[1]}" | sed -e 's/^ *//' -e 's/ *$//')
  status=$(echo "${cells[3]}" | sed -e 's/^ *//' -e 's/ *$//')
  e2e=$(echo "${cells[5]}" | sed -e 's/^ *//' -e 's/ *$//')
  lim=$(echo "${cells[6]}" | sed -e 's/^ *//' -e 's/ *$//')

  case "$status" in
    shipped)
      # Invariant 1: e2e fixture exists (or 'none' is allowed only for
      # cross-cutting features that legitimately have no fixture — they
      # should be marked 'none' explicitly).
      if [ "$e2e" != "none" ] && [ -n "$e2e" ]; then
        if [ ! -d "$e2e_dir/$e2e" ]; then
          fail_msg "row '$feature' is shipped but testdata/e2e/$e2e/ does not exist"
        fi
        shipped_e2e["$e2e"]=1
      fi
      # Invariant 2: limitations fixture must be 'none'.
      if [ "$lim" != "none" ] && [ -n "$lim" ]; then
        fail_msg "row '$feature' is shipped but still names a limitations fixture: $lim (delete testdata/limitations/$lim/ and set the cell to 'none')"
      fi
      ;;
    gap|in-progress|by-design)
      # Invariant 4: if a limitations dir is named, it must exist.
      if [ "$lim" != "none" ] && [ -n "$lim" ]; then
        if [ ! -d "$lim_dir/$lim" ]; then
          fail_msg "row '$feature' names limitations fixture '$lim' but testdata/limitations/$lim/ does not exist"
        fi
        all_referenced_lims["$lim"]=1
      fi
      ;;
    out-of-scope)
      : # No invariants — explicitly not supported.
      ;;
    *)
      fail_msg "row '$feature' has unknown status: '$status' (must be shipped|in-progress|gap|out-of-scope|by-design)"
      ;;
  esac
done

# Invariant 3: every shipped e2e fixture appears in fixtures.All().
for fix in "${!shipped_e2e[@]}"; do
  if ! grep -q "Dir: \"$fix\"" "$fixtures_go"; then
    fail_msg "row uses e2e fixture '$fix' but it isn't in internal/e2e/fixtures/fixtures.go's All() table — cross-backend invariant broken"
  fi
done

# Invariant 5: every dir under testdata/limitations/ is referenced.
if [ -d "$lim_dir" ]; then
  for entry in "$lim_dir"/*/; do
    [ -d "$entry" ] || continue
    name=$(basename "$entry")
    if [ -z "${all_referenced_lims[$name]:-}" ]; then
      fail_msg "testdata/limitations/$name/ exists but no row in feature-parity.md references it (orphan limitations fixture)"
    fi
  done
fi

if [ "$fail" -ne 0 ]; then
  echo >&2
  echo "parity-check: docs/feature-parity.md is out of sync with the tree." >&2
  echo "  Fix the row(s) above OR update the listed fixture(s) to match." >&2
  exit 1
fi

echo "parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent."
