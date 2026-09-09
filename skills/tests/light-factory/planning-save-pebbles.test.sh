#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SAVE="$LF_SCRIPTS/planning-save-pebbles"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
TASKS="$WORK/tasks"
mkdir -p "$TASKS"

cat > "$TASKS/01-driver.md" <<'MD'
## Write the driver

### What are we building?
The driver comes back, so the payer can be talked to at all.
MD
cat > "$TASKS/02-publish.md" <<'MD'
## Publish it

### What are we building?
The driver ships behind the existing feature flag.
MD
cat > "$TASKS/03-closure.md" <<'MD'
## Close the checkpoint

### What are we building?
Run the acceptance checks and write the decision record.
MD

STORED='{"project":"payer","number":2,"status":"frozen","stones":[{"id":"b1","ordinal":1,"title":"Rebuild the driver"}],"pebbles":[{"ordinal":1,"slug":"01-driver","title":"Write the driver","stone_id":"b1"}]}'
cat > "$WORK/rules.json" <<JSON
[
  {"method": "GET", "path_contains": "/checkpoints/2", "status": 200,
   "body": {"project": "payer", "number": 2, "title": "The dashboard finishes a parked live payer",
            "status": "frozen"}},
  {"method": "PUT", "path_contains": "/checkpoints/2/pebbles", "status": 200, "body": $STORED}
]
JSON

# 1. Usage and local checks.
"$SAVE" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
mkdir -p "$WORK/empty"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$WORK/empty" 2>&1)"
assert_eq "a directory with no tasks exits 2" "2" "$?"
assert_contains "says what a task file is called" "NN-slug.md" "$out"

mkdir -p "$WORK/badnames" && printf '## A task\n' > "$WORK/badnames/driver.md"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$WORK/badnames" 2>&1)"
assert_eq "a task file with no NN prefix exits 2" "2" "$?"
assert_contains "names the file it refused" "driver.md" "$out"

# 2. No stones.json: one stone holding all of them, named for the
#    checkpoint, because the page draws stones and a cut made before anyone
#    grouped it still has to render as one chunk.
fake_planning_start 18141 "$WORK/rules.json"
out="$(FACTORY_BASE=http://127.0.0.1:18141 "$SAVE" payer 2 "$TASKS" 2>/dev/null)"
rc=$?
batch="$(sent PUT "/checkpoints/2/pebbles" 1)"
fake_planning_stop

assert_eq "saving exits 0" "0" "$rc"
assert_eq "one stone when there is no manifest" "1" "$(jq '.stones | length' <<< "$batch")"
assert_eq "the stone is named for the checkpoint" \
  "The dashboard finishes a parked live payer" "$(jq -r '.stones[0].title' <<< "$batch")"
assert_eq "every task is in it" "3" \
  "$(jq '[.pebbles[] | select(.stone_id == "b1")] | length' <<< "$batch")"
assert_eq "three pebbles, in file order" "01-driver,02-publish,03-closure" \
  "$(jq -r '[.pebbles[].slug] | join(",")' <<< "$batch")"
assert_eq "the ordinal comes from NN" "1,2,3" \
  "$(jq -r '[.pebbles[].ordinal] | join(",")' <<< "$batch")"
assert_eq "the title comes from the ## heading" "Write the driver" \
  "$(jq -r '.pebbles[0].title' <<< "$batch")"
assert_contains "the body is the whole task file" "talked to at all" \
  "$(jq -r '.pebbles[0].body' <<< "$batch")"
assert_contains "reports the batch it saved" "1 stone(s), 1 pebble(s)" "$out"

# 3. With stones.json, in the shape the orchestrator's pebble pass wrote.
cat > "$TASKS/stones.json" <<'JSON'
{
  "checkpoint": 2,
  "stones": [
    {"id": "B1", "title": "Build it", "statement": "The build tasks.",
     "pebbles": ["01-driver", "02-publish"]},
    {"id": "B2", "title": "Close it", "statement": "The closure task.",
     "pebbles": ["03-closure"]}
  ]
}
JSON
fake_planning_start 18142 "$WORK/rules.json"
FACTORY_BASE=http://127.0.0.1:18142 "$SAVE" payer 2 "$TASKS" >/dev/null 2>&1
rc=$?
batch2="$(sent PUT "/checkpoints/2/pebbles" 1)"
reads="$(request_count GET "/checkpoints/2")"
fake_planning_stop

assert_eq "a manifest saves and exits 0" "0" "$rc"
assert_eq "the manifest's stones are sent" "2" "$(jq '.stones | length' <<< "$batch2")"
assert_eq "stone ids are folded to slugs" "b1,b2" \
  "$(jq -r '[.stones[].id] | join(",")' <<< "$batch2")"
assert_eq "the stone statement survives" "The build tasks." \
  "$(jq -r '.stones[0].statement' <<< "$batch2")"
# One representation of the link: the pebble names its stone, the stone
# does not list its pebbles, so the two halves cannot disagree.
assert_eq "each pebble names its stone" "b1,b1,b2" \
  "$(jq -r '[.pebbles[].stone_id] | join(",")' <<< "$batch2")"
if jq -e '.stones[] | has("pebbles")' >/dev/null 2>&1 <<< "$batch2"; then
  notok "sent the manifest's own pebbles list: the API has one link, on the pebble"
else
  ok "does not send a stone's pebble list back"
fi
assert_eq "a manifest means the checkpoint title is not needed" "0" "$reads"
assert_eq "stones and pebbles go in one request" "1" \
  "$(request_count PUT "/checkpoints/2/pebbles")"

# 4. A manifest naming a task nobody wrote is a real error: the split and the
#    tasks disagree, and guessing which is right is not this script's call.
cat > "$TASKS/stones.json" <<'JSON'
{"stones": [{"id": "B1", "title": "Build it", "pebbles": ["01-driver", "99-ghost"]}]}
JSON
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$TASKS" 2>&1)"
assert_eq "a manifest naming a missing task exits 2" "2" "$?"
assert_contains "names the task that is not there" "99-ghost" "$out"

# 5. A task the manifest forgets is kept and left ungrouped. The server puts
#    it in a catch-all, so no work is hidden by a grouping.
cat > "$TASKS/stones.json" <<'JSON'
{"stones": [{"id": "B1", "title": "Build it", "pebbles": ["01-driver"]}]}
JSON
fake_planning_start 18143 "$WORK/rules.json"
FACTORY_BASE=http://127.0.0.1:18143 "$SAVE" payer 2 "$TASKS" >/dev/null 2>&1
batch3="$(sent PUT "/checkpoints/2/pebbles" 1)"
fake_planning_stop
assert_eq "a forgotten task is still sent" "3" "$(jq '.pebbles | length' <<< "$batch3")"
assert_eq "a forgotten task names no stone" "2" \
  "$(jq '[.pebbles[] | select(has("stone_id") | not)] | length' <<< "$batch3")"

exit "$TESTS_FAILED"
