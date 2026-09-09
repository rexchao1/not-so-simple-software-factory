# Factory command-line interface

> **Status:** Superseded command contract, not implemented. Its process
> boundaries remain useful, but the
> [agent-directed software factory](../software-factory/design.md) owns the
> current proposed command surface.

> **Automation boundary:** The external `factory ingest github` role and
> standalone schedule resource in earlier revisions are superseded by
> control-plane typed Automations. This CLI proposal does not define an
> Automation command surface; that is deferred until the accepted API ships.

## Goal

Factory will ship one Go binary named `factory`. It will preserve the current
control-plane and worker process boundary while giving operators one command
surface.

The key rule is:

> `factory run` submits a durable task. It never starts an agent inside the CLI
> process.

A one-off run therefore uses the same history, assignment, retry, and metrics as
work created by the UI, a schedule, or source ingest.

## Command tree

```text
factory start [--listen ADDRESS] [--database PATH]
              [--worker-config PATH ...]
factory server [--listen ADDRESS] [--database PATH]
factory worker [--config PATH]
factory worker identity [--config PATH]
factory worker cleanup [--config PATH] [--confirm] ATTEMPT_ID

factory run [--file PATH] [--title TITLE]
            [--workflow NAME_OR_ID] [--worker NAME_OR_ID]
            [--repository KEY_OR_ID] [--timeout DURATION]
            [--request-key KEY] [--wait] [CONTEXT]
factory status

factory tasks list [--state STATE] [--limit N] [--cursor CURSOR]
factory tasks show TASK_ID
factory tasks cancel [--confirm] TASK_ID
factory tasks retry TASK_ID
factory tasks delete [--confirm] TASK_ID

factory workers list [--limit N] [--cursor CURSOR]
factory workers show NAME_OR_ID

factory workflows list [--enabled BOOL] [--limit N] [--cursor CURSOR]
factory workflows show NAME_OR_ID
factory workflows apply FILE
factory workflows enable NAME_OR_ID
factory workflows disable NAME_OR_ID

factory version
```

Workflow commands ship only after their API exists. The first CLI slice can
cover `start`, `server`, `worker`, `run`, `status`, `tasks`, `workers`, and
`version`. Automation commands require a later CLI revision built on the typed
control-plane API.

## Process roles

`factory server` runs the current control plane and embedded UI.

`factory worker` runs one current worker identity and agent runtime.

`factory start` is a foreground Unix supervisor. It starts the server, waits for
health, starts one or more selected workers, waits for fresh healthy
registrations, and stops all children when one fails or the operator sends a
signal.

Finite commands are HTTP clients. They never open SQLite, inspect worker data,

## Manual runs

Example:

```sh
factory run \
  --workflow code-review \
  --worker local-claude \
  --repository factory \
  "Review this merge request: https://example.test/team/factory/pull/42"
```

Context stays free text. It may be:

- a ticket such as `PROJ-123`;
- a merge request or pull request URL;
- a repository and branch instruction;
- an ordinary prompt.

The workflow supplies repeatable instructions. The context supplies the subject:

```text
context + workflow revision -> final agent prompt
```

Without `--workflow`, Factory uses a blank workflow and sends the context as the
task description.

The CLI accepts exactly one context source:

- positional text;
- `--file PATH`;
- non-terminal stdin when neither of the above is present.

Empty input or more than one source is a usage error. `--title` is optional. By
default, the first non-empty context line is shortened to 80 Unicode characters
for display without changing the stored context.

## Resolution and idempotency

Workers and repositories accept a stable ID or a unique friendly name.
Workflows accept a stable ID or a unique title. Ambiguity fails before mutation
and prints candidates.

Selection order is:

1. explicit flag;
2. configured default;
3. the server's only valid choice;
4. otherwise fail closed.

Every task submission has a request key. The CLI generates a UUID unless the
operator supplies `--request-key`. Retries after uncertain network failures use
the same key. An explicit key is looked up before resolving mutable names or
titles, so replaying an old command returns the original task.

## Waiting and output

Default `factory run` prints the task ID, state, worker, repository, and UI URL,
then exits.

`factory run --wait` polls every two seconds until terminal:

- success exits 0;
- failed, lost, or cancelled work exits 1;
- usage errors exit 2;
- Ctrl+C exits 130 and does not cancel the task.

Global `--json` makes stdout one JSON value. Diagnostics remain on stderr. List
commands are paginated and print the next cursor rather than reading unbounded
history.

Destructive commands are non-interactive and require `--confirm`.

## Configuration

Factory keeps one home at `~/.factory`. Proposed client config at
`~/.factory/client.toml`:

```toml
server = "http://127.0.0.1:7337"
default_worker = "local-codex"
default_repository = "factory"
```

`FACTORY_HOME` may select another root when the unified CLI ships.
`FACTORY_CLIENT_CONFIG` selects another client config. `FACTORY_SERVER`
overrides the client endpoint. The server's bootstrap-only
`~/.factory/config.toml` and `FACTORY_SERVER_CONFIG` are a separate schema and
selector; finite client commands never decode them.

Current server and worker compatibility remains:

- `FACTORY_DATA_HOME` selects the state root;
- `FACTORY_WORKER_CONFIG` selects a worker config.

The unified CLI must open current databases and reuse current worker identities
without copying or resetting them.

Finite commands accept only plain HTTP loopback URLs with an explicit nonzero
port and root path. Credentials, query strings, fragments, HTTPS, and
non-loopback hosts are rejected.

## Workflow files

```toml
title = "code-review"
summary = "Review a merge request and report actionable findings."
instructions_file = "./prompts/code-review.md"
enabled = true
```

Applying unchanged content is a no-op. Changed instructions create an immutable
revision. Every task pins the exact revision used.

## Invariants

1. All work creation uses one control-plane task service.
2. One worker process has one identity and one runtime.
3. Finite commands never access SQLite or worker directories.
4. `run` never starts an agent process.
5. Every task pins its workflow revision.
6. Retrying preserves the original context and workflow revision.
7. Normal startup never invokes Node or rebuilds UI assets.
8. Human output and JSON output never share stdout.
9. Windows is unsupported.
10. The final release publishes one `factory` operator binary.

## Implementation

Use a small root dispatcher and `flag.FlagSet` for each command. A CLI framework
is not needed yet.

Suggested order:

1. extract current server and worker `run` functions into command packages;
2. build the root parser, configuration, output, and HTTP client;
3. add `server`, `worker`, `start`, `version`, and `status`;
4. add `run`, task inspection, and worker inspection;
5. switch scripts and releases to the unified binary;
6. add Workflow commands, leaving typed Automation commands to their own CLI
   revision.

During the transition, the internal `factory-server` and `factory-worker`
packages can remain testable build targets. Releases should publish only
`factory`.

## Acceptance

- one Go build produces the documented operator binary;
- `factory start` runs the UI plus Codex and Claude workers from separate
  configs;
- current state and worker identities open without migration;
- startup detects unhealthy or stale workers and stops all children on failure;
- manual tasks work from text, a file, and stdin;
- ambiguous names cause no mutation;
- request-key replay is idempotent across uncertain submission;
- `run --wait` returns documented exit codes;
- task and worker commands show the same durable state as the UI;
- startup and operator builds require no Node installation;
- Workflow commands extend the command tree without changing the Task contract.

## Out of scope

- OIDC, user accounts, roles, and hosted multi-tenancy;
- Windows;
- WebSockets or server-sent events;
- a prompt template language or workflow step graph;
- runtime plugin interfaces;
- interactive config editing;
- installing agent runtimes or credentials;
- distributed scheduling or active-active control planes.
