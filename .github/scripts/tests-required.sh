#!/usr/bin/env bash
# tests-required.sh — fail if a PR changes Go source without changing any
# test file. Override per-PR by including the literal token "[skip-test-check]"
# in any commit message in the PR.
#
# Usage: tests-required.sh <base-sha> <head-sha>

set -euo pipefail

base="${1:?base sha required}"
head="${2:?head sha required}"

# 1. Override token in any commit message between base..head.
if git log --format=%B "$base..$head" | grep -qF '[skip-test-check]'; then
  echo "tests-required: skipped via [skip-test-check] in commit message"
  exit 0
fi

# 2. Collect changed paths.
changed=$(git diff --name-only "$base" "$head")

# 3. Classify.
code_changed=0
test_changed=0
while IFS= read -r f; do
  [ -z "$f" ] && continue
  case "$f" in
    *_test.go) test_changed=1 ;;
    *.go)
      # Vendored / generated files don't count as "code that needs a test."
      case "$f" in
        vendor/*) ;;
        *) code_changed=1 ;;
      esac
      ;;
  esac
done <<< "$changed"

if [ "$code_changed" -eq 0 ]; then
  echo "tests-required: no Go production code changed; nothing to enforce"
  exit 0
fi
if [ "$test_changed" -eq 1 ]; then
  echo "tests-required: code change accompanied by test change — OK"
  exit 0
fi

cat <<'EOF' >&2
tests-required: a Go source file changed but no _test.go file did.

Every change to production Go code in this repo must include a test that
exercises the change (a new test, an updated assertion, or a new fixture).

If this PR genuinely cannot have a test (e.g. pure dependency bump, doc
typo in a Go comment, regenerated boilerplate), include the literal token

  [skip-test-check]

in any commit message in the PR. The check will then pass.

See AGENTS.md "Contribution rule: tests required" for the full rationale.
EOF
exit 1
