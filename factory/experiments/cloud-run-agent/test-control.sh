#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

readonly source_repo="${temp_root}/source"
mkdir -p "$source_repo"
git -C "$source_repo" init --quiet --initial-branch main
printf 'fixture\n' > "${source_repo}/README.md"
git -C "$source_repo" add README.md
git -C "$source_repo" -c user.name=Factory -c user.email=factory@example.invalid \
    commit --quiet -m fixture
readonly source_commit="$(git -C "$source_repo" rev-parse HEAD)"
git -C "$source_repo" -c user.name=Factory -c user.email=factory@example.invalid \
    tag -a annotated -m annotated
readonly tag_object="$(git -C "$source_repo" rev-parse annotated)"

resolved_commit="$("${script_dir}/resolve-git-ref.sh" "$source_repo" annotated)"
[[ "$resolved_commit" == "$source_commit" ]]
[[ "$resolved_commit" != "$tag_object" ]]

readonly fake_bin="${temp_root}/bin"
readonly output_root="${temp_root}/results"
readonly fake_archive="${temp_root}/attempt-result.tar.gz"
mkdir -p "$fake_bin"

cat > "${fake_bin}/gcloud" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "$1 $2 $3" == "auth print-access-token " ]]; then
    printf 'test-token\n'
    exit 0
fi
if [[ "$1 $2" == "storage cp" ]]; then
    if [[ "$3" == gs://* ]]; then
        if [[ "$3" == *'/input.json' && -n "${FAKE_STORED_INPUT:-}" ]]; then
            cp "$FAKE_STORED_INPUT" "$4"
            exit 0
        fi
        [[ "${FAKE_ARTIFACT_MISSING:-}" != 1 ]] || exit 1
        cp "${FAKE_RESULT_ARCHIVE:?}" "$4"
    elif [[ "${FAKE_INPUT_LOST_RESPONSE:-}" == 1 ]]; then
        [[ -s "${EXPECTED_LAUNCH_PATH:?}" ]]
        [[ "$(jq -r '.dispatch_state' "$EXPECTED_LAUNCH_PATH")" == dispatching ]]
        [[ -e "${FAKE_STORED_INPUT:?}" ]] || cp "$3" "$FAKE_STORED_INPUT"
        exit 1
    fi
    exit 0
fi
printf 'unexpected gcloud call: %s\n' "$*" >&2
exit 1
EOF
chmod 0755 "${fake_bin}/gcloud"

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
for argument in "$@"; do
    if [[ "$argument" == *':run' ]]; then
        printf '%s\n' '{"metadata":{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test"}}'
        exit 0
    fi
done
printf 'simulated polling interruption\n' >&2
exit 22
EOF
chmod 0755 "${fake_bin}/curl"

printf 'inspect this repository\n' > "${temp_root}/prompt.txt"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
GIT_COMMIT="$source_commit" \
ATTEMPT_ID=attempt-launch-record \
OUTPUT_ROOT="$output_root" \
    "${script_dir}/execute.sh" "${temp_root}/prompt.txt" > "${temp_root}/execute-output" 2>&1
execute_exit="$?"
set -e

[[ "$execute_exit" -ne 0 ]]
readonly launch_path="${output_root}/attempt-launch-record/launch.json"
[[ -s "$launch_path" ]]
[[ "$(jq -r '.attempt' "$launch_path")" == attempt-launch-record ]]
[[ "$(jq -r '.execution' "$launch_path")" == execution-test ]]
[[ "$(jq -r '.commit' "$launch_path")" == "$source_commit" ]]
grep -F -- 'Resume:' "${temp_root}/execute-output" >/dev/null

set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
GIT_COMMIT="$source_commit" \
ATTEMPT_ID=attempt-launch-record \
OUTPUT_ROOT="$output_root" \
    "${script_dir}/execute.sh" "${temp_root}/prompt.txt" > "${temp_root}/duplicate-attempt-output" 2>&1
duplicate_attempt_exit="$?"
set -e
[[ "$duplicate_attempt_exit" -eq 2 ]]
grep -F -- 'Attempt already exists locally: attempt-launch-record' "${temp_root}/duplicate-attempt-output" >/dev/null
grep -F -- 'Use a new ATTEMPT_ID for a genuine retry.' "${temp_root}/duplicate-attempt-output" >/dev/null

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
url="${!#}"
case "$url" in
    *:run)
        [[ -s "${EXPECTED_LAUNCH_PATH:?}" ]]
        [[ "$(jq -r '.dispatch_state' "$EXPECTED_LAUNCH_PATH")" == dispatching ]]
        exit 28
        ;;
    *'/executions?pageSize=100')
        jq -nc '{executions:[{name:"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-reconciled",template:{containers:[{env:[{name:"ATTEMPT_ID",value:"attempt-dispatch-recovery"},{name:"INPUT_URI",value:"gs://factory-505220-factory-agent-artifacts/attempts/attempt-dispatch-recovery/input.json"},{name:"OUTPUT_URI",value:"gs://factory-505220-factory-agent-artifacts/attempts/attempt-dispatch-recovery/attempt-result.tar.gz"}]}]}}]}'
        ;;
    *'/executions/execution-reconciled')
        jq -nc '{name:"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-reconciled",conditions:[{type:"Completed",state:"CONDITION_FAILED"}]}'
        ;;
    *)
        printf 'unexpected curl URL: %s\n' "$url" >&2
        exit 1
        ;;
esac
EOF
chmod 0755 "${fake_bin}/curl"

readonly recovered_launch_path="${output_root}/attempt-dispatch-recovery/launch.json"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
GIT_COMMIT="$source_commit" \
ATTEMPT_ID=attempt-dispatch-recovery \
DISPATCH_NONCE=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
OUTPUT_ROOT="$output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=false \
FAKE_ARTIFACT_MISSING=1 \
EXPECTED_LAUNCH_PATH="$recovered_launch_path" \
FAKE_INPUT_LOST_RESPONSE=1 \
FAKE_STORED_INPUT="${temp_root}/stored-input.json" \
    "${script_dir}/execute.sh" "${temp_root}/prompt.txt" > "${temp_root}/dispatch-recovery-output" 2>&1
dispatch_recovery_exit="$?"
set -e
[[ "$dispatch_recovery_exit" -eq 1 ]]
[[ "$(jq -r '.dispatch_state' "$recovered_launch_path")" == reconciled ]]
[[ "$(jq -r '.execution' "$recovered_launch_path")" == execution-reconciled ]]
grep -F -- 'RunJob response was lost; reconciling by Attempt ID.' "${temp_root}/dispatch-recovery-output" >/dev/null
grep -F -- 'Recovered byte-identical input after an ambiguous upload response.' "${temp_root}/dispatch-recovery-output" >/dev/null
grep -F -- 'Reconciled execution: execution-reconciled' "${temp_root}/dispatch-recovery-output" >/dev/null

readonly second_output_root="${temp_root}/second-results"
readonly second_launch_path="${second_output_root}/attempt-dispatch-recovery/launch.json"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
GIT_COMMIT="$source_commit" \
ATTEMPT_ID=attempt-dispatch-recovery \
DISPATCH_NONCE=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
OUTPUT_ROOT="$second_output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=false \
FAKE_INPUT_LOST_RESPONSE=1 \
FAKE_STORED_INPUT="${temp_root}/stored-input.json" \
EXPECTED_LAUNCH_PATH="$second_launch_path" \
    "${script_dir}/execute.sh" "${temp_root}/prompt.txt" > "${temp_root}/cross-root-duplicate-output" 2>&1
cross_root_duplicate_exit="$?"
set -e
[[ "$cross_root_duplicate_exit" -eq 1 ]]
grep -F -- 'input upload failed and no byte-identical object could be recovered' "${temp_root}/cross-root-duplicate-output" >/dev/null

readonly result_fixture="${temp_root}/result-fixture"
mkdir -p "$result_fixture"
printf '%s\n' '{"attempt_id":"attempt-launch-record"}' > "${result_fixture}/result.json"
: > "${result_fixture}/changes.patch"
: > "${result_fixture}/status.txt"
: > "${result_fixture}/events.jsonl"
digest_file() { shasum -a 256 "$1" | awk '{print $1}'; }
jq -nc \
    --arg attempt_id attempt-launch-record \
    --arg dispatch_nonce "$(jq -r '.dispatch_nonce' "$launch_path")" \
    --arg commit "$source_commit" \
    --arg result "$(digest_file "${result_fixture}/result.json")" \
    --arg patch "$(digest_file "${result_fixture}/changes.patch")" \
    --arg status "$(digest_file "${result_fixture}/status.txt")" \
    --arg events "$(digest_file "${result_fixture}/events.jsonl")" \
    '{version:2,attempt_id:$attempt_id,dispatch_nonce:$dispatch_nonce,commit:$commit,files:{"result.json":$result,"changes.patch":$patch,"status.txt":$status,"events.jsonl":$events}}' \
    > "${result_fixture}/manifest.json"
tar -czf "$fake_archive" -C "$result_fixture" \
    manifest.json result.json changes.patch status.txt events.jsonl

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
counter_path="${FAKE_CURL_COUNTER:?}"
counter=0
[[ ! -f "$counter_path" ]] || counter="$(cat "$counter_path")"
counter=$((counter + 1))
printf '%s' "$counter" > "$counter_path"
if (( counter == 1 )); then
    printf '%s\n' '{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test","conditions":[{"type":"Completed","state":"CONDITION_RECONCILING"}]}'
else
    printf '%s\n' '{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test","conditions":[{"type":"Completed","state":"CONDITION_SUCCEEDED"}]}'
fi
EOF
chmod 0755 "${fake_bin}/curl"

set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
OUTPUT_ROOT="$output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=false \
FAKE_CURL_COUNTER="${temp_root}/curl-counter" \
FAKE_RESULT_ARCHIVE="$fake_archive" \
    "${script_dir}/inspect.sh" attempt-launch-record > "${temp_root}/inspect-output" 2>&1
inspect_exit="$?"
set -e
[[ "$inspect_exit" -eq 0 ]]
[[ "$(cat "${temp_root}/curl-counter")" -eq 2 ]]
grep -F -- 'Verified result:' "${temp_root}/inspect-output" >/dev/null
[[ "$(jq -r '.state' "${output_root}/attempt-launch-record/execution.json")" == CONDITION_SUCCEEDED ]]

rm -f "${output_root}/attempt-launch-record/attempt-result.tar.gz" \
    "${output_root}/attempt-launch-record/manifest.json" \
    "${output_root}/attempt-launch-record/result.json" \
    "${output_root}/attempt-launch-record/changes.patch" \
    "${output_root}/attempt-launch-record/status.txt" \
    "${output_root}/attempt-launch-record/events.jsonl"
: > "${temp_root}/curl-counter"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
OUTPUT_ROOT="$output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=true \
FAKE_CURL_COUNTER="${temp_root}/curl-counter" \
FAKE_RESULT_ARCHIVE="$fake_archive" \
FAKE_ARTIFACT_MISSING=1 \
    "${script_dir}/inspect.sh" attempt-launch-record > "${temp_root}/missing-artifact-output" 2>&1
missing_artifact_exit="$?"
set -e
[[ "$missing_artifact_exit" -eq 1 ]]
grep -F -- 'Execution retained:' "${temp_root}/missing-artifact-output" >/dev/null
grep -F -- 'execution succeeded without a result artifact' "${temp_root}/missing-artifact-output" >/dev/null

printf 'cloud-run control tests passed\n'
