#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly region="${REGION:-europe-west1}"
readonly job_name="${JOB_NAME:-factory-agent-experiment}"
readonly artifact_bucket="${ARTIFACT_BUCKET:-${project_id}-factory-agent-artifacts}"
readonly secret_name="${OPENROUTER_SECRET_NAME:-openrouter-api-key}"
readonly expected_service_account="${SERVICE_ACCOUNT_NAME:-factory-agent-experiment}@${project_id}.iam.gserviceaccount.com"
readonly job_json="$(mktemp)"
readonly bucket_json="$(mktemp)"
trap 'rm -f "$job_json" "$bucket_json"' EXIT

pass() {
    printf '✓ %s\n' "$1"
}

fail() {
    printf '✗ %s\n' "$1" >&2
    failures=$((failures + 1))
}

failures=0
printf 'Cloud Run agent profile\n\n'

if gcloud projects describe "$project_id" \
    --format='value(projectId)' | grep -Fx "$project_id" >/dev/null; then
    pass "Project is active: ${project_id}"
else
    fail "Project is unavailable: ${project_id}"
fi

if gcloud run jobs describe "$job_name" \
    --region "$region" --project "$project_id" --format=json > "$job_json"; then
    pass "Job exists: ${job_name}"
else
    fail "Job is unavailable: ${job_name}"
fi

if [[ -s "$job_json" ]]; then
    image="$(jq -r '.spec.template.spec.template.spec.containers[0].image // ""' "$job_json")"
    retries="$(jq -r '.spec.template.spec.template.spec.maxRetries // ""' "$job_json")"
    tasks="$(jq -r '.spec.template.spec.taskCount // ""' "$job_json")"
    service_account="$(jq -r '.spec.template.spec.template.spec.serviceAccountName // ""' "$job_json")"
    secret_key="$(jq -r '.spec.template.spec.template.spec.containers[0].env[]? | select(.name == "OPENROUTER_API_KEY") | .valueFrom.secretKeyRef.key' "$job_json")"
    secret_resource="$(jq -r '.spec.template.spec.template.spec.containers[0].env[]? | select(.name == "OPENROUTER_API_KEY") | .valueFrom.secretKeyRef.name' "$job_json")"

    [[ "$image" == *@sha256:* ]] && pass "Image is pinned by digest" || fail "Job image is not pinned by digest"
    [[ "$retries" == 0 ]] && pass "Native retries are zero" || fail "Native retries must be zero"
    [[ "$tasks" == 1 ]] && pass "Task count is one" || fail "Task count must be one"
    [[ "$service_account" == "$expected_service_account" ]] \
        && pass "Job service account matches" \
        || fail "Job service account does not match ${expected_service_account}"
    [[ "$secret_resource" == "$secret_name" && "$secret_key" =~ ^[1-9][0-9]*$ ]] \
        && pass "Model secret uses pinned version ${secret_key}" \
        || fail "Model secret must use a numeric pinned version"

    if [[ "$secret_resource" == "$secret_name" && "$secret_key" =~ ^[1-9][0-9]*$ ]] \
        && gcloud secrets versions describe "$secret_key" --secret "$secret_name" \
            --project "$project_id" --format='value(state)' | grep -Fx ENABLED >/dev/null; then
        pass "Pinned model secret version is enabled"
    else
        fail "Pinned model secret version is unavailable or disabled"
    fi
fi

if gcloud secrets get-iam-policy "$secret_name" \
    --project "$project_id" --format=json \
    | jq -e --arg member "serviceAccount:${expected_service_account}" \
        'any(.bindings[]?; .role == "roles/secretmanager.secretAccessor" and any(.members[]?; . == $member))' \
        >/dev/null; then
    pass "Job can access the model secret"
else
    fail "Job is missing roles/secretmanager.secretAccessor on the model secret"
fi

if gcloud storage buckets describe "gs://${artifact_bucket}" \
    --project "$project_id" --format=json > "$bucket_json" 2>/dev/null; then
    pass "Artifact bucket exists: ${artifact_bucket}"
else
    fail "Artifact bucket is unavailable: ${artifact_bucket}"
fi

if [[ -s "$bucket_json" ]]; then
    bucket_public_access="$(jq -r '.public_access_prevention // ""' "$bucket_json")"
    bucket_uniform_access="$(jq -r '.uniform_bucket_level_access // false' "$bucket_json")"
    [[ "$bucket_public_access" == enforced ]] \
        && pass "Artifact bucket prevents public access" \
        || fail "Artifact bucket must enforce public access prevention"
    [[ "$bucket_uniform_access" == true ]] \
        && pass "Artifact bucket uses uniform access" \
        || fail "Artifact bucket must use uniform bucket-level access"
fi

if gcloud storage buckets get-iam-policy "gs://${artifact_bucket}" \
    --project "$project_id" --format=json \
    | jq -e --arg member "serviceAccount:${expected_service_account}" \
        'any(.bindings[]?; .role == "roles/storage.objectUser" and any(.members[]?; . == $member))' \
        >/dev/null; then
    pass "Job can read and write attempt artifacts"
else
    fail "Job is missing roles/storage.objectUser on the artifact bucket"
fi

if [[ "$failures" -ne 0 ]]; then
    printf '\nProfile is not ready: %d check(s) failed.\n' "$failures" >&2
    exit 1
fi

printf '\nProfile is ready.\n'
