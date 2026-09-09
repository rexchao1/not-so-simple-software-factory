#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly prompt_path="${1:?usage: execute.sh PROMPT_FILE}"
readonly region="${REGION:-europe-west1}"
readonly job_name="${JOB_NAME:-factory-agent-experiment}"
readonly artifact_bucket="${ARTIFACT_BUCKET:-${project_id}-factory-agent-artifacts}"
readonly repository_url="${REPOSITORY_URL:-https://github.com/owainlewis/factory.git}"
readonly git_ref="${GIT_REF:-main}"
readonly agent_mode="${AGENT_MODE:-read-only}"
readonly model="${MODEL:-deepseek/deepseek-v4-flash}"
readonly thinking="${THINKING:-low}"
readonly wait_seconds="${WAIT_SECONDS:-600}"
readonly delete_on_terminal="${DELETE_EXECUTION_ON_TERMINAL:-true}"
readonly output_root="${OUTPUT_ROOT:-./factory-agent-results}"
readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

if [[ ! -f "$prompt_path" ]]; then
    printf 'prompt file does not exist: %s\n' "$prompt_path" >&2
    exit 2
fi
if [[ ! "$repository_url" =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$ ]]; then
    printf 'REPOSITORY_URL must be a public GitHub HTTPS clone URL\n' >&2
    exit 2
fi
if [[ ! "$wait_seconds" =~ ^[1-9][0-9]*$ ]]; then
    printf 'WAIT_SECONDS must be a positive integer\n' >&2
    exit 2
fi
case "$agent_mode" in
    read-only | write) ;;
    *)
        printf 'AGENT_MODE must be read-only or write\n' >&2
        exit 2
        ;;
esac
case "$delete_on_terminal" in
    true | false) ;;
    *)
        printf 'DELETE_EXECUTION_ON_TERMINAL must be true or false\n' >&2
        exit 2
        ;;
esac

if [[ -n "${GIT_COMMIT:-}" ]]; then
    git_commit="$GIT_COMMIT"
else
    git_commit="$("${script_dir}/resolve-git-ref.sh" "$repository_url" "$git_ref")"
fi
readonly git_commit
if [[ ! "$git_commit" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'Could not resolve GIT_REF to a full commit: %s\n' "$git_ref" >&2
    exit 2
fi

readonly attempt_id="${ATTEMPT_ID:-attempt-$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 6)}"
if [[ ! "$attempt_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
    printf 'ATTEMPT_ID contains unsupported characters\n' >&2
    exit 2
fi
readonly dispatch_nonce="${DISPATCH_NONCE:-$(openssl rand -hex 16)}"
if [[ ! "$dispatch_nonce" =~ ^[0-9a-f]{32}$ ]]; then
    printf 'DISPATCH_NONCE must be 32 lowercase hexadecimal characters\n' >&2
    exit 2
fi
readonly input_uri="gs://${artifact_bucket}/attempts/${attempt_id}/input.json"
readonly output_uri="gs://${artifact_bucket}/attempts/${attempt_id}/attempt-result.tar.gz"
readonly input_path="${temp_root}/input.json"
readonly existing_input_path="${temp_root}/existing-input.json"
readonly attempt_output="${output_root}/${attempt_id}"
readonly launch_path="${attempt_output}/launch.json"
mkdir -p "$output_root"
if ! mkdir "$attempt_output" 2>/dev/null; then
    printf 'Attempt already exists locally: %s\n' "$attempt_id" >&2
    printf 'Resume it with: PROJECT_ID=%s OUTPUT_ROOT=%q %q %q\n' \
        "$project_id" "$output_root" "${script_dir}/inspect.sh" "$attempt_id" >&2
    printf 'Use a new ATTEMPT_ID for a genuine retry.\n' >&2
    exit 2
fi

jq -nc \
    --arg attempt_id "$attempt_id" \
    --arg dispatch_nonce "$dispatch_nonce" \
    --arg repository_url "$repository_url" \
    --arg git_commit "$git_commit" \
    --rawfile prompt "$prompt_path" \
    --arg agent_mode "$agent_mode" \
    --arg model "$model" \
    --arg thinking "$thinking" \
    '{version: 2, attempt_id: $attempt_id, dispatch_nonce: $dispatch_nonce, repository_url: $repository_url, git_commit: $git_commit, prompt: $prompt, agent_mode: $agent_mode, model: $model, thinking: $thinking}' \
    > "$input_path"

jq -n \
    --arg project "$project_id" \
    --arg region "$region" \
    --arg job "$job_name" \
    --arg attempt "$attempt_id" \
    --arg dispatch_nonce "$dispatch_nonce" \
    --arg commit "$git_commit" \
    --arg input_uri "$input_uri" \
    --arg output_uri "$output_uri" \
    '{version: 2, project: $project, region: $region, job: $job, attempt: $attempt, dispatch_nonce: $dispatch_nonce, execution_name: null, execution: null, commit: $commit, input_uri: $input_uri, output_uri: $output_uri, dispatch_state: "dispatching"}' \
    > "$launch_path"

if ! gcloud storage cp "$input_path" "$input_uri" \
    --if-generation-match 0 \
    --project "$project_id" --quiet; then
    if ! gcloud storage cp "$input_uri" "$existing_input_path" \
        --project "$project_id" --quiet \
        || ! cmp -s "$input_path" "$existing_input_path"; then
        printf 'input upload failed and no byte-identical object could be recovered: %s\n' "$input_uri" >&2
        exit 1
    fi
    printf 'Recovered byte-identical input after an ambiguous upload response.\n' >&2
fi

readonly request_body="$(
    jq -nc \
        --arg attempt_id "$attempt_id" \
        --arg input_uri "$input_uri" \
        --arg output_uri "$output_uri" \
        '{overrides: {containerOverrides: [{env: [{name: "ATTEMPT_ID", value: $attempt_id}, {name: "INPUT_URI", value: $input_uri}, {name: "OUTPUT_URI", value: $output_uri}]}], taskCount: 1, timeout: "600s"}}'
)"
readonly access_token="$(gcloud auth print-access-token)"
readonly run_url="https://run.googleapis.com/v2/projects/${project_id}/locations/${region}/jobs/${job_name}:run"
run_response=''
run_response_received=false
if run_response="$(
    curl --fail-with-body --silent --show-error \
        --request POST \
        --header "Authorization: Bearer ${access_token}" \
        --header 'Content-Type: application/json' \
        --data "$request_body" \
        "$run_url"
)"; then
    run_response_received=true
    execution_name="$(jq -er '.metadata.name' <<< "$run_response")"
    execution_id="${execution_name##*/}"
    launch_update="$(mktemp "${attempt_output}/launch.XXXXXX")"
    jq --arg execution_name "$execution_name" --arg execution "$execution_id" \
        '.execution_name = $execution_name | .execution = $execution | .dispatch_state = "accepted"' \
        "$launch_path" > "$launch_update"
    mv "$launch_update" "$launch_path"
fi

printf 'Attempt: %s\n' "$attempt_id"
printf 'Commit: %s\n' "$git_commit"
if [[ "$run_response_received" == true ]]; then
    printf 'Execution started: %s\n' "$execution_id"
else
    printf 'RunJob response was lost; reconciling by Attempt ID.\n' >&2
fi
printf 'Input: %s\n' "$input_uri"
printf 'Launch record: %s\n' "$launch_path"
printf 'Resume: PROJECT_ID=%s OUTPUT_ROOT=%q %q %q\n' \
    "$project_id" "$output_root" "${script_dir}/inspect.sh" "$attempt_id"

PROJECT_ID="$project_id" \
WAIT_SECONDS="$wait_seconds" \
DELETE_EXECUTION_ON_TERMINAL="$delete_on_terminal" \
OUTPUT_ROOT="$output_root" \
    "${script_dir}/inspect.sh" "$attempt_id"
