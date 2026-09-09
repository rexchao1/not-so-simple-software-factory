# Cloud Run agent proof of concept

This proof of concept runs Pi as a disposable Cloud Run Job while keeping its
result after the container exits. It is intentionally separate from Factory's
Worker protocol.

One reusable Job handles every execution. A run freezes a public GitHub
repository to one full commit, uploads an input document to a private GCS
bucket, starts the Job, and verifies the returned result archive. Completed
Cloud Run execution records are deleted by default. The reusable Job remains.

The proof trusts the selected repository and prompt. Repository code can read
the OpenRouter credential mounted in its container and can make outbound
network requests. This is managed disposable compute, not a sandbox for hostile
code.

## What the operator provisions

`deploy.sh` performs the manual project setup:

- one Artifact Registry repository;
- one Cloud Run Job service account;
- one private artifact bucket with seven-day object cleanup;
- read and write access from that service account to the artifact bucket;
- access to one existing OpenRouter secret;
- one reusable Cloud Run Job with a digest-pinned image, one task, and zero
  native retries.

The OpenRouter secret must exist before deployment. Its latest enabled numeric
version is pinned on the Job. Factory does not receive or store the secret.

## Files

- `Dockerfile` builds a pinned Node and Pi image.
- `run-agent.sh` reads frozen input, checks out the exact commit, runs Pi, and
  uploads one immutable result archive.
- `deploy.sh` creates or updates the manually managed Google Cloud resources.
- `validate.sh` checks the deployed profile and prints actionable failures.
- `execute.sh` resolves a commit, starts one execution, verifies its archive,
  and requests execution cleanup.
- `inspect.sh` resumes polling and artifact recovery from a durable launch
  record.
- `resolve-git-ref.sh` freezes branches, lightweight tags, and annotated tags.
- `cleanup.sh` lists or deletes old completed execution records.
- `test-run-agent.sh` tests checkout, invocation, redaction, failure artifacts,
  and input identity without a real model call.

## Local checks

```sh
bash -n experiments/cloud-run-agent/*.sh
experiments/cloud-run-agent/test-run-agent.sh
experiments/cloud-run-agent/test-control.sh
experiments/cloud-run-agent/test-validate.sh
```

With a Docker daemon running:

```sh
docker build --platform linux/amd64 \
  --tag factory-agent-experiment \
  experiments/cloud-run-agent
```

## Deploy to the Factory Google Cloud project

The project name is `Factory` and its project ID is `factory-505220`. Commands
use the project ID explicitly and do not depend on the global `gcloud` project.

Create the secret without placing the value in shell history:

```sh
export PROJECT_ID=factory-505220
printf '%s' "$OPENROUTER_API_KEY" | \
  gcloud secrets create openrouter-api-key \
    --data-file=- \
    --replication-policy=automatic \
    --project "$PROJECT_ID"
```

If it already exists, add a version instead:

```sh
printf '%s' "$OPENROUTER_API_KEY" | \
  gcloud secrets versions add openrouter-api-key \
    --data-file=- \
    --project "$PROJECT_ID"
```

Build the image and deploy the reusable Job:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
  experiments/cloud-run-agent/deploy.sh
```

By default, a clean experiment directory is required and the image is tagged
with the Git commit. Set a unique `IMAGE_TAG` only for an intentional
development deployment. The Job always uses the resolved image digest.

Validate the profile:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
  experiments/cloud-run-agent/validate.sh
```

## Run an agent

The safe smoke prompt uses read-only Pi tools:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
  experiments/cloud-run-agent/execute.sh \
  experiments/cloud-run-agent/smoke-prompt.txt
```

To produce a patch:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
AGENT_MODE=write \
  experiments/cloud-run-agent/execute.sh \
  experiments/cloud-run-agent/write-smoke-prompt.txt
```

The run stores verified files under
`./factory-agent-results/<attempt-id>/`. The same archive remains in GCS until
the bucket's lifecycle rule deletes it.

`execute.sh` writes `launch.json` immediately after Cloud Run accepts the run.
If the terminal closes or polling fails, use the printed resume command. You
can also resume directly:

```sh
PROJECT_ID=factory-505220 \
  experiments/cloud-run-agent/inspect.sh <attempt-id>
```

Set `DELETE_EXECUTION_ON_TERMINAL=false` to retain one execution for console
inspection. The default requests deletion after terminal state and artifact
verification.

## Input contract

The only per-execution environment overrides are `ATTEMPT_ID`, `INPUT_URI`, and
`OUTPUT_URI`. The private input object contains:

```json
{
  "version": 2,
  "attempt_id": "attempt-...",
  "dispatch_nonce": "32 lowercase hexadecimal characters",
  "repository_url": "https://github.com/owainlewis/factory.git",
  "git_commit": "full 40 character commit",
  "prompt": "the agent task",
  "agent_mode": "read-only",
  "model": "deepseek/deepseek-v4-flash",
  "thinking": "low"
}
```

The first proof supports public GitHub HTTPS repositories only. `execute.sh`
resolves a branch or tag before uploading the input. Set `GIT_COMMIT` to supply
an already resolved full commit.

## Result contract

`attempt-result.tar.gz` contains exactly:

```text
manifest.json
result.json
changes.patch
status.txt
events.jsonl
```

The manifest binds the archive to the Attempt and frozen commit and includes a
SHA-256 digest for every result file. `execute.sh` checks the paths, identity,
and every digest before accepting the result. Raw Pi reasoning and the prompt
are not included in the result archive or mirrored to container logs.

## Clean old execution records

List completed execution records without deleting them:

```sh
PROJECT_ID=factory-505220 \
  experiments/cloud-run-agent/cleanup.sh
```

Delete every completed execution record for the reusable Job:

```sh
PROJECT_ID=factory-505220 APPLY=true \
  experiments/cloud-run-agent/cleanup.sh
```

This does not delete the reusable Job or the GCS artifacts.

## Deliberate limits

- Manual executions only.
- One public repository and one Pi process per execution.
- No Factory Work, Attempt, lease, or cancellation integration yet.
- No private repository authentication.
- No branch push or pull request publishing.
- No untrusted repository isolation.
- No automatic infrastructure provisioning from the Factory UI.
