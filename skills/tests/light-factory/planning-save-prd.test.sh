#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
SAVE="$LF_SCRIPTS/planning-save-prd"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/2.md" <<'MD'
# Checkpoint 2: The dashboard finishes a parked live payer

Project: payer
Status: planned

## Slice
The office fills a field into Settings and the payer goes live.
Today that takes a developer and an afternoon.

## Decisions
D1. The field is on the existing Settings page. Cited: web/src/settings.tsx:40

## Credentials
none
MD

SAVED='{"project":"payer","number":2,"title":"The dashboard finishes a parked live payer","summary":"The office fills a field into Settings and the payer goes live.","status":"planned"}'
cat > "$WORK/rules.json" <<JSON
[{"method": "PUT", "path_contains": "/checkpoints/2", "status": 200, "body": $SAVED}]
JSON

# 1. Usage and local validation.
"$SAVE" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"
FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer two "$WORK/2.md" >/dev/null 2>&1
assert_eq "a checkpoint number that is not an integer exits 2" "2" "$?"

# 2. A PRD with no heading cannot be filed under a name.
printf '## Slice\nA thing.\n' > "$WORK/headless.md"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 2 "$WORK/headless.md" 2>&1)"
assert_eq "a PRD with no heading exits 2" "2" "$?"
assert_contains "says what the heading must look like" "# Checkpoint <n>: <name>" "$out"

# 3. The number in the heading must be the number being written. A PUT
#    replaces the whole row, so saving checkpoint 2's text onto 3 erases 3.
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SAVE" payer 3 "$WORK/2.md" 2>&1)"
assert_eq "a heading number that disagrees exits 2" "2" "$?"
assert_contains "names both numbers" "checkpoint 2" "$out"
assert_contains "says why it refuses" "overwrite" "$out"

# 4. The real save: title from the heading, summary from the first sentence of
#    ## Slice, body the whole file.
fake_planning_start 18111 "$WORK/rules.json"
out="$(FACTORY_BASE=http://127.0.0.1:18111 "$SAVE" payer 2 "$WORK/2.md" 2>/dev/null)"
rc=$?
body="$(sent PUT "/checkpoints/2" 1)"
fake_planning_stop

assert_eq "saving exits 0" "0" "$rc"
assert_contains "title comes from the heading, without the number" \
  '"title":"The dashboard finishes a parked live payer"' "$body"
assert_contains "summary is the first sentence of ## Slice" \
  '"summary":"The office fills a field into Settings and the payer goes live."' "$body"
if grep -q 'Today that takes a developer' <<< "$(printf '%s' "$body" | jq -r '.summary')"; then
  notok "the summary ran on past the first sentence"
else
  ok "the summary stops at the first sentence"
fi
assert_contains "the body is the whole file" "web/src/settings.tsx:40" "$body"
assert_contains "prints what the server stored back" "The dashboard finishes" "$out"

# 5. An opening sentence over the server's 240 byte summary bound is cut to
#    fit rather than costing the whole PUT.
{
  echo "# Checkpoint 4: Long winded"
  echo
  echo "## Slice"
  printf 'The office '
  for _ in $(seq 1 60); do printf 'fills a field '; done
  echo "and the payer goes live."
} > "$WORK/4.md"
cat > "$WORK/long.json" <<'JSON'
[{"method": "PUT", "path_contains": "/checkpoints/4", "status": 200,
  "body": {"project": "payer", "number": 4, "title": "Long winded", "status": "planned"}}]
JSON
fake_planning_start 18112 "$WORK/long.json"
note="$(FACTORY_BASE=http://127.0.0.1:18112 "$SAVE" payer 4 "$WORK/4.md" 2>&1 >/dev/null)"
long_body="$(sent PUT "/checkpoints/4" 1)"
fake_planning_stop
summary_bytes="$(printf '%s' "$long_body" | jq -r '.summary' | wc -c | tr -d ' ')"
if [ "$summary_bytes" -le 241 ]; then
  ok "an oversized first sentence is cut to the server's 240 byte bound"
else
  notok "sent a $summary_bytes byte summary; the server refuses over 240"
fi
assert_contains "says that it cut the summary" "cut to fit" "$note"
if grep -q ' $\|fills a fiel"' <<< "$(printf '%s' "$long_body" | jq -r '.summary')"; then
  notok "the cut landed inside a word"
else
  ok "the cut lands on a word boundary"
fi

exit "$TESTS_FAILED"
