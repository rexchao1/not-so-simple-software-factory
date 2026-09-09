#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

readonly source_repo="${temp_root}/source"
readonly fake_bin="${temp_root}/bin"
mkdir -p "$source_repo" "$fake_bin"

git -C "$source_repo" init --quiet --initial-branch main
printf 'fixture\n' > "${source_repo}/README.md"
git -C "$source_repo" add README.md
git -C "$source_repo" \
    -c user.name=Factory \
    -c user.email=factory@example.invalid \
    commit --quiet -m fixture
readonly source_commit="$(git -C "$source_repo" rev-parse HEAD)"

cat > "${fake_bin}/pi" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$PWD" > "${FAKE_PI_CAPTURE}/cwd"
printf '%s\n' "$@" > "${FAKE_PI_CAPTURE}/arguments"
cat > "${FAKE_PI_CAPTURE}/prompt"
printf 'CLOUD_RUN_AGENT_OK\n' > cloud-run-smoke.txt
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"PRIVATE_REASONING"}}'
printf '%s\n' '{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"PRIVATE_PROMPT"}]}}'
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"PRIVATE_THOUGHT"},{"type":"text","text":"finished"}],"usage":{"cost":{"total":0.0123}}}}'
if [[ "${FAKE_PI_MALFORMED:-}" == 1 ]]; then
    printf '%s\n' '{malformed'
fi
exit "${FAKE_PI_EXIT_CODE:-0}"
EOF
chmod 0755 "${fake_bin}/pi"

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
output=""
source_file=""
url=""
write_out=false
while (( $# > 0 )); do
    case "$1" in
        --output)
            output="$2"
            shift 2
            ;;
        --data-binary)
            source_file="${2#@}"
            shift 2
            ;;
        --write-out)
            write_out=true
            shift 2
            ;;
        http://* | https://*)
            url="$1"
            shift
            ;;
        *)
            shift
            ;;
    esac
done
case "$url" in
    http://metadata.google.internal/*)
        printf '%s\n' '{"access_token":"test-token"}'
        ;;
    *'?alt=media')
        if [[ "$url" == *attempt-result.tar.gz* ]]; then
            cp "$FAKE_OUTPUT_ARCHIVE" "$output"
        else
            cp "$FAKE_INPUT_JSON" "$output"
        fi
        ;;
    https://storage.googleapis.com/upload/*)
        if [[ "${FAKE_UPLOAD_LOST_RESPONSE:-}" == 1 ]]; then
            counter=0
            [[ ! -f "$FAKE_UPLOAD_COUNTER" ]] || counter="$(cat "$FAKE_UPLOAD_COUNTER")"
            counter=$((counter + 1))
            printf '%s' "$counter" > "$FAKE_UPLOAD_COUNTER"
            if (( counter == 1 )); then
                cp "$source_file" "$FAKE_OUTPUT_ARCHIVE"
                printf '000'
                exit 28
            fi
            printf '%s' '{"error":{"code":412}}' > "$output"
            printf '412'
        else
            cp "$source_file" "$FAKE_OUTPUT_ARCHIVE"
            printf '%s' '{}' > "$output"
            [[ "$write_out" == false ]] || printf '200'
        fi
        ;;
    *)
        printf 'unexpected curl URL: %s\n' "$url" >&2
        exit 1
        ;;
esac
EOF
chmod 0755 "${fake_bin}/curl"

write_input() {
    local path="$1"
    local attempt="$2"
    local mode="$3"
    jq -nc \
        --arg attempt_id "$attempt" \
        --arg repository_url "$source_repo" \
        --arg git_commit "$source_commit" \
        --arg prompt "${TEST_PROMPT:---help}" \
        --arg agent_mode "$mode" \
        '{version: 2, attempt_id: $attempt_id, dispatch_nonce: "0123456789abcdef0123456789abcdef", repository_url: $repository_url, git_commit: $git_commit, prompt: $prompt, agent_mode: $agent_mode, model: "deepseek/deepseek-v4-flash", thinking: "low"}' \
        > "$path"
}

run_agent() {
    local workspace="$1"
    local attempt="$2"
    local input="$3"
    local output="$4"
    local exit_code="${5:-0}"
    local malformed="${6:-0}"
    local lost_upload_response="${7:-0}"
    mkdir -p "$workspace" "${workspace}/capture"
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    ATTEMPT_ID="$attempt" \
    INPUT_URI="gs://test-bucket/attempts/${attempt}/input.json" \
    OUTPUT_URI="gs://test-bucket/attempts/${attempt}/attempt-result.tar.gz" \
    OPENROUTER_API_KEY=test-key \
    FACTORY_AGENT_TEST=1 \
    FAKE_INPUT_JSON="$input" \
    FAKE_OUTPUT_ARCHIVE="$output" \
    FAKE_PI_CAPTURE="${workspace}/capture" \
    FAKE_PI_EXIT_CODE="$exit_code" \
    FAKE_PI_MALFORMED="$malformed" \
    FAKE_UPLOAD_LOST_RESPONSE="$lost_upload_response" \
    FAKE_UPLOAD_COUNTER="${workspace}/upload-counter" \
        "$script_dir/run-agent.sh" > "${workspace}/output"
}

run_lost_upload_response_test() {
    local workspace="${temp_root}/lost-upload-response"
    local input="${temp_root}/lost-upload-response-input.json"
    local output="${temp_root}/lost-upload-response-result.tar.gz"
    write_input "$input" attempt-lost-upload-response read-only

    run_agent "$workspace" attempt-lost-upload-response "$input" "$output" 0 0 1

    [[ "$(cat "${workspace}/upload-counter")" -eq 2 ]]
    [[ -s "$output" ]]
    mkdir -p "${workspace}/verified"
    tar -xzf "$output" -C "${workspace}/verified"
    [[ "$(jq -r '.exit_code' "${workspace}/verified/result.json")" == 0 ]]
}

run_malformed_event_test() {
    local workspace="${temp_root}/malformed"
    local input="${temp_root}/malformed-input.json"
    local output="${temp_root}/malformed-result.tar.gz"
    write_input "$input" attempt-malformed read-only

    set +e
    run_agent "$workspace" attempt-malformed "$input" "$output" 0 1 2>/dev/null
    local exit_code="$?"
    set -e

    [[ "$exit_code" -ne 0 ]]
}

run_success_test() {
    local workspace="${temp_root}/success"
    local input="${temp_root}/success-input.json"
    local output="${temp_root}/success-result.tar.gz"
    write_input "$input" attempt-success write
    run_agent "$workspace" attempt-success "$input" "$output"

    [[ "$(cat "${workspace}/capture/cwd")" == "${workspace}/repo" ]]
    [[ "$(git -C "${workspace}/repo" rev-parse HEAD)" == "$source_commit" ]]
    grep -Fx -- '--provider' "${workspace}/capture/arguments" >/dev/null
    grep -Fx -- 'openrouter' "${workspace}/capture/arguments" >/dev/null
    grep -Fx -- 'deepseek/deepseek-v4-flash' "${workspace}/capture/arguments" >/dev/null
    ! grep -Fx -- '--help' "${workspace}/capture/arguments" >/dev/null
    [[ "$(cat "${workspace}/capture/prompt")" == '--help' ]]
    grep -F -- '"cost_usd":0.0123' "${workspace}/output" >/dev/null
    grep -F -- '"exit_code":0' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_REASONING' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_PROMPT' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_THOUGHT' "${workspace}/output" >/dev/null

    mkdir -p "${workspace}/verified"
    tar -xzf "$output" -C "${workspace}/verified"
    grep -F -- 'CLOUD_RUN_AGENT_OK' "${workspace}/verified/changes.patch" >/dev/null
    grep -F -- '"text":"finished"' "${workspace}/verified/events.jsonl" >/dev/null
    ! grep -F -- 'PRIVATE_REASONING' "${workspace}/verified/events.jsonl" >/dev/null
    [[ "$(jq -r '.attempt_id' "${workspace}/verified/manifest.json")" == attempt-success ]]
    [[ "$(jq -r '.commit' "${workspace}/verified/manifest.json")" == "$source_commit" ]]
    [[ "$(jq -r '.dispatch_nonce' "${workspace}/verified/manifest.json")" == 0123456789abcdef0123456789abcdef ]]
}

run_agent_failure_test() {
    local workspace="${temp_root}/failure"
    local input="${temp_root}/failure-input.json"
    local output="${temp_root}/failure-result.tar.gz"
    write_input "$input" attempt-failure read-only

    set +e
    run_agent "$workspace" attempt-failure "$input" "$output" 7
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 7 ]]
    if [[ ! -s "$output" ]]; then
        printf 'failed agent did not publish a result archive\n' >&2
        return 1
    fi
    mkdir -p "${workspace}/verified"
    tar -xzf "$output" -C "${workspace}/verified"
    [[ "$(jq -r '.exit_code' "${workspace}/verified/result.json")" == 7 ]]
}

run_invalid_input_test() {
    local workspace="${temp_root}/invalid"
    local input="${temp_root}/invalid-input.json"
    local output="${temp_root}/invalid-result.tar.gz"
    write_input "$input" another-attempt unsafe
    mkdir -p "$workspace" "${workspace}/capture"

    set +e
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    ATTEMPT_ID=attempt-invalid \
    INPUT_URI=gs://test-bucket/input.json \
    OUTPUT_URI=gs://test-bucket/output.tar.gz \
    OPENROUTER_API_KEY=test-key \
    FACTORY_AGENT_TEST=1 \
    FAKE_INPUT_JSON="$input" \
    FAKE_OUTPUT_ARCHIVE="$output" \
    FAKE_PI_CAPTURE="${workspace}/capture" \
        "$script_dir/run-agent.sh" > "${workspace}/output" 2>&1
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 2 ]]
    grep -F -- 'input identity does not match this execution' "${workspace}/output" >/dev/null
    [[ ! -e "$output" ]]
}

run_cloud_run_test_bypass_rejection_test() {
    local workspace="${temp_root}/cloud-run-bypass"
    local input="${temp_root}/cloud-run-bypass-input.json"
    local output="${temp_root}/cloud-run-bypass-result.tar.gz"
    write_input "$input" attempt-cloud-run-bypass read-only
    mkdir -p "$workspace" "${workspace}/capture"

    set +e
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    ATTEMPT_ID=attempt-cloud-run-bypass \
    INPUT_URI=gs://test-bucket/input.json \
    OUTPUT_URI=gs://test-bucket/output.tar.gz \
    OPENROUTER_API_KEY=test-key \
    FACTORY_AGENT_TEST=1 \
    CLOUD_RUN_EXECUTION=factory-agent-experiment-test \
    FAKE_INPUT_JSON="$input" \
    FAKE_OUTPUT_ARCHIVE="$output" \
    FAKE_PI_CAPTURE="${workspace}/capture" \
        "$script_dir/run-agent.sh" > "${workspace}/output" 2>&1
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 2 ]]
    grep -F -- 'repository_url must be a public GitHub HTTPS clone URL' "${workspace}/output" >/dev/null
    [[ ! -e "$output" ]]
}

run_success_test
run_agent_failure_test
run_lost_upload_response_test
run_malformed_event_test
run_invalid_input_test
run_cloud_run_test_bypass_rejection_test

printf 'cloud-run agent tests passed\n'
