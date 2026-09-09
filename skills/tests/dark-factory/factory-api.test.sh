#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPTS="$(cd "$HERE/../../skills/dark-factory/scripts" && pwd -P)"
. "$HERE/lib.sh"
API="$SCRIPTS/factory-api"

# 1. Usage error when called with no arguments.
"$API" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"

# 2. Unreachable server is exit 3, distinct from an HTTP error.
FACTORY_BASE="http://127.0.0.1:1" "$API" GET /api/v1/overview >/dev/null 2>&1
assert_eq "unreachable server exits 3" "3" "$?"

# 3. A 2xx returns the body on stdout and exits 0.
fake_api_start 18081 "200" '{"runs":[]}'
body="$(FACTORY_BASE=http://127.0.0.1:18081 "$API" GET /api/v1/runs 2>/dev/null)"
assert_eq "2xx exits 0" "0" "$?"
assert_eq "2xx returns the body" '{"runs":[]}' "$body"

# 4. The GET it actually sent carries no Origin header. It carries no
#    Content-Type either, and must not: the factory parses one only where it
#    decodes a body, so sending it on a bodyless GET would be noise.
sent="$(cat "$FAKE_LOG")"
if grep -qi '^Origin:' <<< "$sent"; then
  notok "must not send an Origin header: validateMutationOrigin rejects a foreign one"
else
  ok "sends no Origin header on a GET"
fi
if grep -qi '^Content-Type:' <<< "$sent"; then
  notok "sent Content-Type on a bodyless GET"
else
  ok "sends no Content-Type on a bodyless GET"
fi
fake_api_stop

# 5. A 4xx exits 1 and reports the factory's error code on stderr. This is also
#    the request that carries a body, so it is the one that must declare
#    Content-Type: decodeJSON answers 415 unless it parses to exactly
#    application/json.
fake_api_start 18082 "400" '{"error":{"code":"invalid_source","message":"source must be orchestrator, cockpit, or github"}}'
err="$(FACTORY_BASE=http://127.0.0.1:18082 "$API" POST /api/v1/work '{}' 2>&1 >/dev/null)"
assert_eq "4xx exits 1" "1" "$?"
assert_contains "4xx surfaces the error code" "invalid_source" "$err"
assert_contains "4xx surfaces the message" "source must be orchestrator" "$err"
posted="$(cat "$FAKE_LOG")"
assert_contains "a request with a body declares Content-Type: application/json" "Content-Type: application/json" "$posted"
if grep -qi '^Origin:' <<< "$posted"; then
  notok "must not send an Origin header on a mutation either"
else
  ok "sends no Origin header on a POST"
fi
fake_api_stop

# 6. This repo ships no address for anyone else's factory server: with
#    FACTORY_BASE unset, the script refuses as a usage error rather than
#    defaulting to some baked-in host.
out="$(env -u FACTORY_BASE "$API" GET /api/v1/runs 2>&1)"; rc=$?
assert_eq "unset FACTORY_BASE exits 2" "2" "$rc"
assert_contains "unset FACTORY_BASE names itself in the error" "FACTORY_BASE" "$out"

exit "$TESTS_FAILED"
