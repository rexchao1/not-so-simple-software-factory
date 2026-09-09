#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$HERE/lib.sh"
PRE="$LF_SCRIPTS/preflight"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# A stand-in for the vault CLI. The real one prints hosts and the names of
# token keys, never values, so this one does too, and the tests check that no
# key name and no value reaches the output.
mkdir -p "$WORK/bin"
cat > "$WORK/bin/agent-vault" <<'VAULT'
#!/usr/bin/env bash
[ "$1 $2 $3" = "vault service list" ] || { echo "unexpected: $*" >&2; exit 64; }
cat <<'YAML'
services:
  - name: payer-api
    host: api.example.com
    keys:
      - key: api_token_key_name
  - name: mailer
    host: mail.example.com
YAML
VAULT
chmod +x "$WORK/bin/agent-vault"
export LIGHT_FACTORY_AGENT_VAULT="$WORK/bin/agent-vault"
# Never read the real ~/.factory/light-factory/vault.name.
export LIGHT_FACTORY_VAULT_FILE="$WORK/vault.name"

prd() { jq -n --arg b "$1" '[{method: "GET", path_contains: "/checkpoints/2", status: 200,
  body: {project: "payer", number: 2, status: "frozen", title: "A checkpoint", body: $b}}]'; }

COVERED='# Checkpoint 2: A checkpoint

## Credentials
api.example.com: the payer REST API. Never a value.
mail.example.com: delivery receipts. Never a value.

## Experiments
none
'
NOT_COVERED='# Checkpoint 2: A checkpoint

## Credentials
api.example.com: the payer REST API. Never a value.
billing.example.com: the invoice API. Never a value.
'
NONE='# Checkpoint 2: A checkpoint

## Credentials
none

## Experiments
none
'
# The shape that used to slip through: a line that starts with none and then
# explains itself. It was read as a hostname and looked up in the vault.
NONE_PROSE='# Checkpoint 2: A checkpoint

## Credentials
none. The app makes no network request and stores nothing off device.

## Experiments
none
'
PROSE='# Checkpoint 2: A checkpoint

## Credentials
The build needs no tokens of its own

## Experiments
none
'

# 1. Usage.
"$PRE" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"

# 2. A checkpoint that needs no credentials passes without a vault at all.
prd "$NONE" > "$WORK/none.json"
fake_planning_start 18171 "$WORK/none.json"
out="$(FACTORY_BASE=http://127.0.0.1:18171 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "no credentials exits 0" "0" "$rc"
assert_contains "says no credentials are needed" "needs no credentials" "$out"

# 3. Every host has a rule: one line per host, naming the rule that covers it.
prd "$COVERED" > "$WORK/covered.json"
fake_planning_start 18172 "$WORK/covered.json"
out="$(FACTORY_BASE=http://127.0.0.1:18172 "$PRE" payer 2 --vault clinic 2>&1)"
rc=$?
fake_planning_stop
assert_eq "every host covered exits 0" "0" "$rc"
assert_contains "one line per host, with its rule" "ok       api.example.com  rule payer-api" "$out"
assert_contains "and the second host" "ok       mail.example.com  rule mailer" "$out"
assert_contains "says the check passed" "every host has a rule in vault clinic" "$out"
# The check is against names. Nothing from the listing but hosts and rule
# names may reach the output; no credential value passes through the chain.
if grep -q 'api_token_key_name' <<< "$out"; then
  notok "printed a token key name out of the vault listing"
else
  ok "prints hosts and rule names only"
fi

# 4. A host with no rule is MISSING and fails. This is the gate: nothing is
#    submitted until the rule exists.
prd "$NOT_COVERED" > "$WORK/missing.json"
fake_planning_start 18173 "$WORK/missing.json"
out="$(FACTORY_BASE=http://127.0.0.1:18173 "$PRE" payer 2 --vault clinic 2>&1)"
rc=$?
fake_planning_stop
assert_eq "a host with no rule exits 1" "1" "$rc"
assert_contains "names the uncovered host" "MISSING  billing.example.com" "$out"
assert_contains "still reports the host that is covered" "ok       api.example.com" "$out"
assert_contains "says nothing gets submitted until it passes" "Nothing is submitted" "$out"

# 5. With hosts to check and no vault named, it stops rather than passing.
prd "$COVERED" > "$WORK/covered.json"
fake_planning_start 18174 "$WORK/covered.json"
out="$(env -u LIGHT_FACTORY_VAULT FACTORY_BASE=http://127.0.0.1:18174 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "no vault named exits 2" "2" "$rc"
assert_contains "says the three ways to name one" "LIGHT_FACTORY_VAULT" "$out"

# 6. The vault name file is read when nothing else names one.
echo "clinic" > "$WORK/vault.name"
fake_planning_start 18175 "$WORK/covered.json"
out="$(env -u LIGHT_FACTORY_VAULT FACTORY_BASE=http://127.0.0.1:18175 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "the vault name file is enough" "0" "$rc"
assert_contains "and it is the vault that was used" "vault clinic" "$out"

# 7. A none line that explains itself still means none. Freezing is one way,
#    so a checkpoint that froze with this wording could never pass otherwise.
prd "$NONE_PROSE" > "$WORK/none-prose.json"
fake_planning_start 18177 "$WORK/none-prose.json"
out="$(FACTORY_BASE=http://127.0.0.1:18177 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "none with a sentence after it exits 0" "0" "$rc"
assert_contains "still says no credentials are needed" "needs no credentials" "$out"

# 8. Prose that is not a none line is called malformed before a vault is even
#    named, rather than sent to the vault as a hostname.
prd "$PROSE" > "$WORK/prose.json"
fake_planning_start 18178 "$WORK/prose.json"
out="$(env -u LIGHT_FACTORY_VAULT FACTORY_BASE=http://127.0.0.1:18178 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "a sentence in the section exits 2" "2" "$rc"
assert_contains "says the line is malformed" "MALFORMED" "$out"
assert_contains "says what the section should look like" "one 'host: purpose' line per host" "$out"

# 9. A vault CLI with no session says so, and says whose job the login is.
cat > "$WORK/bin/agent-vault-logged-out" <<'VAULT'
#!/usr/bin/env bash
echo "Error: not logged in, run 'agent-vault auth login' first" >&2
exit 1
VAULT
chmod +x "$WORK/bin/agent-vault-logged-out"
prd "$COVERED" > "$WORK/covered.json"
fake_planning_start 18179 "$WORK/covered.json"
out="$(LIGHT_FACTORY_AGENT_VAULT="$WORK/bin/agent-vault-logged-out" \
       FACTORY_BASE=http://127.0.0.1:18179 "$PRE" payer 2 --vault clinic 2>&1)"
rc=$?
fake_planning_stop
assert_eq "a logged-out vault exits 1" "1" "$rc"
assert_contains "names the real reason" "not logged in" "$out"
assert_contains "says how to fix it" "agent-vault auth login" "$out"

# 10. A checkpoint with no PRD has nothing to check, and saying "ok" there
#    would be a pass nobody earned.
jq -n '[{method: "GET", path_contains: "/checkpoints/2", status: 200,
         body: {project: "payer", number: 2, status: "planned", body: ""}}]' > "$WORK/empty.json"
fake_planning_start 18176 "$WORK/empty.json"
out="$(FACTORY_BASE=http://127.0.0.1:18176 "$PRE" payer 2 2>&1)"
rc=$?
fake_planning_stop
assert_eq "no PRD exits 2" "2" "$rc"
assert_contains "says there is nothing to check" "nothing to check" "$out"

exit "$TESTS_FAILED"
