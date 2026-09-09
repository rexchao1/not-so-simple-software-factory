# Worker contract

A Factory Worker is one stable identity, a set of coding-agent capabilities,
and a pool of independent agent sessions. Its capacity limit controls the number of sessions that may
prepare or run at once. It can run on a developer machine, VM, or Unix
container. Workers are cattle: the control plane owns repository scope, and an
eligible worker acquires an assigned repository into its bounded local cache.
Windows is not supported.

## Configuration

```toml
server = "http://127.0.0.1:7337"
name = "local-codex"
runtime = "codex"
runtimes = ["pi", "codex", "claude-code"]
# Optional. Defaults to 10.
max_concurrent = 10

[labels]
environment = "development"
host = "laptop"
```

`runtimes` lists the coding agents the Worker should discover. Supported values
are `pi`, `codex`, and `claude-code`. `runtime` remains the primary capability
and preserves compatibility with existing single-runtime configurations. When
`runtime` is omitted it defaults to the first entry in `runtimes`, or to `codex`
when neither field is set. Each task selects one ready capability and launches a
fresh runtime process with its own worktree, manifest, lease, and supervisor
process group. `max_concurrent` accepts values from 1 through 100;
preparing attempts consume slots as well as running attempts.

Factory migrates existing SQLite databases to the expanded worker capacity
range when the control plane starts.

When `data_directory` is omitted, Factory derives an absolute path beside the
configuration as `workers/<config filename without .toml>`. For example,
`~/.factory/worker.toml` uses `~/.factory/workers/worker`, while
`~/.factory/claude-worker.toml` uses `~/.factory/workers/claude-worker`. The
config filename, not the mutable worker display name, selects durable local
state, so use a different config filename for each worker on one host.

Set `data_directory` to override the default. Explicit relative paths resolve
from the directory containing the worker TOML file; explicit absolute paths are
used as written. The worker probes `gh auth status --hostname github.com`
automatically. A successful probe advertises GitHub source access and the
ability to acquire centrally managed GitHub repositories. It contains no token.

The legacy `[repositories.<key>]` map remains optional for manual delegation to
an existing non-bare checkout. Its paths resolve relative to the worker TOML.
Each repository must have an `origin`. `base_branch` is an optional short branch
name such as `main`, `develop`, or `release/2026.07`; when omitted, Factory uses
the branch advertised as the origin default.
Newly discovered legacy checkouts enter the catalog disabled and support only
explicit assignment until an operator adds or enables them centrally. Reposting
a centrally managed repository does not override an explicit disable.
Repositories already known before this migration remain enabled for routing
compatibility.

The worker pins the normalized `origin` identity at startup and revalidates it
before and after resolving each attempt base. An intentional origin change
requires a worker restart; an unexpected change fails before agent launch.

## Identity and registration

The first start creates a protected `worker-id` file in the worker data
directory. The worker reuses that ID on every restart. The local API does not
authenticate workers, so the ID is identity, not a credential.

Registration advertises:

- display name, primary runtime, and capability status for each configured tool
  and coding agent;
- maximum concurrent attempts;
- optional host labels;
- worker version;
- optional legacy repository keys, normalized remote identities, and retained
  counts;
- successfully probed source access such as GitHub;
- whether the worker can acquire managed repositories;
- the bounded set of cached control-plane repository IDs, used for exact cache
  headroom accounting. The public Worker view exposes only the count.

The server returns repository IDs used for task assignment. Heartbeats refresh
health and current capacity. A worker is offline when its heartbeat expires.

Print the configured identity without starting the worker:

```sh
~/.factory/bin/factory-worker identity \
  -config ~/.factory/worker.toml
```

## Claiming

Workers poll the loopback API for compatible work. An idle worker makes at most
one empty claim request per polling interval. Each successful claim immediately
starts another claim while a slot remains, so queued work fills the pool without
waiting one polling interval per slot. A claim succeeds only when:

- the Session is assigned to that worker;
- the repository assignment is frozen to that worker;
- worker capacity is available;
- the repository has fewer than ten retained worktrees, active attempts, and
  terminal attempts awaiting a local disposition on that worker.

An attempt has a lease. The worker renews it while the agent process is alive.
If the worker disappears, the control plane marks the attempt lost after the
lease expires.

## Attempt lifecycle

The worker prepares a task as follows:

1. validate the repository ID and canonical `github.com/owner/repository`
   identity, or validate a compatible legacy checkout;
2. clone a missing managed repository with `gh repo clone` or use its existing
   cache;
3. discover the origin default branch or use a legacy repository's configured
   `base_branch`, fetch it, and freeze its exact commit;
4. create an owned worktree below its data directory;
5. create a `factory/<task-prefix>-<attempt-prefix>` branch;
6. write a protected, durable attempt manifest;
7. launch the selected runtime in the worktree;
8. stream bounded lifecycle and output events;
9. report the result and outcome;
10. inspect final Git state locally to decide whether cleanup is safe.

The legacy checkout and managed cache are never switched to the base branch,
and agent work never runs in either. A worktree isolates Git state but is not a
security sandbox: the runtime still has the worker process's filesystem,
network, and credential access. A later sandbox can contain this same prepared
workspace without changing the task contract.

Codex is launched non-interactively with structured result output. Claude Code
is launched non-interactively with JSON output. The worker normalizes both into
the same control-plane result contract.

Runtime output and API event payloads are bounded. Oversized output is truncated
or summarized so one agent cannot grow a request without limit.

Managed caches live at `DATA_DIRECTORY/repositories/REPOSITORY_ID`. Preparation
is coordinated per repository: clone installation, origin and base resolution,
fetch, worktree add and remove, and managed branch cleanup serialize only with
other Git metadata operations for that same repository. Unrelated repositories
prepare concurrently. The repository lock is released before Codex or Claude
Code starts, so sessions using distinct worktrees from one repository can run
in parallel. Clone and fetch have a five-minute bound.

A short cache-accounting lock reserves capacity before a first-time clone
starts. This keeps the worker at no more than 100 managed repository entries
even when unrelated clones overlap. Failed or cancelled clones release their
reservation. This version does not evict caches automatically.
Interrupted `.clone-*` directories are removed during startup after the worker
has locked its data directory, so hard crashes cannot bypass the cache bound.
On the next registration, the control plane releases an uncached dynamic
reservation once no queued or active execution and no retained worktree uses it.

## Cancellation and shutdown

Cancellation is cooperative but enforced:

1. the control plane sets the execution's cancellation request flag;
2. the worker observes the state while renewing the lease;
3. it terminates the runtime process group;
4. it reports the attempt as cancelled.

On SIGINT or SIGTERM the worker stops claiming, terminates active runtimes,
reports their outcomes when possible, and exits.

## Worktree cleanup

Successful clean work can be removed only after the worker proves it is safe.
The worker validates:

- the protected manifest and its recorded worker identity;
- exact worker, task, attempt, repository, path, and branch identity;
- that the path is a direct child of its worktree root;
- the matching Git worktree registration;
- a clean working tree;
- publication of every new commit before automatic cleanup.

Dirty work, uncommitted changes, unpushed commits, and uncertain publication are
retained. The Worker view reports the path, reason, and cleanup command. Its
cleanup preview reports the branch and Git status.

Preview manual cleanup without changing the worktree:

```sh
factory-worker cleanup ATTEMPT_ID --config ~/.factory/worker.toml
```

If the preview is correct, confirm removal:

```sh
factory-worker cleanup ATTEMPT_ID \
  --config ~/.factory/worker.toml \
  --confirm
```

Confirmed operator cleanup preserves the local branch but removes the worktree
with force, so uncommitted changes shown in the preview are lost. Cleanup never
deletes the repository cache, a legacy configured checkout, or another worker's
path.

## Deployment model

The default control plane accepts loopback Workers only. On one host, run the
server and one or more Workers as supervised Unix services.

An optional, separate HTTPS listener accepts authenticated Workers on remote
VMs. A ten-minute, one-time enrollment creates a per-Worker credential, while
the browser and operator API remain on loopback. See the [remote VM
guide](remote-workers.md).

Kubernetes Worker provisioning is not part of the current product.
