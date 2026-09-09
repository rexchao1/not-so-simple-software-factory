#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPTS="$(cd "$HERE/../../skills/dark-factory/scripts" && pwd -P)"
. "$HERE/lib.sh"
ANS="$SCRIPTS/factory-answer"

# factory-answer holds no local state and reads no project map. It needs
# nothing on disk.

"$ANS" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"

# An uppercase UUID is rejected by validUUID server side with
# 400 invalid_answer_identity. Catch it locally instead.
out="$(FACTORY_BASE=http://127.0.0.1:1 "$ANS" B22E40AA-B348-4738-A7A1-051D1C046F72 'go with throwing' 2>&1)"
assert_eq "uppercase work id exits 2" "2" "$?"
assert_contains "explains the lowercase rule" "lowercase" "$out"

# An answer over 8 KiB is a 413 server side. Refuse locally.
big="$(head -c 9000 /dev/zero | tr '\0' 'x')"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$ANS" b22e40aa-b348-4738-a7a1-051d1c046f72 "$big" 2>&1)"
assert_eq "oversized answer exits 2" "2" "$?"
assert_contains "names the 8 KiB limit" "8" "$out"

# A successful answer.
RESP='{"id":"a1","work_id":"b22e40aa-b348-4738-a7a1-051d1c046f72","request_id":"11111111-1111-4111-8111-111111111111","message":"throw","accepted_at":"2026-08-25T01:00:00Z"}'
fake_api_start 18087 "200" "$RESP"
out="$(FACTORY_BASE=http://127.0.0.1:18087 "$ANS" b22e40aa-b348-4738-a7a1-051d1c046f72 'throw a TypeError' 2>/dev/null)"
rc=$?
sent="$(cat "$FAKE_LOG")"
fake_api_stop

assert_eq "success exits 0" "0" "$rc"
assert_contains "posts to the answer route" "/api/v1/work/b22e40aa-b348-4738-a7a1-051d1c046f72/answer" "$sent"
assert_contains "sends the message" "throw a TypeError" "$sent"
if grep -qE '"request_id":"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"' <<< "$sent"; then
  ok "generates a lowercase hex request_id"
else
  notok "request_id must be a lowercase UUID: validUUID rejects uppercase"
fi

# --list must name the task from the RUN, not from a session field. A session
# has no title: the keys are fixed by protocol.Session and title is not one.
# One canned body answers both the list and the detail request.
BOTH='{"runs":[{"id":"aaaaaaaa-1111-4111-8111-111111111111","needs_input_count":1}],"run":{"id":"aaaaaaaa-1111-4111-8111-111111111111","task":{"name":"Add input validation to greet (deadbeef)","submitted_name":"Add input validation to greet"}},"sessions":[{"id":"b22e40aa-b348-4738-a7a1-051d1c046f72","state":"needs-input","question":"Should an empty name throw or return a default?"}]}'
fake_api_start 18088 "200" "$BOTH"
lst="$(FACTORY_BASE=http://127.0.0.1:18088 "$ANS" --list 2>/dev/null)"
rcl=$?
fake_api_stop
assert_eq "--list exits 0" "0" "$rcl"
assert_contains "--list prints the work id" "b22e40aa" "$lst"
assert_contains "--list prints the question" "empty name throw" "$lst"
assert_contains "--list names the task from the run" "Add input validation to greet" "$lst"
if grep -q '(deadbeef)' <<< "$lst"; then
  notok "--list printed tasks.name with its request-key suffix"
else
  ok "--list does not print the request-key suffix"
fi
if grep -q '(untitled)' <<< "$lst"; then
  notok "--list read a session field that does not exist"
else
  ok "--list found a real task name"
fi

# Nothing waiting is a sentence, not an empty page.
fake_api_start 18089 "200" '{"runs":[]}'
none="$(FACTORY_BASE=http://127.0.0.1:18089 "$ANS" --list 2>/dev/null)"
fake_api_stop
assert_contains "says so when nothing is waiting" "nothing is waiting" "$none"

exit "$TESTS_FAILED"
