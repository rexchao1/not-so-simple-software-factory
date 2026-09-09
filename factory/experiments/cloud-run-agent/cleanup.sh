#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly region="${REGION:-europe-west1}"
readonly job_name="${JOB_NAME:-factory-agent-experiment}"
readonly apply="${APPLY:-false}"

case "$apply" in
    true | false) ;;
    *)
        printf 'APPLY must be true or false\n' >&2
        exit 2
        ;;
esac

access_token="$(gcloud auth print-access-token)"
parent="projects/${project_id}/locations/${region}/jobs/${job_name}"
page_token=""
execution_names=()

while :; do
    url="https://run.googleapis.com/v2/${parent}/executions?pageSize=100"
    if [[ -n "$page_token" ]]; then
        encoded_token="$(jq -rn --arg value "$page_token" '$value | @uri')"
        url="${url}&pageToken=${encoded_token}"
    fi
    response="$(
        curl --fail-with-body --silent --show-error \
            --header "Authorization: Bearer ${access_token}" \
            "$url"
    )"
    while IFS= read -r execution_name; do
        [[ -n "$execution_name" ]] && execution_names+=("$execution_name")
    done < <(jq -r '.executions[]? | select(.completionTime != null) | .name' <<< "$response")
    page_token="$(jq -r '.nextPageToken // ""' <<< "$response")"
    [[ -n "$page_token" ]] || break
done

if (( ${#execution_names[@]} == 0 )); then
    printf 'No completed executions to clean up.\n'
    exit 0
fi

printf 'Completed executions:\n'
printf '  %s\n' "${execution_names[@]##*/}"
if [[ "$apply" != true ]]; then
    printf '\nDry run. Set APPLY=true to delete these execution records.\n'
    exit 0
fi

for execution_name in "${execution_names[@]}"; do
    curl --fail-with-body --silent --show-error \
        --request DELETE \
        --header "Authorization: Bearer ${access_token}" \
        "https://run.googleapis.com/v2/${execution_name}" >/dev/null
    printf 'Cleanup requested: %s\n' "${execution_name##*/}"
done
