#!/usr/bin/env bash
# Every dark-factory script test, in one run.
#
# Not wired into `bin/skills test`: that runs the discovery canary, which shells
# out to claude and needs Keychain access. These need neither, and they must
# stay runnable from anywhere, including a bare SSH session.
#
# Every test serves its own fake factory on a loopback port or points at a dead
# one. Nothing here reaches the real factory, the real project map, or ~/.factory.
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
  echo "dark-factory tests: ok"
else
  echo "dark-factory tests: FAILED"
fi
exit "$failed"
