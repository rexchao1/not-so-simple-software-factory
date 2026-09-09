#!/usr/bin/env bash

set -Eeuo pipefail

readonly workspace_root="${WORKSPACE_ROOT:-/workspace}"
readonly checkout_dir="${workspace_root}/repo"
readonly result_dir="${workspace_root}/result"
readonly input_path="${workspace_root}/input.json"
readonly archive_path="${workspace_root}/attempt-result.tar.gz"
readonly raw_events_path="${workspace_root}/raw-events.jsonl"
readonly events_path="${result_dir}/events.jsonl"
readonly patch_path="${result_dir}/changes.patch"
readonly status_path="${result_dir}/status.txt"

emit_error() {
    local exit_code="$?"
    jq -nc \
        --arg type factory_agent_error \
        --arg attempt "${ATTEMPT_ID:-unknown}" \
        --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
        --arg message "agent job failed before publishing a complete result" \
        --argjson exit_code "$exit_code" \
        '{type: $type, attempt: $attempt, execution: $execution, exit_code: $exit_code, message: $message}'
    exit "$exit_code"
}
trap emit_error ERR

require_value() {
    local name="$1"
    if [[ -z "${!name:-}" ]]; then
        printf '%s is required\n' "$name" >&2
        return 1
    fi
}

parse_gs_uri() {
    local uri="$1"
    if [[ ! "$uri" =~ ^gs://([^/]+)/(.+)$ ]]; then
        printf 'invalid GCS object URI: %s\n' "$uri" >&2
        return 1
    fi
    storage_bucket="${BASH_REMATCH[1]}"
    storage_object="${BASH_REMATCH[2]}"
}

storage_token() {
    curl --fail-with-body --silent --show-error \
        --header 'Metadata-Flavor: Google' \
        'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token' \
        | jq -er '.access_token'
}

download_object() {
    local uri="$1"
    local destination="$2"
    local storage_bucket storage_object encoded_object token
    parse_gs_uri "$uri"
    encoded_object="$(jq -rn --arg value "$storage_object" '$value | @uri')"
    token="$(storage_token)"
    curl --fail-with-body --silent --show-error \
        --retry 3 --retry-all-errors \
        --header "Authorization: Bearer ${token}" \
        --output "$destination" \
        "https://storage.googleapis.com/storage/v1/b/${storage_bucket}/o/${encoded_object}?alt=media"
}

upload_object_create_only() {
    local source="$1"
    local uri="$2"
    local storage_bucket storage_object encoded_object token response_path existing_path
    local http_code curl_exit upload_attempt
    parse_gs_uri "$uri"
    encoded_object="$(jq -rn --arg value "$storage_object" '$value | @uri')"
    token="$(storage_token)"
    response_path="$(mktemp "${workspace_root}/upload-response.XXXXXX")"
    existing_path="$(mktemp "${workspace_root}/existing-result.XXXXXX")"

    for upload_attempt in 1 2 3 4; do
        if http_code="$(
            curl --silent --show-error \
                --request POST \
                --header "Authorization: Bearer ${token}" \
                --header 'Content-Type: application/gzip' \
                --data-binary "@${source}" \
                --output "$response_path" \
                --write-out '%{http_code}' \
                "https://storage.googleapis.com/upload/storage/v1/b/${storage_bucket}/o?uploadType=media&ifGenerationMatch=0&name=${encoded_object}"
        )"; then
            curl_exit=0
        else
            curl_exit="$?"
        fi

        if (( curl_exit == 0 )) && [[ "$http_code" =~ ^2[0-9][0-9]$ ]]; then
            rm -f "$response_path" "$existing_path"
            return 0
        fi
        if [[ "$http_code" == 412 ]]; then
            download_object "$uri" "$existing_path"
            if cmp -s "$source" "$existing_path"; then
                rm -f "$response_path" "$existing_path"
                return 0
            fi
            printf 'result object already exists with different content: %s\n' "$uri" >&2
            rm -f "$response_path" "$existing_path"
            return 1
        fi
        if (( upload_attempt < 4 )); then
            sleep "$upload_attempt"
        fi
    done

    printf 'result upload failed with HTTP %s: ' "${http_code:-unknown}" >&2
    cat "$response_path" >&2
    printf '\n' >&2
    rm -f "$response_path" "$existing_path"
    return 1
}

file_digest() {
    sha256sum "$1" | awk '{print $1}'
}

require_value ATTEMPT_ID
require_value INPUT_URI
require_value OUTPUT_URI
require_value OPENROUTER_API_KEY

if [[ ! "$ATTEMPT_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
    printf 'ATTEMPT_ID contains unsupported characters\n' >&2
    exit 2
fi
if [[ -e "$checkout_dir" || -e "$result_dir" ]]; then
    printf 'workspace paths already exist under %s\n' "$workspace_root" >&2
    exit 2
fi
mkdir -p "$result_dir"

download_object "$INPUT_URI" "$input_path"

readonly input_version="$(jq -er '.version' "$input_path")"
readonly input_attempt="$(jq -er '.attempt_id' "$input_path")"
readonly dispatch_nonce="$(jq -er '.dispatch_nonce' "$input_path")"
readonly repository_url="$(jq -er '.repository_url' "$input_path")"
readonly git_commit="$(jq -er '.git_commit' "$input_path")"
readonly prompt="$(jq -er '.prompt | select(type == "string" and length > 0)' "$input_path")"
readonly agent_mode="$(jq -er '.agent_mode' "$input_path")"
readonly model="$(jq -er '.model' "$input_path")"
readonly thinking="$(jq -er '.thinking // "low"' "$input_path")"

if [[ "$input_version" != 2 || "$input_attempt" != "$ATTEMPT_ID" ]]; then
    printf 'input identity does not match this execution\n' >&2
    exit 2
fi
if [[ ! "$dispatch_nonce" =~ ^[0-9a-f]{32}$ ]]; then
    printf 'dispatch_nonce must be 32 lowercase hexadecimal characters\n' >&2
    exit 2
fi
if [[ ! "$git_commit" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'git_commit must be a full lowercase commit SHA\n' >&2
    exit 2
fi
if [[ "$repository_url" != https://github.com/*.git && ( "${FACTORY_AGENT_TEST:-}" != 1 || -n "${CLOUD_RUN_EXECUTION:-}" ) ]]; then
    printf 'repository_url must be a public GitHub HTTPS clone URL\n' >&2
    exit 2
fi
readonly prompt_bytes="$(printf '%s' "$prompt" | wc -c | tr -d ' ')"
if (( prompt_bytes > 65536 )); then
    printf 'prompt exceeds 64 KiB\n' >&2
    exit 2
fi
if [[ -z "$model" || ${#model} -gt 200 || -z "$thinking" || ${#thinking} -gt 32 ]]; then
    printf 'model or thinking setting is invalid\n' >&2
    exit 2
fi

case "$agent_mode" in
    read-only)
        readonly agent_tools="read,grep,find,ls"
        ;;
    write)
        readonly agent_tools="read,grep,find,ls,bash,edit,write"
        ;;
    *)
        printf 'agent_mode must be read-only or write\n' >&2
        exit 2
        ;;
esac

git init --quiet "$checkout_dir"
git -C "$checkout_dir" remote add origin "$repository_url"
git -C "$checkout_dir" fetch --quiet --depth=1 origin "$git_commit"
git -C "$checkout_dir" checkout --quiet --detach FETCH_HEAD
readonly base_commit="$(git -C "$checkout_dir" rev-parse HEAD)"
if [[ "$base_commit" != "$git_commit" ]]; then
    printf 'checked out commit does not match frozen input\n' >&2
    exit 2
fi

jq -nc \
    --arg type factory_agent_start \
    --arg attempt "$ATTEMPT_ID" \
    --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
    --arg dispatch_nonce "$dispatch_nonce" \
    --arg repository "$repository_url" \
    --arg commit "$base_commit" \
    --arg model "$model" \
    --arg mode "$agent_mode" \
    '{type: $type, attempt: $attempt, execution: $execution, repository: $repository, commit: $commit, model: $model, mode: $mode}'

trap - ERR
set +e
(
    cd "$checkout_dir"
    printf '%s' "$prompt" | pi \
        --mode json \
        --no-session \
        --no-approve \
        --no-extensions \
        --no-skills \
        --no-prompt-templates \
        --provider openrouter \
        --model "$model" \
        --thinking "$thinking" \
        --tools "$agent_tools"
) | tee "$raw_events_path" | jq --unbuffered -c '
    select(.type == "message_end" and .message.role == "assistant")
    | .message.content |= map(select(.type != "thinking"))
' | tee "$events_path"
pipeline_status=("${PIPESTATUS[@]}")
agent_exit_code="${pipeline_status[0]}"
for pipeline_exit_code in "${pipeline_status[@]:1}"; do
    if (( agent_exit_code == 0 && pipeline_exit_code != 0 )); then
        agent_exit_code="$pipeline_exit_code"
    fi
done
readonly agent_exit_code
set -e
trap emit_error ERR

git -C "$checkout_dir" add --intent-to-add .
git -C "$checkout_dir" status --short > "$status_path"
git -C "$checkout_dir" diff --binary "$base_commit" > "$patch_path"

readonly cost="$(
    jq -s '[.[] | select(.type == "message_end" and .message.role == "assistant") | (.message.usage.cost.total // 0)] | add // 0' \
        "$raw_events_path"
)"

jq -nc \
    --arg attempt_id "$ATTEMPT_ID" \
    --arg dispatch_nonce "$dispatch_nonce" \
    --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
    --arg commit "$base_commit" \
    --arg model "$model" \
    --arg mode "$agent_mode" \
    --arg status "$(cat "$status_path")" \
    --argjson cost_usd "$cost" \
    --argjson exit_code "$agent_exit_code" \
    '{attempt_id: $attempt_id, dispatch_nonce: $dispatch_nonce, execution: $execution, commit: $commit, model: $model, mode: $mode, exit_code: $exit_code, cost_usd: $cost_usd, git_status: $status}' \
    > "${result_dir}/result.json"

jq -nc \
    --arg attempt_id "$ATTEMPT_ID" \
    --arg dispatch_nonce "$dispatch_nonce" \
    --arg input_uri "$INPUT_URI" \
    --arg output_uri "$OUTPUT_URI" \
    --arg commit "$base_commit" \
    --arg result_sha256 "$(file_digest "${result_dir}/result.json")" \
    --arg patch_sha256 "$(file_digest "$patch_path")" \
    --arg status_sha256 "$(file_digest "$status_path")" \
    --arg events_sha256 "$(file_digest "$events_path")" \
    '{version: 2, attempt_id: $attempt_id, dispatch_nonce: $dispatch_nonce, input_uri: $input_uri, output_uri: $output_uri, commit: $commit, files: {"result.json": $result_sha256, "changes.patch": $patch_sha256, "status.txt": $status_sha256, "events.jsonl": $events_sha256}}' \
    > "${result_dir}/manifest.json"

tar -czf "$archive_path" -C "$result_dir" \
    manifest.json result.json changes.patch status.txt events.jsonl
upload_object_create_only "$archive_path" "$OUTPUT_URI"

jq -nc \
    --arg type factory_agent_summary \
    --arg attempt "$ATTEMPT_ID" \
    --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
    --arg commit "$base_commit" \
    --arg output_uri "$OUTPUT_URI" \
    --argjson cost_usd "$cost" \
    --argjson exit_code "$agent_exit_code" \
    '{type: $type, attempt: $attempt, execution: $execution, commit: $commit, exit_code: $exit_code, cost_usd: $cost_usd, output_uri: $output_uri}'

exit "$agent_exit_code"
