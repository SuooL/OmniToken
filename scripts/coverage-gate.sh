#!/usr/bin/env bash
# Per-package coverage floor for the packages that generate event_id.
#
# Why only these packages: event_id is the idempotency key (docs/adr/0004-event-identity.md).
# If dedup breaks, already-ingested history gets double-counted — reverting the code
# does not repair the database. Everywhere else, `go vet` + tests passing is enough;
# we deliberately do NOT gate coverage on I/O-heavy packages (server, collect's SSH
# and git paths, cmd) where mocking costs a lot and catches little.
#
# Thresholds are the measured values rounded down by 1-2 points, so an ordinary
# refactor that shifts the percentage slightly does not fail the build. Raise them
# by hand when coverage improves — this script never rewrites itself, because a CI
# job that pushes commits back would collide with the PR flow.
#
# Usage: scripts/coverage-gate.sh
# Exits 0 when every critical package meets its floor, 1 otherwise.

set -uo pipefail

MODULE="github.com/suool/omnitoken"

# <package path>:<minimum statement coverage %>
CRITICAL=(
  "internal/parser/codex:90"
  "internal/parser/claudecode:80"
  "internal/agent:64"
)

output="$(go test ./... -cover 2>&1)"
test_status=$?
printf '%s\n' "$output"

if [ $test_status -ne 0 ]; then
  echo
  echo "FAIL: tests did not pass; coverage gate not evaluated."
  exit 1
fi

echo
echo "Coverage gate (critical packages only)"
echo "--------------------------------------"

failed=0
for entry in "${CRITICAL[@]}"; do
  pkg="${entry%:*}"
  min="${entry##*:}"

  line="$(printf '%s\n' "$output" | grep -E "[[:space:]]${MODULE}/${pkg}[[:space:]]" || true)"
  if [ -z "$line" ]; then
    printf '  %-34s NO RESULT (package missing or has no tests)\n' "$pkg"
    failed=1
    continue
  fi

  pct="$(printf '%s\n' "$line" | sed -nE 's/.*coverage: ([0-9.]+)% of statements.*/\1/p')"
  if [ -z "$pct" ]; then
    printf '  %-34s NO COVERAGE REPORTED\n' "$pkg"
    failed=1
    continue
  fi

  if awk -v a="$pct" -v b="$min" 'BEGIN { exit !(a + 0 >= b + 0) }'; then
    printf '  %-34s %6s%%  >= %s%%   PASS\n' "$pkg" "$pct" "$min"
  else
    printf '  %-34s %6s%%  <  %s%%   FAIL\n' "$pkg" "$pct" "$min"
    failed=1
  fi
done

echo
if [ $failed -ne 0 ]; then
  echo "FAIL: a package that generates event_id dropped below its coverage floor."
  echo "Add tests for the changed logic, or justify raising/lowering the floor in the PR."
  exit 1
fi

echo "PASS: all critical packages meet their coverage floor."
