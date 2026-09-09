#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SHOW="$LF_SCRIPTS/planning-show"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The roadmap shape is internal/controlplane/roadmap.go: cost and pass rounds
# are joined onto a checkpoint there and nowhere else, which is why the
# summary is read from /api/v1/roadmap rather than from the planning routes.
cat > "$WORK/rules.json" <<'JSON'
[
  {"method": "GET", "path": "/api/v1/roadmap", "status": 200, "body": {
    "configured": true,
    "read_at": "2026-09-07T12:00:00Z",
    "projects": [{
      "project": "payer",
      "title": "a new payer onboards itself",
      "statement": "Give it a new payer portal and it works, unattended.",
      "cost_usd": 1.2345,
      "built_count": 1,
      "checkpoints": [
        {"number": 1, "title": "The dashboard lists payers", "status": "built",
         "planned": true, "stones": [], "pebbles": [], "passes": [],
         "cost_usd": 0.5, "pass_rounds": 2},
        {"number": 2, "title": "The dashboard finishes a parked live payer",
         "summary": "the office fills a field into Settings", "status": "review",
         "planned": true, "cost_usd": 0.84, "pass_rounds": 3,
         "stones": [{"id": "b1", "title": "Rebuild the driver", "state": "running",
                     "pebbles": []}],
         "pebbles": [{"ordinal": 1, "slug": "01-driver", "title": "Write the driver",
                      "state": "running", "work_id": "w_123"}],
         "passes": [{"at": "2026-09-07T11:00:00Z", "mode": "critique", "round": 1,
                     "cost_usd": 0.42}],
         "live": {"mode": "critique", "round": 3, "started": "2026-09-07T11:55:00Z"}}
      ]
    }],
    "waiting": [{"project": "payer", "number": 2, "title": "The dashboard finishes a parked live payer",
                 "status": "review", "reason": "in review with no answers saved",
                 "action": "render the review and save the answers",
                 "cost_usd": 0.84, "pass_rounds": 3}]
  }},
  {"method": "GET", "path_contains": "/checkpoints/2", "status": 200, "body": {
    "project": "payer", "number": 2, "status": "review",
    "title": "The dashboard finishes a parked live payer",
    "body": "# Checkpoint 2: The dashboard finishes a parked live payer\n\n## Slice\nThe office fills a field.",
    "answers": "Q1: rotate weekly."}}
]
JSON

# 1. Usage.
"$SHOW" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"

# 2. The whole project.
fake_planning_start 18151 "$WORK/rules.json"
out="$(FACTORY_BASE=http://127.0.0.1:18151 "$SHOW" payer 2>/dev/null)"
rc=$?
body_reads="$(request_count GET "/checkpoints/")"
fake_planning_stop

assert_eq "showing a project exits 0" "0" "$rc"
assert_contains "names the project" "a new payer onboards itself" "$out"
assert_contains "lists every checkpoint" "The dashboard lists payers" "$out"
assert_contains "prints each status" "built" "$out"
assert_contains "prints the critic rounds" "3 round(s)" "$out"
assert_contains "prints the cost of a checkpoint" "0.84" "$out"
assert_contains "prints the cost of the project" '$1.23' "$out"
assert_contains "says how many checkpoints are built" "built 1/2" "$out"
assert_contains "names a pass that is running now" "critique round 3 is running now" "$out"
assert_contains "surfaces what the human is holding up" "in review with no answers saved" "$out"
assert_eq "the whole-project view does not read a PRD body" "0" "$body_reads"
if grep -q 'null' <<< "$out"; then
  notok "printed a null: a field was read from the wrong level of the roadmap"
else
  ok "prints no nulls"
fi

# 3. One checkpoint adds the body and the answers, which the roadmap does not
#    carry.
fake_planning_start 18152 "$WORK/rules.json"
one="$(FACTORY_BASE=http://127.0.0.1:18152 "$SHOW" payer 2 2>/dev/null)"
rc2=$?
fake_planning_stop
assert_eq "showing one checkpoint exits 0" "0" "$rc2"
assert_contains "prints the PRD body" "The office fills a field." "$one"
assert_contains "prints the saved answers" "Q1: rotate weekly." "$one"
assert_contains "still prints the cost" "0.84" "$one"
if grep -q 'The dashboard lists payers' <<< "$one"; then
  notok "asked for one checkpoint and got the whole route"
else
  ok "one checkpoint is one checkpoint"
fi

# 4. An unknown project says so and lists the ones there are, rather than
#    printing an empty summary that reads like a project with no work in it.
fake_planning_start 18153 "$WORK/rules.json"
out4="$(FACTORY_BASE=http://127.0.0.1:18153 "$SHOW" nosuch 2>&1)"
rc4=$?
fake_planning_stop
assert_eq "an unknown project exits 2" "2" "$rc4"
assert_contains "lists the projects there are" "payer" "$out4"

exit "$TESTS_FAILED"
