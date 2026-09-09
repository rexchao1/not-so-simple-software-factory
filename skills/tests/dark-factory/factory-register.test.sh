#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPTS="$(cd "$HERE/../../skills/dark-factory/scripts" && pwd -P)"
. "$HERE/lib.sh"
REG="$SCRIPTS/factory-register"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/home" "$WORK/plain"
printf 'scratch\tgithub.com/octocat/factory-scratch\t\n' > "$WORK/projects.tsv"
export FACTORY_PROJECTS="$WORK/projects.tsv"

SKILL_DIR="$(cd "$SCRIPTS/.." && pwd -P)"
before="$(find "$SKILL_DIR" -type f | sort | xargs shasum | shasum)"

# One canned body answers every request in a run, so it carries the shape of
# all four: the repository list, the create response, the readiness report, and
# the delivery response.
repos_with() { # repos_with <remote_identity> <true|false enabled>
  jq -nc --arg r "$1" --argjson e "$2" \
    '[{id: "c0ffee00-0000-4000-8000-000000000001", remote_identity: $r, enabled: $e, default_delivery: "pr"}]'
}

body_for() { # body_for <repositories json array>
  jq -nc --argjson repos "$1" '{
    repositories: $repos,
    id: "c0ffee00-0000-4000-8000-000000000001",
    default_delivery: "pr+automerge",
    routing_ready: true,
    workers: [{id: "w1", name: "mini", cached: true, advertised: true, ready: true, reason: ""}]}'
}

mkrepo() { # mkrepo <dir> <origin url>
  rm -rf "$1"; mkdir -p "$1"
  git -C "$1" init -q >/dev/null 2>&1
  git -C "$1" remote add origin "$2"
}

PORT=18120
run_in() { # run_in <cwd> <status> <body> <command...> -> RC, OUT (stdout+stderr), SENT
  local dir="$1" status="$2" body="$3"; shift 3
  PORT=$((PORT + 1))
  fake_api_start "$PORT" "$status" "$body"
  OUT="$(cd "$dir" && FACTORY_BASE="http://127.0.0.1:$PORT" "$@" 2>&1)"
  RC=$?
  SENT="$(cat "$FAKE_LOG")"
  fake_api_stop
}

ALPHA="github.com/octocat/alpha"
PRESENT="$(body_for "$(repos_with "$ALPHA" true)")"
ABSENT="$(body_for '[]')"

# 1. --list prints one row per managed repository.
run_in "$WORK" 200 "$PRESENT" "$REG" --list
assert_eq "--list exits 0" "0" "$RC"
assert_contains "--list prints the identity" "$ALPHA" "$OUT"
assert_contains "--list prints the delivery mode" "pr" "$OUT"

# 2. With no argument at all, the repository is the checkout we are standing
# in. That is the case this script is for: somebody in a new repository who
# wants the factory to know about it.
mkrepo "$WORK/derived" "git@github.com:octocat/alpha.git"
run_in "$WORK/derived" 201 "$ABSENT" "$REG"
assert_eq "no argument at all exits 0" "0" "$RC"
assert_contains "registers the derived repository" '{"remote_identity":"github.com/octocat/alpha"}' "$SENT"
assert_contains "says what it registered" "registered: $ALPHA" "$OUT"

# Every origin form collapses to the same identity, or a repository gets
# registered twice under two spellings.
while read -r form; do
  [ -n "$form" ] || continue
  mkrepo "$WORK/derived" "$form"
  run_in "$WORK/derived" 201 "$ABSENT" "$REG"
  assert_contains "derives $ALPHA from $form" '{"remote_identity":"github.com/octocat/alpha"}' "$SENT"
done <<'FORMS'
https://github.com/octocat/alpha.git
https://github.com/octocat/alpha
ssh://git@github.com/octocat/alpha.git
FORMS

# 3. Already registered is not an error and does not register again.
mkrepo "$WORK/derived" "git@github.com:octocat/alpha.git"
run_in "$WORK/derived" 200 "$PRESENT" "$REG"
assert_eq "an already registered repository exits 0" "0" "$RC"
assert_contains "says it was already registered" "registered already: $ALPHA" "$OUT"
if grep -q '^POST /api/v1/repositories' <<< "$SENT"; then
  notok "registered a repository the factory already had"
else
  ok "does not register a repository the factory already had"
fi
assert_contains "prints routing readiness" "routing_ready: true" "$OUT"

# 4. --repo names the repository outright, from anywhere.
run_in "$WORK/plain" 201 "$ABSENT" "$REG" --repo github.com/octocat/beta
assert_eq "--repo works outside any checkout" "0" "$RC"
assert_contains "--repo is the identity that is registered" \
  '{"remote_identity":"github.com/octocat/beta"}' "$SENT"

out5="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$REG" --repo https://gitlab.com/octocat/beta 2>&1)"
assert_eq "a --repo that is not a github identity exits 2" "2" "$?"

out6="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$REG" scratch --repo github.com/octocat/beta 2>&1)"
assert_eq "a project and --repo together exits 2" "2" "$?"
assert_contains "and says which to pick" "not both" "$out6"

# 5. Derivation refuses rather than guessing, before any request.
out7="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$REG" 2>&1)"
assert_eq "outside a git checkout exits 2" "2" "$?"
assert_contains "outside a git checkout says so" "not inside a git checkout" "$out7"

mkrepo "$WORK/gitlab" "https://gitlab.com/octocat/alpha.git"
out8="$(cd "$WORK/gitlab" && FACTORY_BASE=http://127.0.0.1:1 "$REG" 2>&1)"
assert_eq "a non-github origin exits 2" "2" "$?"
assert_contains "a non-github origin names what it refused" "gitlab.com/octocat/alpha" "$out8"

# 6. A project name still means the map, and still needs the map to exist.
run_in "$WORK/derived" 200 "$(body_for "$(repos_with github.com/octocat/factory-scratch true)")" \
  "$REG" scratch
assert_eq "a project name exits 0" "0" "$RC"
assert_contains "a project name reads the map, not the checkout" \
  "github.com/octocat/factory-scratch" "$OUT"

out9="$(cd "$WORK/plain" && env -u FACTORY_PROJECTS HOME="$WORK/home" FACTORY_BASE=http://127.0.0.1:1 \
  "$REG" scratch 2>&1)"
assert_eq "a project name with no map exits 2" "2" "$?"
assert_contains "and names the map it wanted" "projects.tsv" "$out9"

# 7. Deriving does not need the map at all, which is the whole point.
run_in "$WORK/derived" 200 "$PRESENT" env -u FACTORY_PROJECTS HOME="$WORK/home" "$REG"
assert_eq "deriving works with no map anywhere" "0" "$RC"
assert_contains "and reports the derived repository" "$ALPHA" "$OUT"

# 8. --delivery is still the only thing that changes a delivery mode, and it
# is still an explicit request. INV-8 reads this setting and nothing else.
run_in "$WORK/derived" 200 "$PRESENT" "$REG" --delivery pr+automerge
assert_eq "--delivery exits 0" "0" "$RC"
assert_contains "--delivery puts the mode" '{"default_delivery":"pr+automerge"}' "$SENT"
assert_contains "--delivery reports what it set" "default_delivery: pr+automerge" "$OUT"

run_in "$WORK/derived" 200 "$PRESENT" "$REG"
if grep -q '^PUT ' <<< "$SENT"; then
  notok "changed a delivery mode without being asked to"
else
  ok "touches no delivery mode unless --delivery was given"
fi

out10="$(cd "$WORK/derived" && FACTORY_BASE=http://127.0.0.1:1 "$REG" --delivery merge 2>&1)"
assert_eq "an unknown delivery mode exits 2" "2" "$?"

# 9. Nothing was written inside the skill directory.
after="$(find "$SKILL_DIR" -type f | sort | xargs shasum | shasum)"
assert_eq "writes nothing inside the installed skill directory" "$before" "$after"

exit "$TESTS_FAILED"
