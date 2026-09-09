#!/usr/bin/env bash
# Every light-factory script test, in one run.
#
# Same shape and the same promise as tests/dark-factory/run.sh: every test
# serves its own fake planning API on a loopback port or points at a dead one.
# Nothing here reaches the real factory, the real vault, or ~/.factory.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
failed=0

for test in "$HERE"/*.test.sh; do
  name="$(basename "$test")"
  echo "# $name"
  bash "$test" || failed=1
done

echo
if [ "$failed" -eq 0 ]; then
  echo "light-factory tests: ok"
else
  echo "light-factory tests: FAILED"
fi
exit "$failed"
