# The light factory's test harness.
#
# The reporters and the single-response fake come from the dark factory's
# lib.sh, unchanged: there is one set of assertions in this repo, and a second
# copy would drift. What is added here is a fake that answers differently per
# route and per call, which the planning scripts need and the dark factory's
# scripts did not:
#
#   - planning-save-route asks whether each checkpoint exists before it writes
#     one, so the same GET has to 404 for one number and 200 for another.
#   - critic-submit polls one Work until it is terminal, so the same GET has to
#     answer running, running, succeeded.
#
# Rules are matched in order. Each is {method, path, path_contains, status,
# body, times}. "times" makes a rule answer that many calls and then fall
# through to the next matching one, which is how a sequence is written. An
# unmatched request is 404 with a not_found envelope, the same shape the
# factory sends, so a script that mishandles a miss fails here the way it
# would there.
#
# Nothing in here reaches the real factory. Every test points FACTORY_BASE at
# a loopback port or a dead one.
set -uo pipefail

LF_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$LF_HERE/../dark-factory/lib.sh"

LF_SCRIPTS="$(cd "$LF_HERE/../../skills/light-factory/scripts" && pwd -P)"

# fake_planning_start <port> <rules.json>
fake_planning_start() {
  FAKE_PORT="$1"
  FAKE_LOG="$(mktemp)"
  # One JSON object per request, so a test can ask "what did the third PUT
  # send" without parsing a header dump.
  FAKE_REQUESTS="$(mktemp)"
  # The routed server lives in tests/fake-api.py so the dark-factory tests
  # can use the same one; a Work list and a run detail come from different
  # paths, which a single-response fake cannot express.
  FAKE_SERVE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)/fake-api.py"
  python3 "$FAKE_SERVE" "$FAKE_PORT" "$2" "$FAKE_LOG" "$FAKE_REQUESTS" &
  FAKE_PID=$!
  sleep 0.5
}

# The single-response fake's stop function reaps this one too.
fake_planning_stop() { fake_api_stop; }

# sent <method> <path substring> [nth]: the JSON body of a request the fake
# received, so an assertion names the request it is about.
sent() {
  jq -c -r --arg m "$1" --arg p "$2" \
    'select(.method == $m and (.path | contains($p))) | .body' "$FAKE_REQUESTS" \
    | sed -n "${3:-1}p"
}

# request_count <method> <path substring>
request_count() {
  jq -r --arg m "$1" --arg p "$2" \
    'select(.method == $m and (.path | contains($p))) | .path' "$FAKE_REQUESTS" | wc -l | tr -d ' '
}
