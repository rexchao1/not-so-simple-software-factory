#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
CRITIC="$LF_SCRIPTS/critic-submit"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The dark factory's project map, kept off the real one.
printf 'payer\tgithub.com/octocat/payer\t\n' > "$WORK/projects.tsv"
export FACTORY_PROJECTS="$WORK/projects.tsv"
# Do not sleep between polls, and do not wait out an hour if a fake ever
# stops answering.
export CRITIC_POLL_SECONDS=0
export CRITIC_TIMEOUT_SECONDS=5

CHECKPOINT='{"project":"payer","number":2,"status":"review","title":"The dashboard finishes a parked live payer","body":"# Checkpoint 2: The dashboard finishes a parked live payer\n\n## Slice\nThe office fills a field into Settings."}'
PIPELINES='{"pipelines":[{"id":"7ad8c32a-1f0e-4b2a-9c31-2d5f4a6b8e10","name":"Implement, review, deliver"},{"id":"c11c9d4e-3a2b-4c5d-8e6f-7a8b9c0d1e2f","name":"Critique"}]}'
ADMITTED='{"run_id":"5edf217d-53dc-4099-ad56-55e1f27bdd68","task_id":"3bf8ba22-935d-4ef8-9445-9139c3413fc7","work_ids":["b22e40aa-b348-4738-a7a1-051d1c046f72"],"state":"queued","source":"orchestrator"}'
FINDINGS='{"round":1,"verdict":"revise","findings":[{"id":"F1","kind":"uncited","text":"D2 has no citation"}]}'

# One run detail per state, keyed on the same Work id the admission returned.
run_state() {
  jq -nc --arg s "$1" --arg r "${2:-}" '{
    run: {id: "5edf217d-53dc-4099-ad56-55e1f27bdd68", state: "running",
          source: "orchestrator", task: {submitted_name: "Critique"}},
    sessions: [({id: "b22e40aa-b348-4738-a7a1-051d1c046f72", state: $s}
                + (if $r == "" then {} else {result: $r} end))]}'
}

# factory-submit checks that the repository is managed before it submits, and
# registers it when it is not, so the fake has to answer that too. payer is
# already registered here: this suite is about the critique loop, and the
# registration path has its own tests next door.
REPOSITORIES='{"repositories":[{"id":"c0ffee00-0000-4000-8000-000000000001","remote_identity":"github.com/octocat/payer","enabled":true,"default_delivery":"pr"}]}'

rules_for() { # rules_for <file> <terminal state> [result]
  local f="$1" terminal="$2" result="${3:-}"
  jq -n --argjson cp "$CHECKPOINT" --argjson pipes "$PIPELINES" --argjson adm "$ADMITTED" \
        --argjson repos "$REPOSITORIES" \
        --argjson running "$(run_state running)" --argjson ended "$(run_state "$terminal" "$result")" '
    [{method: "GET", path_contains: "/planning/projects/payer/checkpoints/2", status: 200, body: $cp},
     {method: "GET", path_contains: "/api/v1/pipelines", status: 200, body: $pipes},
     {method: "GET", path_contains: "/api/v1/repositories", status: 200, body: $repos},
     {method: "POST", path_contains: "/api/v1/work", status: 201, body: $adm},
     {method: "GET", path_contains: "/api/v1/runs/", status: 200, body: $running, times: 2},
     {method: "GET", path_contains: "/api/v1/runs/", status: 200, body: $ended}]' > "$f"
}

# 1. Usage. The round is half the join key the roadmap uses, so it is never
#    guessed.
"$CRITIC" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$CRITIC" payer 2 2>&1)"
assert_eq "a missing --round exits 2" "2" "$?"
assert_contains "says the round is required" "--round" "$out"

# 2. The happy path: read the PRD, submit it, poll, print the findings.
rules_for "$WORK/ok.json" ready "$FINDINGS"
fake_planning_start 18161 "$WORK/ok.json"
out="$(FACTORY_BASE=http://127.0.0.1:18161 "$CRITIC" payer 2 --round 1 2>"$WORK/err")"
rc=$?
admission="$(sent POST "/api/v1/work" 1)"
polls="$(request_count GET "/api/v1/runs/")"
progress="$(cat "$WORK/err")"
fake_planning_stop

assert_eq "a succeeded critique exits 0" "0" "$rc"
assert_eq "the findings JSON is the only thing on stdout" "$FINDINGS" "$out"

# The contract with the factory: the planning triple and the Critique
# pipeline. Without the triple the roadmap cannot join this pass to its
# checkpoint, and the round would be filed under whatever it guessed.
assert_contains "the payload carries the planning project" '"project":"payer"' \
  "$(jq -c '.planning' <<< "$admission")"
assert_eq "the payload carries the checkpoint number" "2" "$(jq '.planning.number' <<< "$admission")"
assert_eq "the payload carries the round" "1" "$(jq '.planning.round' <<< "$admission")"
assert_eq "the payload names the Critique pipeline by its resolved id" \
  "c11c9d4e-3a2b-4c5d-8e6f-7a8b9c0d1e2f" "$(jq -r '.pipeline_id' <<< "$admission")"
assert_contains "the PRD read from the server is the spec" "fills a field into Settings" "$admission"
assert_contains "the Work is named for the checkpoint and round" "round 1" \
  "$(jq -r '.name' <<< "$admission")"

# Polling reports movement rather than sitting silent for minutes.
assert_contains "one line per state change, on stderr" "state   running" "$progress"
assert_contains "the terminal state is announced" "state   ready" "$progress"
assert_contains "the Work is identified while it waits" "5edf217d" "$progress"
assert_eq "polls until terminal and then stops" "3" "$polls"

# 3. Every terminal state stops the poll. ready, succeeded and no-change are
#    the three the run aggregate counts as successful.
for state in succeeded no-change; do
  rules_for "$WORK/$state.json" "$state" "$FINDINGS"
  fake_planning_start 18162 "$WORK/$state.json"
  o="$(FACTORY_BASE=http://127.0.0.1:18162 "$CRITIC" payer 2 --round 2 2>/dev/null)"
  r=$?
  p="$(request_count GET "/api/v1/runs/")"
  fake_planning_stop
  assert_eq "$state exits 0" "0" "$r"
  assert_eq "$state stops the poll" "3" "$p"
  assert_eq "$state prints the result" "$FINDINGS" "$o"
done

# 4. A Work that failed is a failure here, even though the request that read
#    it succeeded. The findings, if any, are still printed.
rules_for "$WORK/failed.json" failed ""
fake_planning_start 18163 "$WORK/failed.json"
out4="$(FACTORY_BASE=http://127.0.0.1:18163 "$CRITIC" payer 2 --round 2 2>&1)"
rc4=$?
p4="$(request_count GET "/api/v1/runs/")"
fake_planning_stop
assert_eq "a failed Work exits non-zero" "1" "$rc4"
assert_contains "says the Work did not succeed" "did not succeed: failed" "$out4"
assert_eq "a failed Work stops the poll" "3" "$p4"

rules_for "$WORK/cancelled.json" cancelled ""
fake_planning_start 18164 "$WORK/cancelled.json"
FACTORY_BASE=http://127.0.0.1:18164 "$CRITIC" payer 2 --round 2 >/dev/null 2>&1
rc5=$?
p5="$(request_count GET "/api/v1/runs/")"
fake_planning_stop
assert_eq "a cancelled Work exits non-zero" "1" "$rc5"
assert_eq "a cancelled Work stops the poll" "3" "$p5"

# 5. needs-input is not terminal, but a critique must never ask, and waiting
#    out the timeout on a question nobody is watching for helps no one.
rules_for "$WORK/asking.json" needs-input ""
fake_planning_start 18165 "$WORK/asking.json"
out6="$(FACTORY_BASE=http://127.0.0.1:18165 "$CRITIC" payer 2 --round 2 2>&1)"
rc6=$?
p6="$(request_count GET "/api/v1/runs/")"
fake_planning_stop
assert_eq "a critique that asks a question exits non-zero" "1" "$rc6"
assert_contains "says a critique should not need input" "should not need input" "$out6"
assert_eq "a question stops the poll" "3" "$p6"

# 6. A checkpoint with no PRD saved is refused before anything is admitted.
jq -n '[{method: "GET", path_contains: "/checkpoints/2", status: 200,
         body: {project: "payer", number: 2, status: "planned", body: ""}}]' > "$WORK/nobody.json"
fake_planning_start 18166 "$WORK/nobody.json"
out7="$(FACTORY_BASE=http://127.0.0.1:18166 "$CRITIC" payer 2 --round 1 2>&1)"
rc7=$?
submits="$(request_count POST "/api/v1/work")"
fake_planning_stop
assert_eq "an unwritten PRD exits 2" "2" "$rc7"
assert_contains "says to save it first" "planning-save-prd" "$out7"
assert_eq "nothing is admitted for a PRD that was never saved" "0" "$submits"

exit "$TESTS_FAILED"
