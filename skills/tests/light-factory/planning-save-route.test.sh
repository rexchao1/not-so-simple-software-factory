#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SAVE="$LF_SCRIPTS/planning-save-route"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/route.md" <<'MD'
# Route: a new payer onboards itself

## Boulder
Give it a new payer portal and it works, unattended.
Today every payer costs a week of hand holding.

## Checkpoints
1. The dashboard lists payers: the office sees every payer and its state without asking anyone.
2. The dashboard finishes a parked live payer: the office fills a field into Settings.
3. Onboarding runs unattended

## Not on the route
Retiring the broker: it works, and nothing here needs it to change.
MD

# 1. Usage.
"$SAVE" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
"$SAVE" 'Not A Slug!' "$WORK/route.md" >/dev/null 2>&1
assert_eq "a project that is not a slug exits 2" "2" "$?"

# 2. A route with no numbered lines is a parse failure here, not a 400 there.
printf '# Route: nothing\n\n## Boulder\nA thing.\n' > "$WORK/bare.md"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer "$WORK/bare.md" 2>&1)"
assert_eq "a route with no checkpoint lines exits 2" "2" "$?"
assert_contains "says what is missing" "Checkpoints" "$out"

# 3. The real path. Checkpoint 1 already has a row, 2 and 3 do not.
CP1='{"project":"payer","number":1,"title":"The dashboard lists payers","summary":"already here","status":"frozen","body":"# Checkpoint 1: ..."}'
cat > "$WORK/rules.json" <<JSON
[
  {"method": "PUT", "path_contains": "/planning/projects/payer", "status": 200,
   "body": {"project": "payer", "title": "a new payer onboards itself", "checkpoints": 3}},
  {"method": "GET", "path_contains": "/checkpoints/1", "status": 200, "body": $CP1},
  {"method": "GET", "path_contains": "/checkpoints/2", "status": 404,
   "body": {"error": {"code": "not_found", "message": "no such checkpoint"}}},
  {"method": "GET", "path_contains": "/checkpoints/3", "status": 404,
   "body": {"error": {"code": "not_found", "message": "no such checkpoint"}}}
]
JSON
fake_planning_start 18101 "$WORK/rules.json"
out="$(FACTORY_BASE=http://127.0.0.1:18101 "$SAVE" payer "$WORK/route.md" 2>&1)"
rc=$?
project_body="$(sent PUT "/planning/projects/payer" 1)"
cp2="$(sent PUT "/checkpoints/2" 1)"
cp3="$(sent PUT "/checkpoints/3" 1)"
cp1_writes="$(request_count PUT "/checkpoints/1")"
fake_planning_stop

assert_eq "a route saves and exits 0" "0" "$rc"

# The project carries the title, the Boulder section, and the whole file.
assert_contains "project title comes from the # Route line" \
  '"title":"a new payer onboards itself"' "$project_body"
assert_contains "statement comes from the ## Boulder section" \
  "Give it a new payer portal" "$project_body"
if grep -q '## Not on the route' <<< "$project_body"; then
  ok "the whole route body is saved, not only the parts it parsed"
else
  notok "the route body was truncated to the sections the script understood"
fi
if grep -q '"statement":"[^"]*Checkpoints' <<< "$project_body"; then
  notok "the Boulder section ran past its own heading into ## Checkpoints"
else
  ok "the Boulder section stops at the next heading"
fi

# A row is opened for every numbered line that had none, with the title before
# the colon and the summary after it.
assert_contains "checkpoint 2 takes its title from the line" \
  '"title":"The dashboard finishes a parked live payer"' "$cp2"
assert_contains "checkpoint 2 takes its summary from the rest of the line" \
  '"summary":"the office fills a field into Settings."' "$cp2"
assert_contains "a new checkpoint row is saved with an empty body" '"body":""' "$cp2"
assert_contains "a line with no colon is all title" \
  '"title":"Onboarding runs unattended"' "$cp3"
assert_contains "a line with no colon has no summary" '"summary":""' "$cp3"

# The one rule that matters: an existing row is left alone. A PUT here would
# blank the PRD that was written since, because every write is a whole
# resource replace.
assert_eq "an existing checkpoint row is never rewritten" "0" "$cp1_writes"
assert_contains "says which rows it kept" "kept    1" "$out"
assert_contains "says which rows it created" "created 2" "$out"

# 4. A GET that fails for any reason other than 404 must not become a create.
cat > "$WORK/broken.json" <<'JSON'
[
  {"method": "PUT", "path_contains": "/planning/projects/payer", "status": 200,
   "body": {"project": "payer"}},
  {"method": "GET", "path_contains": "/checkpoints/", "status": 503,
   "body": {"error": {"code": "storage_unavailable", "message": "database is unavailable"}}}
]
JSON
fake_planning_start 18102 "$WORK/broken.json"
out2="$(FACTORY_BASE=http://127.0.0.1:18102 "$SAVE" payer "$WORK/route.md" 2>&1)"
rc2=$?
creates="$(request_count PUT "/checkpoints/")"
fake_planning_stop
assert_eq "a storage failure exits 1" "1" "$rc2"
assert_contains "the storage failure is reported, not swallowed" "storage_unavailable" "$out2"
assert_eq "never creates a row on top of an unreadable one" "0" "$creates"

exit "$TESTS_FAILED"
