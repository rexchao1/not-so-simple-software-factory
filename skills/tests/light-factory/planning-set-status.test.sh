#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SET="$LF_SCRIPTS/planning-set-status"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# 1. Usage, and a status that is not one of the five fails locally so a typo
#    reads as a typo rather than as a refusal from the server.
"$SET" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SET" payer 2 frozzen 2>&1)"
assert_eq "an unknown status exits 2" "2" "$?"
assert_contains "lists the five statuses" "planned, review, fog, frozen, built" "$out"

# 2. A move the table allows.
cat > "$WORK/ok.json" <<'JSON'
[{"method": "POST", "path_contains": "/checkpoints/2/status", "status": 200,
  "body": {"project": "payer", "number": 2, "status": "frozen",
           "frozen_at": "2026-09-07T11:30:00Z"}}]
JSON
fake_planning_start 18131 "$WORK/ok.json"
out="$(FACTORY_BASE=http://127.0.0.1:18131 "$SET" payer 2 frozen 2>/dev/null)"
rc=$?
body="$(sent POST "/checkpoints/2/status" 1)"
fake_planning_stop
assert_eq "an allowed move exits 0" "0" "$rc"
assert_eq "sends the status and nothing else" '{"status":"frozen"}' "$body"
assert_contains "says where the checkpoint landed" "is now frozen" "$out"
assert_contains "says when it froze" "2026-09-07T11:30:00Z" "$out"

# 3. A move the table refuses. The code is the thing to branch on and the
#    thing to say: it means the checkpoint is not where this session thought.
cat > "$WORK/refused.json" <<'JSON'
[{"method": "POST", "path_contains": "/checkpoints/2/status", "status": 409,
  "body": {"error": {"code": "planning_transition_not_allowed",
                     "message": "a checkpoint cannot move from frozen to review"}}}]
JSON
fake_planning_start 18132 "$WORK/refused.json"
out="$(FACTORY_BASE=http://127.0.0.1:18132 "$SET" payer 2 review 2>&1)"
rc=$?
fake_planning_stop
assert_eq "a refused move exits 1" "1" "$rc"
assert_contains "prints the error code plainly" "refused planning_transition_not_allowed" "$out"
assert_contains "prints the server's own words too" "cannot move from frozen to review" "$out"
assert_contains "says what to do about it" "planning-show" "$out"

# 4. Any other API error is still reported by its code, not turned into the
#    transition message.
cat > "$WORK/gone.json" <<'JSON'
[{"method": "POST", "path_contains": "/status", "status": 404,
  "body": {"error": {"code": "not_found", "message": "no such checkpoint"}}}]
JSON
fake_planning_start 18133 "$WORK/gone.json"
out="$(FACTORY_BASE=http://127.0.0.1:18133 "$SET" payer 9 review 2>&1)"
rc=$?
fake_planning_stop
assert_eq "an unknown checkpoint exits 1" "1" "$rc"
assert_contains "reports the code it got" "refused not_found" "$out"
if grep -q 'planning_transition_not_allowed' <<< "$out"; then
  notok "reported a transition refusal for an error that was not one"
else
  ok "does not dress every failure as a transition refusal"
fi

exit "$TESTS_FAILED"
