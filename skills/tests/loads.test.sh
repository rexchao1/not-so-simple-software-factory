#!/usr/bin/env bash
# Proves that skills installed by install.sh are actually discovered by
# Claude Code, which is only observable by asking the harness.
#
# Runs after ./install.sh. Requires a claude with keychain access, so on the
# host it must run inside a GUI-started terminal or tmux server, not over a
# bare SSH session.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

fail=0
ok()    { printf 'ok - %s\n' "$1"; }
notok() { printf 'not ok - %s\n' "$1"; fail=1; }

out="$(claude -p 'Answer in exactly one line: "SKILLS:" followed by a comma separated list of the names of every skill available to you, or NONE. Do not use any tools.' 2>&1)"
printf '%s\n' "$out"

# Read only the answer line, then compare whole comma separated tokens. A bare
# substring search would pass on an echo of this prompt, on prose that merely
# mentions a skill, or on a longer name that contains a shorter one.
line="$(printf '%s\n' "$out" | grep -m1 -E '^[[:space:]]*[*_`]*SKILLS:' || true)"
if [ -z "$line" ]; then
  notok "claude did not answer with a SKILLS: line"
  exit "$fail"
fi

reported="$(printf '%s\n' "$line" \
  | sed 's/^.*SKILLS:[[:space:]]*//' \
  | tr ',' '\n' \
  | sed 's/[*_`]//g; s/^[[:space:]]*//; s/[[:space:]]*$//' \
  | grep -v '^$')"

missing=""
for dir in skills/*/; do
  skill="$(basename "$dir")"
  grep -qxF "$skill" <<< "$reported" || missing="$missing $skill"
done
if [ -z "$missing" ]; then
  ok "every skill in skills/ is discovered by claude"
else
  notok "skills not discovered:$missing (run ./install.sh)"
fi

exit "$fail"
