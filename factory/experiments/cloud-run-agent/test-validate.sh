#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT
readonly fake_bin="${temp_root}/bin"
mkdir -p "$fake_bin"

cat > "${fake_bin}/gcloud" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case "$1 $2 $3" in
    'projects describe factory-505220')
        printf 'factory-505220\n'
        ;;
    'run jobs describe')
        jq -nc '{spec:{template:{spec:{taskCount:1,template:{spec:{maxRetries:0,serviceAccountName:"factory-agent-experiment@factory-505220.iam.gserviceaccount.com",containers:[{image:"example.invalid/agent@sha256:abc",env:[{name:"OPENROUTER_API_KEY",valueFrom:{secretKeyRef:{name:"openrouter-api-key",key:"1"}}}]}]}}}}}}'
        ;;
    'secrets versions describe')
        printf 'ENABLED\n'
        ;;
    secrets\ get-iam-policy\ *)
        if [[ "${FAKE_SECRET_ACCESS:-true}" == true ]]; then
            jq -nc '{bindings:[{role:"roles/secretmanager.secretAccessor",members:["serviceAccount:factory-agent-experiment@factory-505220.iam.gserviceaccount.com","serviceAccount:another@example.invalid"]}]}'
        else
            jq -nc '{bindings:[]}'
        fi
        ;;
    'storage buckets describe')
        jq -nc --arg pap "${FAKE_BUCKET_PAP:-enforced}" --argjson uniform "${FAKE_BUCKET_UNIFORM:-true}" \
            '{public_access_prevention:$pap,uniform_bucket_level_access:$uniform}'
        ;;
    'storage buckets get-iam-policy')
        jq -nc '{bindings:[{role:"roles/storage.objectUser",members:["serviceAccount:factory-agent-experiment@factory-505220.iam.gserviceaccount.com","serviceAccount:another@example.invalid"]}]}'
        ;;
    *)
        printf 'unexpected gcloud call: %s\n' "$*" >&2
        exit 1
        ;;
esac
EOF
chmod 0755 "${fake_bin}/gcloud"

PATH="${fake_bin}:$PATH" PROJECT_ID=factory-505220 \
    "${script_dir}/validate.sh" > "${temp_root}/valid-output"
grep -F -- 'Profile is ready.' "${temp_root}/valid-output" >/dev/null

set +e
PATH="${fake_bin}:$PATH" PROJECT_ID=factory-505220 FAKE_SECRET_ACCESS=false \
    "${script_dir}/validate.sh" > "${temp_root}/invalid-output" 2>&1
invalid_exit="$?"
set -e
[[ "$invalid_exit" -eq 1 ]]
grep -F -- 'Job is missing roles/secretmanager.secretAccessor' "${temp_root}/invalid-output" >/dev/null

set +e
PATH="${fake_bin}:$PATH" PROJECT_ID=factory-505220 \
FAKE_BUCKET_PAP=inherited FAKE_BUCKET_UNIFORM=false \
    "${script_dir}/validate.sh" > "${temp_root}/public-bucket-output" 2>&1
public_bucket_exit="$?"
set -e
[[ "$public_bucket_exit" -eq 1 ]]
grep -F -- 'Artifact bucket must enforce public access prevention' "${temp_root}/public-bucket-output" >/dev/null
grep -F -- 'Artifact bucket must use uniform bucket-level access' "${temp_root}/public-bucket-output" >/dev/null

printf 'cloud-run validation tests passed\n'
