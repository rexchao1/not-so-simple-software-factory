#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPTS="$(cd "$HERE/../../skills/dark-factory/scripts" && pwd -P)"
. "$HERE/lib.sh"
SUB="$SCRIPTS/factory-submit"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/home"
printf 'scratch\tgithub.com/octocat/factory-scratch\t\n' > "$WORK/projects.tsv"
printf '## Add a farewell function\n\n### Done when\n- it works\n' > "$WORK/spec.md"

# The real map is ~/.factory/dark-factory/projects.tsv. FACTORY_PROJECTS is the
# override that keeps a test off it.
export FACTORY_PROJECTS="$WORK/projects.tsv"

# The installed skill directory is read only in practice: an install replaces
# it wholesale, so anything written inside is lost. Nothing here may write to
# it. Fingerprint it before the runs below and again at the end.
SKILL_DIR="$(cd "$SCRIPTS/.." && pwd -P)"
before="$(find "$SKILL_DIR" -type f | sort | xargs shasum | shasum)"

# 1. Usage.
"$SUB" >/dev/null 2>&1; assert_eq "no args exits 2" "2" "$?"

# 2. An unknown project fails locally, before any request.
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SUB" --project nope --name x --spec-file "$WORK/spec.md" 2>&1)"
assert_eq "unknown project exits 2" "2" "$?"
assert_contains "unknown project names the config file" "projects.tsv" "$out"

# 3. An oversized spec is refused locally, not by an opaque server error.
head -c 40000 /dev/zero | tr '\0' 'x' > "$WORK/big.md"
out="$(FACTORY_BASE=http://127.0.0.1:1 "$SUB" --project scratch --name x --spec-file "$WORK/big.md" 2>&1)"
assert_eq "oversized spec exits 2" "2" "$?"
assert_contains "oversized spec says so plainly" "too large" "$out"

# 4. A successful submission parses the response and records the pointer.
RESP='{"run_id":"5edf217d-53dc-4099-ad56-55e1f27bdd68","task_id":"3bf8ba22-935d-4ef8-9445-9139c3413fc7","work_ids":["b22e40aa-b348-4738-a7a1-051d1c046f72"],"state":"queued","source":"orchestrator"}'
fake_api_start 18085 "201" "$RESP"
out="$(FACTORY_BASE=http://127.0.0.1:18085 "$SUB" --project scratch --name 'Add a farewell function' --spec-file "$WORK/spec.md" 2>/dev/null)"
rc=$?
sent="$(cat "$FAKE_LOG")"
fake_api_stop

assert_eq "success exits 0" "0" "$rc"
assert_contains "prints the run id" "5edf217d" "$out"
assert_contains "prints the work id" "b22e40aa" "$out"
assert_contains "prints the state" "queued" "$out"

# 5. The payload it sent must satisfy the admission contract exactly.
assert_contains "sends runtime claude-code, never the codex default" '"runtime":"claude-code"' "$sent"
assert_contains "sends source orchestrator" '"source":"orchestrator"' "$sent"
assert_contains "defaults to pre_approved true" '"pre_approved":true' "$sent"
assert_contains "defaults to reviewed assurance" '"assurance":"reviewed"' "$sent"
if grep -q '"concurrency_limit"\|"execution_profile_id"' <<< "$sent"; then
  notok "sent a field AdmitWorkRequest does not have: DisallowUnknownFields makes it a 400"
else
  ok "sends no unknown fields"
fi
if grep -q '"delivery"' <<< "$sent"; then
  notok "sent delivery: the project's server side default_delivery must decide, not a prompt"
else
  ok "sends no delivery, so the project's own setting decides"
fi

# 6. The spec reached the server intact without execution-policy text. Factory
# supplies reporting only to the stage that owns the outcome.
assert_contains "spec text survived JSON encoding" "Add a farewell function" "$sent"
if grep -q 'Reporting, required' <<< "$sent"; then
  notok "submission duplicated the reporting contract into every stage"
else
  ok "does not duplicate reporting policy into task content"
fi

# 7. The default project map is under ~/.factory, not in the skill and not in
# any checkout. Read with HOME pointed at an empty directory, so the message
# names the path without depending on the real one existing.
out7="$(env -u FACTORY_PROJECTS HOME="$WORK/home" FACTORY_BASE=http://127.0.0.1:1 \
  "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" 2>&1)"
assert_eq "a missing project map exits 2" "2" "$?"
assert_contains "the default map is under ~/.factory/dark-factory" \
  "$WORK/home/.factory/dark-factory/projects.tsv" "$out7"

# 8. --draft flips pre_approved, which is the cockpit path, not ours.
fake_api_start 18086 "201" "$RESP"
FACTORY_BASE=http://127.0.0.1:18086 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" --draft >/dev/null 2>&1
sent2="$(cat "$FAKE_LOG")"
fake_api_stop
assert_contains "--draft sends pre_approved false" '"pre_approved":false' "$sent2"

# 9. A named pipeline is resolved to its id through the API, never guessed.
# One canned body answers both requests, so it carries both shapes.
BOTH='{"pipelines":[{"id":"7ad8c32a-1f0e-4b2a-9c31-2d5f4a6b8e10","name":"Implement, review, deliver"},{"id":"8bd9c43b-2f1e-4c3b-8d42-3e6f5b7c9f21","name":"Fast"}],"run_id":"5edf217d-53dc-4099-ad56-55e1f27bdd68","task_id":"3bf8ba22-935d-4ef8-9445-9139c3413fc7","work_ids":["b22e40aa-b348-4738-a7a1-051d1c046f72"],"state":"queued","source":"orchestrator"}'
fake_api_start 18087 "200" "$BOTH"
FACTORY_BASE=http://127.0.0.1:18087 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" \
  --pipeline 'Implement, review, deliver' >/dev/null 2>&1
sent3="$(cat "$FAKE_LOG")"
fake_api_stop
assert_contains "resolves the pipeline name to an id" '"pipeline_id":"7ad8c32a-1f0e-4b2a-9c31-2d5f4a6b8e10"' "$sent3"

fake_api_start 18089 "200" "$BOTH"
FACTORY_BASE=http://127.0.0.1:18089 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" \
  --assurance fast >/dev/null 2>&1
fast_sent="$(cat "$FAKE_LOG")"
fake_api_stop
assert_contains "fast assurance is explicit" '"assurance":"fast"' "$fast_sent"
assert_contains "fast assurance selects Fast pipeline" '"pipeline_id":"8bd9c43b-2f1e-4c3b-8d42-3e6f5b7c9f21"' "$fast_sent"

# 10. An unknown pipeline name fails locally, and never submits.
fake_api_start 18088 "200" "$BOTH"
out4="$(FACTORY_BASE=http://127.0.0.1:18088 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" \
  --pipeline 'No Such Pipeline' 2>&1)"
rc4=$?
sent4="$(cat "$FAKE_LOG")"
fake_api_stop
assert_eq "unknown pipeline exits 2" "2" "$rc4"
assert_contains "unknown pipeline lists the real ones" "Implement, review, deliver" "$out4"
if grep -q 'POST' <<< "$sent4"; then
  notok "submitted anyway after failing to resolve the pipeline"
else
  ok "never submits when the pipeline cannot be resolved"
fi

# 11. --planning carries the light factory's triple into the admission
# payload. It is the only thing that lets the roadmap join a critique Work
# back to the checkpoint it critiqued, so it is sent as three typed fields
# rather than as a string a reader has to split.
fake_api_start 18090 "200" "$BOTH"
FACTORY_BASE=http://127.0.0.1:18090 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" \
  --pipeline 'Fast' --planning 'Payer:2:3' >/dev/null 2>&1
planned="$(cat "$FAKE_LOG")"
fake_api_stop
assert_contains "--planning sends the planning object" '"planning":{' "$planned"
assert_contains "--planning sends the project, folded to a slug" '"project":"payer"' "$planned"
assert_contains "--planning sends the number as a number" '"number":2' "$planned"
assert_contains "--planning sends the round as a number" '"round":3' "$planned"

# 12. Without the flag there is no planning key at all. Admission uses
# DisallowUnknownFields, but an empty object would also file every ordinary
# submission under a checkpoint it has nothing to do with.
fake_api_start 18091 "201" "$RESP"
FACTORY_BASE=http://127.0.0.1:18091 "$SUB" --project scratch --name x --spec-file "$WORK/spec.md" >/dev/null 2>&1
plain="$(cat "$FAKE_LOG")"
fake_api_stop
if grep -q '"planning"' <<< "$plain"; then
  notok "sent a planning field on a submission that has no checkpoint"
else
  ok "sends no planning field unless --planning was given"
fi

# 13. A malformed triple fails here, with the shape in the message, and never
# reaches admission.
fake_api_start 18092 "200" "$BOTH"
outp="$(FACTORY_BASE=http://127.0.0.1:18092 "$SUB" --project scratch --name x \
  --spec-file "$WORK/spec.md" --planning 'payer:two:1' 2>&1)"
rcp=$?
sentp="$(cat "$FAKE_LOG")"
fake_api_stop
assert_eq "a malformed --planning exits 2" "2" "$rcp"
assert_contains "says the shape it wanted" "PROJECT:N:ROUND" "$outp"
if grep -q 'POST' <<< "$sentp"; then
  notok "submitted anyway with a planning triple it could not parse"
else
  ok "never submits on a planning triple it cannot parse"
fi

# 15. Everything below is the no-map path: the repository comes from the
# checkout the caller is standing in, or from --repo, and the project map is no
# longer a prerequisite for submitting work.
#
# One canned body answers every request in a run, so it carries the shape of
# all four: the repository list, the pipeline list, the create-repository
# response, and the admission response.
PIPES='[{"id":"7ad8c32a-1f0e-4b2a-9c31-2d5f4a6b8e10","name":"Implement, review, deliver"},{"id":"8bd9c43b-2f1e-4c3b-8d42-3e6f5b7c9f21","name":"Fast"},{"id":"9ce0d54c-3a2f-4d4c-9e53-4f706c8daf32","name":"Mapped pipeline"}]'

repos_with() { # repos_with <remote_identity> <true|false enabled>
  jq -nc --arg r "$1" --argjson e "$2" \
    '[{id: "c0ffee00-0000-4000-8000-000000000001", remote_identity: $r, enabled: $e, default_delivery: "pr"}]'
}

body_for() { # body_for <repositories json array>
  jq -nc --argjson repos "$1" --argjson pipes "$PIPES" '{
    repositories: $repos,
    pipelines:    $pipes,
    id:       "c0ffee00-0000-4000-8000-000000000001",
    run_id:   "5edf217d-53dc-4099-ad56-55e1f27bdd68",
    task_id:  "3bf8ba22-935d-4ef8-9445-9139c3413fc7",
    work_ids: ["b22e40aa-b348-4738-a7a1-051d1c046f72"],
    state: "queued", source: "orchestrator"}'
}

mkrepo() { # mkrepo <dir> <origin url>
  rm -rf "$1"; mkdir -p "$1"
  git -C "$1" init -q >/dev/null 2>&1
  git -C "$1" remote add origin "$2"
}

# A fresh port each time. A killed HTTPServer and an immediate rebind are fine
# on one machine and not on another, and a flaky harness teaches nothing.
PORT=18100
run_in() { # run_in <cwd> <status> <body> <command...> -> RC, OUT (stdout+stderr), SENT
  local dir="$1" status="$2" body="$3"; shift 3
  PORT=$((PORT + 1))
  fake_api_start "$PORT" "$status" "$body"
  OUT="$(cd "$dir" && FACTORY_BASE="http://127.0.0.1:$PORT" "$@" 2>&1)"
  RC=$?
  SENT="$(cat "$FAKE_LOG")"
  fake_api_stop
}

ALPHA_PRESENT="$(body_for "$(repos_with github.com/octocat/alpha true)")"

# Every origin form git hands out has to collapse to the one identity the
# factory knows, because that string is what admission and registration match.
while read -r form; do
  [ -n "$form" ] || continue
  mkrepo "$WORK/derived" "$form"
  run_in "$WORK/derived" 200 "$ALPHA_PRESENT" "$SUB" --name x --spec-file "$WORK/spec.md"
  assert_contains "derives github.com/octocat/alpha from $form" \
    '"repository":"github.com/octocat/alpha"' "$SENT"
done <<'FORMS'
https://github.com/octocat/alpha.git
https://github.com/octocat/alpha
git@github.com:octocat/alpha.git
ssh://git@github.com/octocat/alpha.git
FORMS

# 16. Every way the derivation can fail says so here, before any request. An
# agent that cannot name the repository must not guess one.
mkdir -p "$WORK/plain"
out16="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$SUB" --name x --spec-file "$WORK/spec.md" 2>&1)"
assert_eq "outside a git checkout exits 2" "2" "$?"
assert_contains "outside a git checkout says so" "not inside a git checkout" "$out16"
assert_contains "outside a git checkout names the way out" "--repo github.com/owner/name" "$out16"

rm -rf "$WORK/noremote"; mkdir -p "$WORK/noremote"; git -C "$WORK/noremote" init -q >/dev/null 2>&1
out16b="$(cd "$WORK/noremote" && FACTORY_BASE=http://127.0.0.1:1 "$SUB" --name x --spec-file "$WORK/spec.md" 2>&1)"
assert_eq "a checkout with no origin exits 2" "2" "$?"
assert_contains "a checkout with no origin says so" "no 'origin' remote" "$out16b"

mkrepo "$WORK/gitlab" "https://gitlab.com/octocat/alpha.git"
out16c="$(cd "$WORK/gitlab" && FACTORY_BASE=http://127.0.0.1:1 "$SUB" --name x --spec-file "$WORK/spec.md" 2>&1)"
assert_eq "a non-github origin exits 2" "2" "$?"
assert_contains "a non-github origin names the origin it refused" "gitlab.com/octocat/alpha" "$out16c"

# 17. --repo names the repository outright, from anywhere.
run_in "$WORK/plain" 200 "$(body_for "$(repos_with github.com/octocat/beta true)")" \
  "$SUB" --name x --spec-file "$WORK/spec.md" --repo github.com/octocat/beta
assert_eq "--repo works outside any checkout" "0" "$RC"
assert_contains "--repo is the repository that is submitted" '"repository":"github.com/octocat/beta"' "$SENT"

out17="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$SUB" --name x --spec-file "$WORK/spec.md" \
  --repo https://gitlab.com/octocat/beta 2>&1)"
assert_eq "a --repo that is not a github identity exits 2" "2" "$?"

out17b="$(cd "$WORK/plain" && FACTORY_BASE=http://127.0.0.1:1 "$SUB" --name x --spec-file "$WORK/spec.md" \
  --project scratch --repo github.com/octocat/beta 2>&1)"
assert_eq "--project and --repo together exits 2" "2" "$?"
assert_contains "--project and --repo together says which to pick" "not both" "$out17b"

# 18. Pipeline resolution when there is no map line to read it from:
# --pipeline, then fast, then a matching map line, then FACTORY_DEFAULT_PIPELINE,
# then the three stage pipeline reviewed work needs.
IMPL='"pipeline_id":"7ad8c32a-1f0e-4b2a-9c31-2d5f4a6b8e10"'
FAST='"pipeline_id":"8bd9c43b-2f1e-4c3b-8d42-3e6f5b7c9f21"'
MAPPED='"pipeline_id":"9ce0d54c-3a2f-4d4c-9e53-4f706c8daf32"'

mkrepo "$WORK/derived" "git@github.com:octocat/alpha.git"

run_in "$WORK/derived" 200 "$ALPHA_PRESENT" "$SUB" --name x --spec-file "$WORK/spec.md"
assert_contains "with nothing to go on, reviewed work gets the three stage pipeline" "$IMPL" "$SENT"

run_in "$WORK/derived" 200 "$ALPHA_PRESENT" \
  env FACTORY_DEFAULT_PIPELINE=Fast "$SUB" --name x --spec-file "$WORK/spec.md"
assert_contains "FACTORY_DEFAULT_PIPELINE beats the built-in default" "$FAST" "$SENT"

run_in "$WORK/derived" 200 "$ALPHA_PRESENT" \
  env FACTORY_DEFAULT_PIPELINE=Fast "$SUB" --name x --spec-file "$WORK/spec.md" \
  --pipeline 'Implement, review, deliver'
assert_contains "--pipeline beats FACTORY_DEFAULT_PIPELINE" "$IMPL" "$SENT"

run_in "$WORK/derived" 200 "$ALPHA_PRESENT" \
  env FACTORY_DEFAULT_PIPELINE='Implement, review, deliver' "$SUB" --name x --spec-file "$WORK/spec.md" \
  --assurance fast
assert_contains "fast assurance beats FACTORY_DEFAULT_PIPELINE" "$FAST" "$SENT"

# A map line still wins over the environment, and it matches on the repository
# column, case-insensitively, because a remote's case is not the map's business.
printf 'mapped\tgithub.com/octocat/MappedRepo\tMapped pipeline\n' >> "$FACTORY_PROJECTS"
mkrepo "$WORK/mapped" "git@github.com:octocat/mappedrepo.git"
MAPPED_PRESENT="$(body_for "$(repos_with github.com/octocat/mappedrepo true)")"

run_in "$WORK/mapped" 200 "$MAPPED_PRESENT" \
  env FACTORY_DEFAULT_PIPELINE=Fast "$SUB" --name x --spec-file "$WORK/spec.md"
assert_contains "a map line matching the repository, case-insensitively, beats the environment" \
  "$MAPPED" "$SENT"

run_in "$WORK/mapped" 200 "$MAPPED_PRESENT" "$SUB" --name x --spec-file "$WORK/spec.md" --assurance fast
assert_contains "fast assurance beats a matching map line" "$FAST" "$SENT"

# The map is a convenience now, not a prerequisite. HOME points at an empty
# directory so no map exists at the default path either.
run_in "$WORK/derived" 200 "$ALPHA_PRESENT" \
  env -u FACTORY_PROJECTS HOME="$WORK/home" "$SUB" --name x --spec-file "$WORK/spec.md"
assert_eq "a missing map is not an error once the repository is derived" "0" "$RC"
assert_contains "and it still resolves a pipeline" "$IMPL" "$SENT"

# 19. Registration. Admission is 404 repository_not_found for a repository the
# factory does not manage, and registering carries no policy, so submit does it
# rather than making a human run factory-register first.
run_in "$WORK/derived" 200 "$(body_for '[]')" "$SUB" --name x --spec-file "$WORK/spec.md"
assert_eq "registering on the way through still exits 0" "0" "$RC"
assert_eq "registers the missing repository exactly once" "1" \
  "$(grep -c '^POST /api/v1/repositories' <<< "$SENT")"
assert_contains "registers it under the identity it will submit" \
  '{"remote_identity":"github.com/octocat/alpha"}' "$SENT"
assert_contains "says it registered" "registered github.com/octocat/alpha" "$OUT"
assert_contains "and then submits" 'POST /api/v1/work' "$SENT"
if grep -q '"default_delivery"' <<< "$SENT"; then
  notok "registration set a delivery mode: that is a cockpit decision, not a submission's"
else
  ok "registration sends remote_identity and nothing else"
fi

run_in "$WORK/derived" 200 "$ALPHA_PRESENT" "$SUB" --name x --spec-file "$WORK/spec.md"
assert_eq "an already registered repository is not registered again" "0" \
  "$(grep -c '^POST /api/v1/repositories' <<< "$SENT")"

# The list is matched case-insensitively, so a repository registered under one
# spelling is not registered a second time under another.
run_in "$WORK/derived" 200 "$(body_for "$(repos_with github.com/Octocat/Alpha true)")" \
  "$SUB" --name x --spec-file "$WORK/spec.md"
assert_eq "a differently cased registration still counts as registered" "0" \
  "$(grep -c '^POST /api/v1/repositories' <<< "$SENT")"

# Disabled is a deliberate stop. Submitting anyway would queue work that can
# never route, and re-enabling it here would undo somebody's decision.
run_in "$WORK/derived" 200 "$(body_for "$(repos_with github.com/octocat/alpha false)")" \
  "$SUB" --name x --spec-file "$WORK/spec.md"
assert_eq "a disabled repository exits 2" "2" "$RC"
assert_contains "a disabled repository says it is disabled" "disabled" "$OUT"
if grep -q '^POST ' <<< "$SENT"; then
  notok "posted something for a disabled repository: neither work nor a re-registration belongs there"
else
  ok "a disabled repository submits nothing and registers nothing"
fi

# 20. --project still means the map, and never the checkout it happens to be
# standing in.
run_in "$WORK/derived" 200 "$(body_for "$(repos_with github.com/octocat/factory-scratch true)")" \
  "$SUB" --project scratch --name x --spec-file "$WORK/spec.md"
assert_contains "--project reads the map, not the checkout it is standing in" \
  '"repository":"github.com/octocat/factory-scratch"' "$SENT"

# 14. Nothing was written inside the skill directory. No state file, no project
# map, no scratch: an install replaces that directory and would eat it.
after="$(find "$SKILL_DIR" -type f | sort | xargs shasum | shasum)"
assert_eq "writes nothing inside the installed skill directory" "$before" "$after"

exit "$TESTS_FAILED"
