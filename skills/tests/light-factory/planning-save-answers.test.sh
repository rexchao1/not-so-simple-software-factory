#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SAVE="$LF_SCRIPTS/planning-save-answers"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
printf 'Q1: rotate weekly.\nQ2: no SSO, the office signs in with a shared account.\n' > "$WORK/answers.md"

cat > "$WORK/rules.json" <<'JSON'
[{"method": "PUT", "path_contains": "/checkpoints/2/answers", "status": 200,
  "body": {"project": "payer", "number": 2, "status": "review",
           "answers": "Q1: rotate weekly.\nQ2: no SSO, the office signs in with a shared account."}}]
JSON

# 1. Usage.
"$SAVE" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$WORK/nope.md" >/dev/null 2>&1
assert_eq "a missing answers file exits 2" "2" "$?"

# 2. An empty file is refused. Every write is a whole resource replace, so
#    saving nothing would erase the answers already stored.
: > "$WORK/empty.md"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$WORK/empty.md" 2>&1)"
assert_eq "an empty answers file exits 2" "2" "$?"
assert_contains "says why an empty file is refused" "erase" "$out"

# 3. The save itself.
fake_planning_start 18121 "$WORK/rules.json"
out="$(FACTORY_BASE=http://127.0.0.1:18121 "$SAVE" payer 2 "$WORK/answers.md" 2>/dev/null)"
rc=$?
body="$(sent PUT "/checkpoints/2/answers" 1)"
path_used="$(jq -r 'select(.method == "PUT") | .path' "$FAKE_REQUESTS" | head -n1)"
fake_planning_stop

assert_eq "saving exits 0" "0" "$rc"
assert_eq "writes to the answers route, not over the checkpoint" \
  "/api/v1/planning/projects/payer/checkpoints/2/answers" "$path_used"
assert_contains "sends the file under the answers key" '"answers":' "$body"
assert_contains "sends the whole file, both answers" "no SSO" "$body"
if grep -q '"body"\|"title"\|"summary"' <<< "$body"; then
  notok "sent a field the answers route does not have: unknown fields are 400 malformed_json"
else
  ok "sends nothing but answers"
fi
assert_contains "reports what was saved" "answers saved for payer checkpoint 2" "$out"

exit "$TESTS_FAILED"
