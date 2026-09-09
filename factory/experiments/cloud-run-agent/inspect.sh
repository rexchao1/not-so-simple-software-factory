#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly attempt_id="${1:?usage: inspect.sh ATTEMPT_ID}"
readonly wait_seconds="${WAIT_SECONDS:-600}"
readonly delete_on_terminal="${DELETE_EXECUTION_ON_TERMINAL:-true}"
readonly output_root="${OUTPUT_ROOT:-./factory-agent-results}"
readonly attempt_output="${output_root}/${attempt_id}"
readonly launch_path="${attempt_output}/launch.json"

if [[ ! "$wait_seconds" =~ ^[1-9][0-9]*$ ]]; then
    printf 'WAIT_SECONDS must be a positive integer\n' >&2
    exit 2
fi
case "$delete_on_terminal" in
    true | false) ;;
    *)
        printf 'DELETE_EXECUTION_ON_TERMINAL must be true or false\n' >&2
        exit 2
        ;;
esac
if [[ ! -f "$launch_path" ]]; then
    printf 'launch record does not exist: %s\n' "$launch_path" >&2
    exit 2
fi

readonly launch_project="$(jq -er '.project' "$launch_path")"
readonly region="$(jq -er '.region' "$launch_path")"
readonly job_name="$(jq -er '.job' "$launch_path")"
readonly recorded_attempt="$(jq -er '.attempt' "$launch_path")"
readonly dispatch_nonce="$(jq -r '.dispatch_nonce // ""' "$launch_path")"
readonly git_commit="$(jq -er '.commit' "$launch_path")"
readonly input_uri="$(jq -er '.input_uri' "$launch_path")"
readonly output_uri="$(jq -er '.output_uri' "$launch_path")"

if [[ "$launch_project" != "$project_id" || "$recorded_attempt" != "$attempt_id" ]]; then
    printf 'launch record identity does not match this inspection\n' >&2
    exit 2
fi
if [[ ! "$git_commit" =~ ^[0-9a-f]{40}$ ]]; then
    printf 'launch record contains an invalid commit identity\n' >&2
    exit 2
fi
if [[ -n "$dispatch_nonce" && ! "$dispatch_nonce" =~ ^[0-9a-f]{32}$ ]]; then
    printf 'launch record contains an invalid dispatch nonce\n' >&2
    exit 2
fi

readonly access_token="$(gcloud auth print-access-token)"
execution_name="$(jq -r '.execution_name // ""' "$launch_path")"
execution_id="$(jq -r '.execution // ""' "$launch_path")"

find_execution() {
    local page_token='' list_url list_response match next_page_token
    while true; do
        list_url="https://run.googleapis.com/v2/projects/${project_id}/locations/${region}/jobs/${job_name}/executions?pageSize=100"
        if [[ -n "$page_token" ]]; then
            list_url="${list_url}&pageToken=$(jq -rn --arg value "$page_token" '$value | @uri')"
        fi
        list_response="$(
            curl --fail-with-body --silent --show-error \
                --header "Authorization: Bearer ${access_token}" \
                "$list_url"
        )"
        match="$(
            jq -r \
                --arg attempt "$attempt_id" \
                --arg input "$input_uri" \
                --arg output "$output_uri" '
                first(.executions[]? | select(
                    any(.template.containers[].env[]?; .name == "ATTEMPT_ID" and .value == $attempt)
                    and any(.template.containers[].env[]?; .name == "INPUT_URI" and .value == $input)
                    and any(.template.containers[].env[]?; .name == "OUTPUT_URI" and .value == $output)
                ) | .name) // ""
                ' <<< "$list_response"
        )"
        if [[ -n "$match" ]]; then
            printf '%s\n' "$match"
            return 0
        fi
        next_page_token="$(jq -r '.nextPageToken // ""' <<< "$list_response")"
        [[ -n "$next_page_token" ]] || return 1
        page_token="$next_page_token"
    done
}

if [[ -z "$execution_name" ]]; then
    readonly reconcile_count="$((wait_seconds / 5 + 1))"
    for _reconcile_index in $(seq 1 "$reconcile_count"); do
        if execution_name="$(find_execution)"; then
            execution_id="${execution_name##*/}"
            launch_update="$(mktemp "${attempt_output}/launch.XXXXXX")"
            jq --arg execution_name "$execution_name" --arg execution "$execution_id" \
                '.execution_name = $execution_name | .execution = $execution | .dispatch_state = "reconciled"' \
                "$launch_path" > "$launch_update"
            mv "$launch_update" "$launch_path"
            printf 'Reconciled execution: %s\n' "$execution_id"
            break
        fi
        sleep 5
    done
fi
readonly execution_name execution_id
if [[ ! "$execution_name" =~ ^projects/${project_id}/locations/${region}/jobs/${job_name}/executions/${execution_id}$ ]]; then
    printf 'No matching Cloud Run execution is visible yet; retry this inspect command.\n' >&2
    exit 1
fi

verify_archive() {
    local verified_archive_path="$1"
    local archive_listing expected_listing artifact_name expected_digest actual_digest
    archive_listing="$(tar -tzf "$verified_archive_path")"
    expected_listing=$'manifest.json\nresult.json\nchanges.patch\nstatus.txt\nevents.jsonl'
    if [[ "$archive_listing" != "$expected_listing" ]]; then
        printf 'result archive contains unexpected paths\n' >&2
        return 1
    fi
    tar -xzf "$verified_archive_path" -C "$attempt_output"
    for artifact_name in result.json changes.patch status.txt events.jsonl; do
        expected_digest="$(jq -er --arg name "$artifact_name" '.files[$name]' "${attempt_output}/manifest.json")"
        if command -v sha256sum >/dev/null 2>&1; then
            actual_digest="$(sha256sum "${attempt_output}/${artifact_name}" | awk '{print $1}')"
        else
            actual_digest="$(shasum -a 256 "${attempt_output}/${artifact_name}" | awk '{print $1}')"
        fi
        if [[ "$actual_digest" != "$expected_digest" ]]; then
            printf 'artifact digest mismatch: %s\n' "$artifact_name" >&2
            return 1
        fi
    done
    if [[ "$(jq -er '.attempt_id' "${attempt_output}/manifest.json")" != "$attempt_id" ]] \
        || [[ "$(jq -er '.commit' "${attempt_output}/manifest.json")" != "$git_commit" ]]; then
        printf 'result archive identity does not match the frozen input\n' >&2
        return 1
    fi
    if [[ -n "$dispatch_nonce" ]] \
        && [[ "$(jq -er '.dispatch_nonce' "${attempt_output}/manifest.json")" != "$dispatch_nonce" ]]; then
        printf 'result archive dispatch nonce does not match the immutable input\n' >&2
        return 1
    fi
}

readonly execution_url="https://run.googleapis.com/v2/${execution_name}"
completion_state=CONDITION_PENDING
execution_response='{}'
readonly poll_count="$((wait_seconds / 5 + 1))"
for _poll_index in $(seq 1 "$poll_count"); do
    execution_response="$(
        curl --fail-with-body --silent --show-error \
            --header "Authorization: Bearer ${access_token}" \
            "$execution_url"
    )"
    completion_state="$(
        jq -r 'first(.conditions[]? | select(.type == "Completed") | .state) // "CONDITION_PENDING"' \
            <<< "$execution_response"
    )"
    case "$completion_state" in
        CONDITION_SUCCEEDED | CONDITION_FAILED)
            break
            ;;
        *)
            completion_state=CONDITION_PENDING
            ;;
    esac
    sleep 5
done

readonly archive_path="${attempt_output}/attempt-result.tar.gz"
artifact_available=false
if gcloud storage cp "$output_uri" "$archive_path" \
    --project "$project_id" --quiet 2>/dev/null; then
    verify_archive "$archive_path"
    artifact_available=true
    printf 'Verified result: %s\n' "$attempt_output"
fi

jq -n \
    --arg attempt "$attempt_id" \
    --arg execution "$execution_id" \
    --arg state "$completion_state" \
    --arg commit "$git_commit" \
    --arg input_uri "$input_uri" \
    --arg output_uri "$output_uri" \
    --arg log_uri "$(jq -r '.logUri // ""' <<< "$execution_response")" \
    --argjson artifact_available "$artifact_available" \
    '{attempt: $attempt, execution: $execution, state: $state, commit: $commit, input_uri: $input_uri, output_uri: $output_uri, artifact_available: $artifact_available, log_uri: $log_uri}' \
    > "${attempt_output}/execution.json"

if [[ "$delete_on_terminal" == true \
    && "$completion_state" != CONDITION_PENDING \
    && "$artifact_available" == true ]]; then
    curl --fail-with-body --silent --show-error \
        --request DELETE \
        --header "Authorization: Bearer ${access_token}" \
        "$execution_url" >/dev/null
    printf 'Execution cleanup requested: %s\n' "$execution_id"
else
    printf 'Execution retained: https://console.cloud.google.com/run/jobs/executions/details/%s/%s?project=%s\n' \
        "$region" "$execution_id" "$project_id"
fi

if [[ "$completion_state" != CONDITION_SUCCEEDED ]]; then
    jq -c '{name, conditions}' <<< "$execution_response" >&2
    exit 1
fi
if [[ "$artifact_available" != true ]]; then
    printf 'execution succeeded without a result artifact: %s\n' "$output_uri" >&2
    exit 1
fi
