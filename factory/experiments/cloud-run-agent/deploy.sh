#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly region="${REGION:-europe-west1}"
readonly repository="${ARTIFACT_REPOSITORY:-experiments}"
readonly job_name="${JOB_NAME:-factory-agent-experiment}"
readonly service_account_name="${SERVICE_ACCOUNT_NAME:-factory-agent-experiment}"
readonly secret_name="${OPENROUTER_SECRET_NAME:-openrouter-api-key}"
readonly artifact_bucket="${ARTIFACT_BUCKET:-${project_id}-factory-agent-artifacts}"
readonly service_account="${service_account_name}@${project_id}.iam.gserviceaccount.com"
readonly source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

image_tag=""
if [[ -n "${IMAGE_TAG:-}" ]]; then
    image_tag="$IMAGE_TAG"
else
    source_status="$(
        git -C "$source_dir" status --short --untracked-files=all -- .
    )"
    readonly source_status
    if [[ -n "$source_status" ]]; then
        printf 'Build context has uncommitted changes. Commit them or set a unique IMAGE_TAG.\n' >&2
        exit 2
    fi
    image_tag="$(git -C "$source_dir" rev-parse --short=12 HEAD)"
fi
readonly image_tag

readonly tagged_image="${region}-docker.pkg.dev/${project_id}/${repository}/factory-agent:${image_tag}"

gcloud services enable \
    artifactregistry.googleapis.com \
    cloudbuild.googleapis.com \
    run.googleapis.com \
    secretmanager.googleapis.com \
    storage.googleapis.com \
    --project "$project_id" \
    --quiet

gcloud beta services identity create \
    --service run.googleapis.com \
    --project "$project_id" \
    --quiet >/dev/null

if ! gcloud artifacts repositories describe "$repository" \
    --location "$region" --project "$project_id" >/dev/null 2>&1; then
    gcloud artifacts repositories create "$repository" \
        --repository-format docker \
        --location "$region" \
        --description "Factory agent experiments" \
        --project "$project_id" \
        --quiet
fi

if ! gcloud iam service-accounts describe "$service_account" \
    --project "$project_id" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$service_account_name" \
        --display-name "Factory agent experiment" \
        --project "$project_id" \
        --quiet
fi

if ! gcloud storage buckets describe "gs://${artifact_bucket}" \
    --project "$project_id" >/dev/null 2>&1; then
    gcloud storage buckets create "gs://${artifact_bucket}" \
        --location "$region" \
        --uniform-bucket-level-access \
        --public-access-prevention \
        --project "$project_id" \
        --quiet
fi

gcloud storage buckets update "gs://${artifact_bucket}" \
    --lifecycle-file "${source_dir}/bucket-lifecycle.json" \
    --uniform-bucket-level-access \
    --public-access-prevention \
    --project "$project_id" \
    --quiet >/dev/null

gcloud storage buckets add-iam-policy-binding "gs://${artifact_bucket}" \
    --member "serviceAccount:${service_account}" \
    --role roles/storage.objectUser \
    --project "$project_id" \
    --quiet >/dev/null

readonly build_service_account="$(
    gcloud builds get-default-service-account \
        --project "$project_id"
)"
gcloud projects add-iam-policy-binding "$project_id" \
    --member "serviceAccount:${build_service_account}" \
    --role roles/cloudbuild.builds.builder \
    --condition None \
    --quiet >/dev/null

if ! gcloud secrets describe "$secret_name" \
    --project "$project_id" >/dev/null 2>&1; then
    printf 'Secret %s does not exist in project %s.\n' "$secret_name" "$project_id" >&2
    printf 'Create it before deploying; see README.md.\n' >&2
    exit 1
fi

secret_version="$(
    gcloud secrets versions list "$secret_name" \
        --filter 'state=ENABLED' \
        --sort-by='~createTime' \
        --limit 1 \
        --format 'value(name)' \
        --project "$project_id"
)"
readonly secret_version
if [[ ! "$secret_version" =~ ^[1-9][0-9]*$ ]]; then
    printf 'Secret %s has no enabled numeric version.\n' "$secret_name" >&2
    exit 1
fi

gcloud secrets add-iam-policy-binding "$secret_name" \
    --member "serviceAccount:${service_account}" \
    --role roles/secretmanager.secretAccessor \
    --project "$project_id" \
    --quiet >/dev/null

gcloud builds submit "$source_dir" \
    --tag "$tagged_image" \
    --project "$project_id" \
    --quiet

image="$(
    gcloud artifacts docker images describe "$tagged_image" \
        --project "$project_id" \
        --format 'value(image_summary.fully_qualified_digest)'
)"
readonly image
if [[ -z "$image" ]]; then
    printf 'Could not resolve image digest for %s\n' "$tagged_image" >&2
    exit 1
fi

job_flags=(
    --image "$image"
    --region "$region"
    --project "$project_id"
    --service-account "$service_account"
    --tasks 1
    --cpu 1
    --memory 2Gi
    --task-timeout 10m
    --max-retries 0
    --set-secrets "OPENROUTER_API_KEY=${secret_name}:${secret_version}"
    --quiet
)

job_exists() {
    gcloud run jobs describe "$job_name" \
        --region "$region" --project "$project_id" >/dev/null 2>&1
}

wait_for_job_ready() {
    local attempt_index
    local ready_state
    local ready_message
    for attempt_index in $(seq 1 60); do
        ready_state="$(
            gcloud run jobs describe "$job_name" \
                --region "$region" \
                --project "$project_id" \
                --format 'value(status.conditions[0].status)' \
                2>/dev/null || true
        )"
        ready_message="$(
            gcloud run jobs describe "$job_name" \
                --region "$region" \
                --project "$project_id" \
                --format 'value(status.conditions[0].message)' \
                2>/dev/null || true
        )"
        case "$ready_state" in
            True)
                return 0
                ;;
            False)
                printf 'Cloud Run Job became unready: %s\n' "$ready_message" >&2
                return 1
                ;;
        esac
        sleep 5
    done
    printf 'Cloud Run Job was not ready after five minutes\n' >&2
    return 1
}

if ! job_exists; then
    if ! gcloud run jobs create "$job_name" "${job_flags[@]}"; then
        job_exists || exit 1
    fi
    wait_for_job_ready
fi

gcloud run jobs update "$job_name" "${job_flags[@]}"

printf 'IMAGE=%s\n' "$image"
printf 'IMAGE_TAG=%s\n' "$tagged_image"
printf 'JOB=%s\n' "$job_name"
printf 'ARTIFACT_BUCKET=%s\n' "$artifact_bucket"
printf 'OPENROUTER_SECRET_VERSION=%s\n' "$secret_version"
printf '\nFactory profile:\n'
printf '[[cloud_profiles]]\n'
printf 'id = "%s"\n' "$job_name"
printf 'kind = "cloud_run"\n'
printf 'project = "%s"\n' "$project_id"
printf 'region = "%s"\n' "$region"
printf 'job = "%s"\n' "$job_name"
printf 'artifact_bucket = "%s"\n' "$artifact_bucket"
printf 'image = "%s"\n' "$image"
printf 'job_service_account = "%s"\n' "$service_account"
