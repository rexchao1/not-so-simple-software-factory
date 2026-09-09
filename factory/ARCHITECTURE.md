# Factory architecture

> **Status:** Current implementation
>
> **Verification basis:** implementation and tests in this repository

## Executive summary

Factory is a local-first control plane for repeatable software-engineering
agents. An operator saves ordered agent prompts as a Pipeline and selects it for
a Task, uses `factory build` to admit up to 100 existing work-item references,
or runs one saved Procedure across up to 100 enabled managed repositories. Each
target becomes independent Session-backed Work. A persistent Worker claims the
Work, prepares one isolated Git worktree, and starts a fresh Pi, Codex, or
Claude Code process for each Pipeline stage in order. The stages share that
worktree and branch, stream events, and accept scoped progress or semantic
outcome updates. An agent can checkpoint clean committed work in the final
stage, stop with `needs-input`, and resume from that exact commit after an
operator answer.

The implementation has four main parts:

- `factory` is the operator CLI. Long-running commands replace themselves with
  the compatible server or Worker executable, while finite commands read the
  loopback HTTP API and never open SQLite or Worker directories.
- `factory-server` owns durable state, scheduling, routing, the HTTP API, and
  the embedded browser UI.
- `factory-worker` owns runtime health, repository caches, worktrees, agent
  processes, and cleanup or retention.
- SQLite stores Pipelines, Tasks, Runs, Session-backed Work, durable stages and
  Work updates, executions, Attempts, events, Workers, and repositories.

The operator API is loopback-only. Workers make outbound polling requests to
the server. Remote VM Workers use a separate TLS listener and per-Worker bearer
credential. No server connection into a Worker host is required.

The Cloud Run backend contract is implemented behind immutable execution
profiles and a deterministic fake provider. Real Google Cloud dispatch,
artifacts, and credentials are not implemented yet. The contract keeps Factory
as the source of truth and preserves the persistent Worker path as the built-in
`persistent-auto` default.

### System architecture

```text
Operator browser or `factory` CLI
      |
      | loopback HTTP and JSON
      v
factory-server
  |-- Task scheduler, Build admission, and Procedure fleet admission
  |-- routing and lease state machine
  |-- embedded React UI
  `-- SQLite
      ^
      | register, claim, heartbeat, events, complete
      | local HTTP or separate authenticated TLS
      |
factory-worker
  |-- stable identity and N slots
  |-- runtime capability probes
  |-- bounded repository cache
  |-- isolated worktrees and manifests
  `-- Pi, Codex, or Claude Code
```

The control plane decides what should run and records what happened. A Worker
decides how to execute one claim safely on its machine. The agent runtime is a
child process and does not receive a control-plane operator credential.

### Dependency hierarchy

```text
cmd/factory         -> internal/factorycli   -> internal/protocol
cmd/factory-server  -> internal/controlplane -> internal/protocol
                    -> web
cmd/factory-worker  -> internal/worker       -> internal/protocol
commands and browser -> HTTP API -> control-plane store -> SQLite
factory-worker       -> typed protocol -> control-plane store
factory-worker       -> runtime adapters -> agent child processes
```

Entry points depend on their runtime package, and both long-running runtime
packages share only protocol types. Product and Worker code depend on
`internal/protocol`; Worker code must never import `internal/controlplane`.
Commands and the browser use the HTTP API, and only the control plane writes
lifecycle state to SQLite. Work is user-facing lifecycle truth. Execution and
Attempt remain process and lease truth. Finite CLI commands cannot read
control-plane or Worker state directly.

## Current product model

### Pipeline and Task

A Pipeline is a reusable, versioned sequence of one to twenty stages. Agent
stages have a prompt plus optional model and effort. Code stages run a fixed
command without a model. A final delivery stage performs the fixed push,
pull-request creation, and outcome report without a model. Prompt interpolation
is limited to Task identity and input, Run identity, repository identity, and
the managed branch.
The built-in `Single agent` Pipeline contains one `Do the task` stage whose
prompt is `{{ task.prompt }}`. Updating a Pipeline replaces its stages and
increments its generation. Existing Runs keep their frozen Pipeline snapshot.
A built-in Pipeline cannot be deleted, and a custom Pipeline must be removed
from every Task before deletion.

An agent stage may also be read only. A read-only stage runs the agent with
every writing tool denied, which is how Factory guarantees a pass reads a
repository and changes nothing without asking the prompt to promise it. The
flag is frozen onto the Run like kind, model, and effort, so a Pipeline edited
mid Run cannot loosen a stage that is already executing. Three rules go with
it. Only an agent stage can carry it, because a code stage runs an operator's
own command and a delivery stage runs Factory's git operations, and neither is
narrowed by a tool list. Only the `claude-code` runtime can enforce it, so a
Run that would freeze a read-only stage onto any other runtime is refused at
admission with `read_only_runtime_unsupported`. And a read-only stage gets a
prompt wrapper with no reporting contract on it: it has no changes to list, ran
no commands to verify, and can open no pull request, so the result it returns
is exactly what the agent printed.

The built-in `Critique` Pipeline is one read-only agent stage named `Critique`
whose prompt is the checkpoint-critic skill body followed by `{{ task.prompt }}`.
It has no delivery stage, since a critique commits nothing, and Work admitted
to it is stamped `process_exit` so that the Work result is the findings the
critic printed. Factory stores that result whole and never parses it. Unlike
the other built-ins, which a migration wrote once, the Critique Pipeline is
installed or repaired on every server start, because its prompt is a copy of a
skill that keeps being edited and its model comes from the light factory's
model table. A repair increments the generation and leaves every admitted Run
on its own frozen snapshot.

A Task is the stored form of a saved Procedure. It contains:

- name and input prompt;
- selected Pipeline;
- runtime: `pi`, `codex`, or `claude-code`;
- timeout and per-Run concurrency limit;
- one or more managed repositories;
- optional cron schedule and IANA timezone;
- mutable generation and archived state;
- an outcome contract: `process_exit` or `agent_update`.

A Task may also save an execution-profile ID. Missing profile data means
`persistent-auto`, so existing rows need no migration. Manual Run requests may
override the saved profile; scheduled Runs use the saved default.

Existing and newly created Tasks default to `process_exit`. An explicit
conversion to `agent_update` increments the generation and requires the
persistent backend. Updates use an expected generation. Admission snapshots
the Task, Pipeline, and outcome contract so later edits do not change existing
Runs. A
manual run uses an idempotency key.
Scheduled admission polls every ten seconds and preserves the frozen pending
snapshot while retrying a failed admission.

### Run and Session

One Task admission creates one Run and one Session-backed Work record per
selected repository. Work is the operator-facing unit and the unit the Work
board lists; Run is the parent grouping record. `sessions` remains the table
name and `protocol.Work` is an alias of `protocol.Session`, so the older
`/runs/{run_id}/sessions/{session_id}` routes keep working for the CLI. A Run stores the Task and Pipeline snapshot, immutable execution
profile version, backend, runtime, provider, model, timeout, resource class,
commit-resolution policy, outcome contract, assurance (`reviewed` or `fast`),
ordered target snapshot, source (`manual` or `schedule`), schedule time, and
aggregate state. Work stores the
same frozen execution choice with target identity, source reference, context,
stable publish branch, repository identity, ownership, waiting reason,
progress, checkpoint, pending resume, pull-request evidence, predecessor,
answer, result, and terminal fields. Each Session owns ordered durable stage records
with the rendered prompt, resolved model and effort, runtime-reported token usage,
state, result, error, and timestamps.

Work can represent `queued`, `running`, `needs-input`, `ready`, `succeeded`,
`failed`, `no-change`, and `cancelled`. The backing Session table also retains
the compatibility routing states `blocked` and `preparing`. Run state and
counts are derived from Work as `blocked`, `queued`, `running`, `succeeded`,
`failed`, `partial`, or `cancelled`.

Work updates store typed status, actor, request, Attempt, sequence, message,
checkpoint, and pull-request fields. Storage allows at most 199 progress
updates and reserves one outcome update per Attempt. Progress messages are at
most 2 KiB and outcome messages are at most 8 KiB.

Trusted answers are stored separately from agent updates and linked to the
question update they answer. The current answer is also projected on Work.
The answer carries the actor that gave it, `operator` unless the request names
one, and the Work projects it as `answered_by`.
Continuation prompt assembly keeps the frozen Procedure and original context,
question, answer, checkpoint, branch, and pull-request evidence. It fills the
remaining 72 KiB prompt budget with trusted prior answers, then recent outcomes
and progress. Omitted history is counted and identified by a SHA-256 digest
without deleting stored records.

A Session starts blocked when no eligible Worker can currently accept it. A
later claim can route it when a healthy Worker advertises the runtime and
repository access. Procedure concurrency limits how many sibling Sessions may
be queued or active at once. Claim ordering uses each Run's prior Attempt count,
then admission order, so a large Run yields to an older compatible Run that has
received less service.

### Build admission

`factory build` accepts 1 to 100 ordered HTTPS GitHub issue URLs or opaque
references. Opaque references require `--repo`. An explicit repository must
also match every GitHub URL, while an omitted repository permits a GitHub-only
batch across managed repositories. The server resolves repository state and
rejects the whole batch before writing when any target is invalid.

Every Build freezes generation 1 of the built-in `standard-build` Procedure,
the current built-in Pipeline, the configured default or explicit runtime,
persistent execution settings, and one immutable target and publish branch per
Work. Every Work stores its rendered stages. The Procedure text is trusted
policy. References are labelled as untrusted context and are not fetched by the
CLI or control plane.

The caller fingerprint covers ordered normalized references, explicit option
presence and values, and the rebuild flag. Request-key lookup and fingerprint
comparison happen before repository, runtime-default, duplicate, or predecessor
reads. Matching replay returns the original Run. Active matching Work blocks a
duplicate. A rebuild requires a new key and the latest terminal predecessor for
every target, selected in the same transaction.

### Procedure fleet admission

`factory procedures` reads saved Procedures, including archived entries, from
the bounded local API. `factory run PROCEDURE --repos ...|all` selects explicit
enabled managed repositories in command order or freezes all enabled managed
repositories in repository-identity order. The server snapshots the current
Procedure generation, Pipeline generation and stages, prompt, runtime, timeout,
concurrency, outcome contract, execution choice, and ordered targets in one
transaction. Every repository Work stores its independently rendered stages.

The caller fingerprint covers the normalized Procedure name, ordered repository
selectors or `all`, and the rebuild flag. Request-key replay happens before
Procedure, repository, execution-profile, or predecessor reads. A rebuild uses
a new key and records the latest terminal predecessor for the exact Procedure
and repository. Existing scheduled Tasks continue to use their saved repository
selection and schedule snapshot.

### Execution and Attempt

An Execution is the durable assignment of one Session to one Worker and runtime.
An Attempt is one leased try of that Execution. An explicit retry of a failed or
cancelled Session requeues the same Execution and Work, increments retry
history, and warns that external effects may repeat only when a process already
started. Retry rejects replaced Work and matching nonterminal Work in the same
transaction.

An Attempt begins in `preparing`, moves to `running` after the Worker reports
its supervisor identity, then ends as `succeeded`, `failed`, `cancelled`, or
`lost`. It owns ordered bounded events, a bounded result or error, process
identity, and a 30-second lease, and, for a Claude Code attempt, the estimated dollar cost, token usage, and per-model breakdown the runtime reported, summed over its stages, with each stage keeping its own.

### Worker and repository

A Worker has one durable ID, display name, labels, capacity, health, runtime
capabilities, source access, repository advertisements, and retained-worktree
inventory. One Worker can advertise several runtimes and run 1 to 100 Attempts,
with ten slots by default.

Each fake cloud profile projects into one stable synthetic Worker named
`cloud-run-<profile-id>`. The control plane creates Attempts internally for
that Worker. Synthetic Workers cannot enroll, register, heartbeat, poll claims,
or hold a remote Worker credential.

The control plane owns a catalog of managed GitHub repositories. Eligible
Workers clone them on demand with `gh`, keep at most 100 cache entries, fetch
before an Attempt, and resolve the current base branch and commit. Legacy
static repository paths remain readable through Worker configuration.

### Planning

Planning is the light factory's half of the product, and it is state the
control plane stores rather than work it executes. A project holds a boulder
statement and a route. A checkpoint is one rung of that route and the unit that
carries a written PRD, a status, and the answers a human gave at review. A
frozen checkpoint is split into stones, which group, and pebbles, which
become factory Work. The two words are different sizes and never the same
thing: a boulder is the whole idea, one per project, and a stone is one chunk
inside one checkpoint.

This state used to be markdown and JSON files in a separate orchestrator
repository, which the cockpit read through a configured `roadmap_root` and
nothing wrote. Two machines editing those files is what broke, so migration 46
moved the state into `planning_projects`, `planning_checkpoints`,
`planning_boulders`, and `planning_pebbles`, and `/api/v1/planning` became the
only way to write it. Migration 48 renamed `planning_boulders` to
`planning_stones` and both `boulder_id` columns to `stone_id`. `roadmap_root`
and its parsers are gone.

The store owns three rules and no caller can route around them. A frozen or
built checkpoint's PRD never changes again, because the pebbles cut from it
cite that text; only its answers can still be saved. Status moves along one
path, `planned` to `review`, `review` to `fog` or `frozen`, `fog` to `frozen`,
`frozen` to `built`, and every other move is refused. Pebbles exist only under
a frozen checkpoint.

A critique pass is the one place planning reaches the Work side. `POST
/api/v1/work` accepts an optional planning triple of project, checkpoint
number, and round. The triple is all or nothing, its project is validated and
folded exactly as the planning API validates a project id, and it is accepted
only on the built-in Critique Pipeline. Admission stores it on every session of
the Run, which is what lets the roadmap ask what passes a checkpoint has had
without guessing from a task name. The admission response shape is unchanged,
and a replay through the request key returns the original Work rather than
running a second critique on the same round.

`GET /api/v1/roadmap` reads those tables and is what the Planning page
consumes. Its response shape did not change when its source did. It reports
`configured` true whenever the store is open, since there is no directory left
to point at, and it derives its waiting list on every read rather than storing
one: a checkpoint in `review` with no answers saved, or one in `fog`. Pebbles
are joined to the factory's own Work rows by recorded Work id where one exists
and by title otherwise, so the page says what was built and not only what was
planned.

Passes come from the sessions carrying a planning triple. Each pass reports its
round, the model from the frozen execution snapshot, the cost summed over its
attempts, the milliseconds from start to terminal, the Work state as its
outcome, so a critique that failed reads as a failed pass rather than vanishing,
and the id of the Work it ran as, so the Planning page can send an operator to
the findings themselves rather than only naming a round. The live pass is the
submitted critique that has not come back: queued, preparing, or running, and it
carries the same Work id. A checkpoint rolls its passes up into a cost and a count
of distinct rounds, and a project rolls those up again, which is what the
waiting list carries beside each entry. Rounds are counted distinctly, so a
round re-submitted after a failure is one round that cost twice rather than two
rounds of review.

## Architectural invariants

1. SQLite and the control plane are the authority for Run and Attempt state.
2. A claim is assigned only to its selected, healthy, online Worker with a ready
   runtime, free capacity, and repository availability.
3. A random lease token owns one active Attempt. The server stores its digest,
   not the token. Active mutations require the matching unexpired lease.
4. Claim request IDs and terminal completion are idempotent. Replays cannot
   create two Attempts or replace a stored terminal outcome.
5. Every runtime starts in a Worker-owned worktree. The supervisor owns its
   process group and enforces cancellation, timeout, lease loss, and parent
   loss.
6. Cleanup fails closed unless repository, manifest, path, branch, process, and
   worktree identity can all be proved.
7. Dirty, failed, cancelled, lost, unpublished, or uncertain worktrees are
   retained for inspection. Clean unchanged or proved-published work may be
   removed.
8. Plain HTTP accepts loopback clients only. Remote Workers require TLS,
   one-time enrollment bound to a stable Worker ID, and a stored bearer
   credential.
9. Task admission snapshots prompt, runtime, ordered targets, timeout,
   concurrency, generation, outcome contract, execution choice, and schedule
   context.
10. Operator builds embed committed `web/dist` assets and do not require Node.js
    at runtime.
11. Finite `factory` commands accept only an explicit-port plain HTTP loopback
    endpoint and read current state through bounded API routes.
12. Claim protocol version 5 gates every persistent Worker claim. Older Workers
    receive `worker_upgrade_required`, including for process-exit Work.
13. Build and Procedure fleet admission are all-or-none. Exact request-key
    replay wins before mutable configuration reads, and one Run cannot contain
    a duplicate target.
14. An implicit admission key is written durably before HTTP submission and is
    not removed until an authoritative admitted, replayed, or pre-commit
    rejection result has been written and flushed.
15. A pending resume SHA is authoritative until the supervisor starts the
    runtime and the Worker reports that it started from that exact commit.
    Answer, cancellation, failed preparation, and retry do not clear it or fall
    back to another ref.
16. Agent-owned `needs-input` requires a clean worktree and a checkpoint
    revalidated after process stop. Changed Work must match the immutable
    publish ref. Operator-owned Work cannot create this outcome.
17. Exact Work replacement creates one new one-Work Run from the named terminal
    predecessor. First admission checks current eligibility, while exact
    request-key replay returns the stored replacement before mutable reads.
18. Pause is a switch and a timestamp, with no reason text. A paused Factory
    admits no new Work and dispatches none. Every admission
    and dispatch path reads the flag inside the transaction it is about to
    write in, and always after its own request-key replay lookup, so a
    concurrent pause cannot be overtaken by the insert and a client retry still
    receives its original result. Attempts already running are untouched, and a
    Worker claiming while paused is told there is no Work rather than handed an
    error. Resume wakes the control plane's loops rather than waiting out a
    poll interval.
19. Factory reports verification it can vouch for and labels the rest. A code
    stage's exit status is Factory's own evidence. An agent's report is a claim,
    parsed conservatively from a contracted block and marked agent-reported,
    and a result that does not follow the contract yields nothing rather than a
    guess. Counts are of checks, never of test cases, which Factory cannot see.
20. An absent cost is never rendered as a zero. Only a runtime that reports cost
    produces a figure; every other Work and stage reads as unavailable, and any
    total that omits unreported items says so. Overview cost is reported over
    every Work item ever run, with a trailing window beside it for the rate: a
    day-scoped total resets before an operator has necessarily read it.
21. A read-only stage cannot write. Only an agent stage carries the flag, only
    `claude-code` can enforce it, and a Run that would freeze one onto another
    runtime is refused at admission rather than executed with the flag dropped.
    The Worker denies the writing tools through a runtime argument, so the
    guarantee holds regardless of what the prompt or the agent decides.

## Components

### Operator CLI

`cmd/factory` delegates parsing and finite HTTP work to `internal/factorycli`.
The `build`, `run`, `procedures`, `status`, `show`, and `workers` commands use
typed protocol resources and write either stable tabular output or one JSON
value. They do not import SQLite or Worker packages. Build and Procedure Run
syntax normalization is local, but managed-state resolution belongs to the
server. An injected `factory update` command instead connects only to a private
Attempt-scoped Unix socket using the Work ID, Attempt ID, and update token
supplied by the Worker.

When no Build or Procedure Run key is supplied, the CLI journals a random key
under the private operator data directory before sending. A nonblocking OS lock
scopes concurrent submissions by endpoint and caller fingerprint. Lost
responses retain the key for replay by a later CLI process. The journal holds
at most 100 uncertain requests and never evicts one silently.

The `server start` and `worker start` commands replace the CLI process with the
matching compatibility executable beside it or on `PATH`. An explicit config
path is passed through the existing `FACTORY_SERVER_CONFIG` or
`FACTORY_WORKER_CONFIG` environment contract. Process replacement preserves the
existing role's signal and shutdown behavior.

### Control plane

`cmd/factory-server` loads optional bootstrap TOML, opens SQLite, applies
embedded migrations, sweeps expired leases, starts the Task scheduler,
serves the local API and UI, and optionally starts the remote Worker TLS
listener. Shutdown stops schedulers first and gives HTTP servers ten seconds.

`internal/controlplane` owns validation, transactions, Run admission, routing,
claiming, leases, event ingestion, completion, cancellation, retry, pagination,
overview aggregates, Worker authentication, and backup or restore validation.

SQLite uses foreign keys, WAL journaling, a five-second busy timeout, and a
bounded connection pool. The default database is
`~/.factory/server/factory.sqlite3`.

The backup path validates a live database and uses `VACUUM INTO` to publish a
mode-`0600` standalone snapshot without replacement. Restore validates a marked
snapshot, rejects SQLite sidecars, applies supported migrations in a private
staging directory, and publishes only a complete destination.

### Worker

`cmd/factory-worker` loads TOML configuration and starts one manager. The
manager:

- creates or loads its stable identity and local credential;
- probes Git, `gh`, and configured runtime readiness;
- registers every ten seconds and polls for claims about every two seconds;
- acquires managed repositories into a bounded local cache;
- renews active leases every ten seconds;
- starts up to the configured number of isolated sessions;
- reconciles manifests, worktrees, and owned process groups after restart.

Only a frozen `agent_update` Attempt receives a private update socket and
token. The Worker validates that capability locally, resolves ready pull
request evidence with GitHub and Git, validates clean durable needs-input
checkpoints, and forwards a typed update under its own lease. The agent-facing
request never contains an operator credential, Worker credential, or Attempt
lease token. Outcome reports ask the supervisor to stop the process group, then
the Worker completes the Attempt only after verified process stop and the
required delivery or checkpoint postflight check.

The supervisor is a subprocess of `factory-worker`. It anchors ownership of the
runtime process group. Unix process-group behavior is required, so Windows
Workers are unsupported.

### Agent runtimes

The Worker launches each runtime non-interactively in the prepared checkout:

- Pi uses `--print --no-session` and captures the final plain-text result.
- Codex uses `codex exec` with JSON events and a last-message file.
- Claude Code uses `claude --print` with streaming JSON.

Runtime output is normalized into the same Attempt event and completion
contract. Event batches are at most 100 events and 256 KiB; each event is at
most 64 KiB; one Attempt stores at most 10 MiB of events. Results are at most
256 KiB and errors at most 64 KiB.
A Claude Code result event's `total_cost_usd`, `usage`, and `modelUsage` travel with the stage and attempt completions and are stored as `cost_usd`, `usage`, and `models`, where `usage` counts the top-level loop only and `models` includes subagent requests; Codex and Pi attempts carry none of them.

### Browser UI

`web/src` is a React and TypeScript single-page application. It exposes Work,
Planning, Tasks, Pipelines, Overview, Workers, and Repositories, with detail
views for each operational resource. Planning reads `GET /api/v1/roadmap` and
is read-only; the light factory writes that state through `/api/v1/planning`. Work presents one card per Work record, as a board or a
table, and polls the same-origin API.

The board unit is Work, not Run. One admission across three repositories is
three Work records with three lifecycles, so a Run card in a repository tab
would describe work in two repositories the operator did not ask about. A Run
remains the parent grouping record, reached from any of its Work records at
`/runs/<run-id>`; a Work record is at `/work/<work-id>`.

Work detail is four tabs. Brief opens the page with the orchestrator brief, if
one exists, and the operational facts; Factory never manufactures a brief for
Work admitted without one. Stages draws the frozen pipeline as nodes with the
bounded evidence each stage handed the next. Outcome carries the normalised
verification and cost. Evidence holds raw stage results, prompts, agent
updates, and runtime event streams, collapsed by default. Opening a Work record
never begins with a wall of logs, and no raw evidence is discarded to achieve
that.

`web/dist` is generated, committed, and embedded by `web/embed.go`. The server
uses an SPA fallback, immutable caching for versioned assets, and restrictive
security headers. Node.js is needed only when UI source changes.

## Critical flows

### Task admission

1. The operator runs a Task with a request key, or the scheduler claims a
   due occurrence.
2. The server freezes the Task generation, Pipeline generation and stages,
   outcome contract, execution choice, and ordered target list.
3. One Run and one Session-backed Work record per repository are inserted
   transactionally.
4. Routing selects compatible Workers where possible. Unroutable Sessions stay
   blocked with a reason.
5. The same manual request key or scheduled occurrence cannot admit duplicate
   Runs.

### Build admission

1. The CLI normalizes reference syntax and computes the caller fingerprint.
2. For an implicit key, it acquires the endpoint/fingerprint lock and durably
   journals a random request key before sending one HTTP request.
3. The server checks the key and fingerprint before mutable reads. Exact replay
   returns the stored Run and different input conflicts.
4. For a new key, one transaction resolves every enabled managed repository,
   rejects duplicates, applies active-Work and rebuild guards, and freezes the
   standard Procedure, current built-in Pipeline, and runtime.
5. The transaction inserts one Run and ordered independent Work targets. Work
   routes immediately when possible or remains visibly blocked for a later
   scheduler claim. The CLI never starts agents.
6. The CLI clears an implicit journal entry only after it flushes an admitted,
   replayed, or typed pre-commit rejection result. Transport, server, malformed
   response, timeout, interruption, and output errors leave the key pending.

### Procedure fleet admission

1. The CLI normalizes the Procedure name and ordered repository selectors, or
   the `all` selector, then computes a caller fingerprint.
2. Explicit and generated request keys use the same lock, durable journal,
   replay, typed rejection, output flush, and cleanup contract as Build.
3. The server checks the key and fingerprint before any mutable read. Exact
   replay returns the frozen Run even after Procedure or repository changes.
4. For a new key, one transaction loads the active Procedure, resolves the
   explicit enabled repositories or complete enabled set, freezes the selected
   Pipeline, validates the frozen execution backend, and selects exact
   Procedure-and-repository predecessors for a rebuild.
5. The transaction inserts one Run and one ordered repository Work target per
   selection. Each target keeps independent scheduling, Attempts, cancellation,
   retries, updates, and outcomes.

### Claim and execution

1. A healthy Worker registers capabilities and polls with a fresh claim request
   ID and lease token.
2. In one transaction, the server may materialize a blocked route or reroute a
   queued Session, checks capacity and Procedure concurrency, applies run-aware
   fair ordering, creates an Attempt, and moves Execution and Session to
   `preparing`.
3. The Worker acquires or refreshes the repository. Preparation uses the
   pending resume SHA first, then an existing immutable publish ref, then the
   repository base when no pull request or resume checkpoint exists. Missing or
   moved authoritative commits fail visibly. It then creates a branch and
   worktree, writes a manifest, and starts the supervisor.
4. The Worker reports process identity and the server moves the lifecycle to
   `running`.
5. Heartbeats extend the lease by 30 seconds and return cancellation state.
6. The Worker starts each frozen stage in order. Agent stages get a fresh model
   process, code stages run their fixed command with no model, and delivery
   stages push and open the pull request with no model. A bounded structured
   result from the immediate predecessor is supplied as evidence to the next
   agent stage. The Worker reports stage start and completion under the Attempt
   lease. A failed or cancelled stage stops the sequence. Later stages cannot
   start before all predecessors succeed.
7. Ordered runtime events are appended idempotently. Attempt success requires
   every stage to succeed, except for the one-stage compatibility path.
8. The Worker removes proved-safe worktrees and reports retained ones back to
   the control plane.

### Agent outcome and question resume

1. The agent calls its Attempt-scoped Unix-socket update endpoint. The Worker
   prompt names the exact immutable publish branch. The Worker verifies token
   and Work identity before any mutable Git or provider checks.
2. Progress is stored without changing Work ownership. `ready` requires
   matching repository, publish branch, local HEAD, remote ref, and pull-request
   head evidence.
3. `needs-input` requires a clean worktree. An unchanged Work checkpoints its
   exact base commit. Changed Work must be committed and match the fetched
   immutable publish ref.
4. An accepted outcome stops the process group. The Worker repeats ready or
   checkpoint validation after verified process stop. Failed postflight makes
   the Attempt and Work fail and retains the worktree.
5. A valid question stores the checkpoint as both historical evidence and the
   pending resume SHA. The Attempt succeeds, Work enters `needs-input`, and no
   process or lease remains alive.
6. An operator answer stores bounded trusted context and requeues the same
   Work when the frozen Run concurrency limit has a slot; otherwise it remains
   blocked for the normal fair materializer. The next claim contains a bounded
   continuation prompt that labels the pending and historical checkpoint SHAs
   separately, renders the agent question as escaped single-line untrusted
   text, and prepares from the pending SHA.
7. The server first validates the prepared commit while moving the Attempt to
   running. The supervisor then reports that the runtime child started, and a
   second leased acknowledgement clears the pending SHA for that exact commit.
   The historical checkpoint remains.

### Cancellation, lease loss, and retry

Queued or blocked Sessions cancel immediately. Active cancellation is stored on
the Session and Execution, returned by the next heartbeat, and enforced by the
supervisor. If lease renewal fails or the 30-second deadline passes, the
supervisor stops the process group and the control plane marks the Attempt
lost. Startup and periodic sweeps recover expired leases after server failure.

Only failed or cancelled Sessions can be retried. Retry preserves the Session
and Attempt history, pending resume SHA, and known pull-request evidence. It
selects a currently eligible Worker and creates the next Attempt when claimed.
Known-PR Work with a missing publish ref fails preparation with the PR, ref,
and trusted recovery SHA. The ref is accepted only when restored at that SHA.
Work with a previously published checkpoint also fails visibly when its publish
ref is missing instead of falling back to the repository base.
`ReplaceWork` is the exact-predecessor recovery path when retry cannot recover
the original Work.

## API and security boundaries

The local listener exposes health plus operator and Worker routes under
`/api/v1`: Builds, Work list/detail/answer/retry/replacement, Workers,
repositories, Pipelines, Tasks, Runs, overview, roadmap, planning, Attempts,
updates, and event history. It rejects non-loopback clients before route
handling.

`GET /api/v1/work` is the cursor-paginated Work list behind the board, filtered
by `repository_id`, `run_id`, and repeated `state` parameters. It orders by
`admitted_at` because that column never moves, while `terminal_at` is
recomputed by lifecycle updates and reset by a retry. Its projection omits
every prompt, command, stage result, and error, each of which can hold
hundreds of kilobytes. `GET /api/v1/work/{work_id}` returns one Work record in
full, and its `work.result` field is what a caller reads a finished agent's
output from: the light factory reads a critique's findings there. `POST
/api/v1/work` still admits Work: the method separates them, and its optional
`planning` object is what makes a submission a critique pass.

`/api/v1/planning` is the only writer of planning state: a project, a
checkpoint, its answers, its status, and its pebbles, each addressed by
`(project, number)` in the path so no request body can disagree with the URL.
Every write validates the way admission does, bounds each text field, and
refuses with a stable code. `docs/light-dark/planning-api.md` is the route
reference. It is loopback operator API like the rest and is not exposed on the
remote Worker listener.

The optional remote listener exposes only health, enrollment exchange, Worker
registration and claims, and the active Attempt lifecycle. Creating an
enrollment remains a local operator action. Enrollment tokens are one-time and
short-lived; exchange installs a per-Worker credential. Attempt routes also
check that the authenticated Worker owns the Attempt.

Factory is a trusted single-operator system. It has no multi-user tenant model.
Agents may execute repository code using credentials already available on the
Worker host. Worktrees isolate Git state, not hostile code. The product must not
describe a Worker as a security sandbox.

## Persistence and migration

Migrations are embedded from `migrations/` and applied in order. Migration 27
introduces the current lifecycle model. Migration 28 adds the single-claim
protocol and rejects incompatible old Workers. Migration 30 renames the
operator model to Tasks, Runs, and Sessions without changing behaviour, and
refuses to apply if the new table names are already in use. Migration 31 adds
the durable Work lifecycle, ordered Run targets, outcome contracts, bounded
Work updates, and claim protocol version 3. It preserves existing rows as
`process_exit`. Migration 32 adds idempotent agent update requests. Migration
33 adds Pipeline templates, Task selection, the built-in single-agent Pipeline,
durable Session stages, and claim protocol version 4. Migration 34 stores
checkpoint publication and trusted answer history and advances the combined
claim protocol to version 5. Supported legacy
Definitions, schedules, repositories, and execution history are converted;
unsupported legacy provider admission is blocked and reported rather than
silently discarded.

Migration 44 adds the orchestrator brief and the durable pause switch, which
is a flag and a timestamp only.
Migration 46 moves planning state into the database. It creates
`planning_projects`, `planning_checkpoints`, `planning_boulders`, and
`planning_pebbles`, with natural composite keys rather than surrogate ids,
because every command and every route already addresses a checkpoint as
`(project, number)`. Checkpoint status is CHECK-constrained to the five values
the cockpit styles, so a writer that skips the store still cannot invent a
sixth. It creates tables and backfills nothing: the files it replaces were
never in this database.

Migration 45 supports the Work board: `sessions.updated_at`, two indexes
covering the list's filter and its `admitted_at` ordering, and a pair of
triggers that maintain the column. The triggers exist because twenty-five
statements across nine files update a session and every future one would
otherwise have to remember the column; each leaves an explicitly written value
alone, so a caller that wants the Store's own clock still gets it.

Migration 48 renames the grouping inside a checkpoint from boulder to stone:
`planning_boulders` becomes `planning_stones`, and the `boulder_id` on it and
on `planning_pebbles` becomes `stone_id`. A pure rename, like migration 30, and
it refuses rather than run if a `planning_stones` table is already there. The
word was doing two jobs, naming both the whole idea a project plans and one
chunk of one rung of it; only the inner one moved.

Current lifecycle tables include `pipelines`, `pipeline_stages`, `tasks`,
`task_repositories`, `runs`, `sessions`, `session_stages`, `work_updates`,
`work_answers`, `executions`, `attempts`, `attempt_events`, `workers`,
`repositories`, `planning_projects`, `planning_checkpoints`,
`planning_stones`, `planning_pebbles`, Worker repository state, claim request
deduplication, and Worker enrollment or credentials. Older migration tables may remain for
history and upgrade compatibility but are not part of the current UI or
admission path.

## Known limitations

- Only the embedded SQLite orchestration path exists.
- Pipelines are linear agent stages. They do not branch, run stages in
  parallel, contain deterministic actions, or move an active Session between
  Workers. Multi-stage Pipelines currently require a persistent Worker.
- Cloud Run execution profiles and elastic dispatch are designed but not
  implemented.
- The finite operator CLI and browser do not yet expose answer, retry,
  replacement, and cancellation controls. Their loopback APIs and lifecycle
  behavior exist.
- Managed repository acquisition supports GitHub through `gh`.
- A retry without a pending checkpoint or stable publish ref may observe a
  newer default branch commit. Once either recovery source exists, fallback is
  forbidden.
- Remote Workers require operator-managed TLS certificates and enrollment.
- Windows Workers are unsupported.
- Execution isolates worktrees and process groups but does not sandbox hostile
  repository code or network egress.
- A checkpoint's critique passes and their cost are not populated. The roadmap
  carries the fields and returns them empty until the critique pipeline exists
  and Work admission records which checkpoint a critique belongs to.

## Source map

| Area | Primary files |
| --- | --- |
| Operator CLI | `cmd/factory/main.go`, `internal/factorycli/command.go`, `internal/factorycli/client.go` |
| Build admission and journal | `internal/controlplane/build.go`, `internal/factorycli/build.go`, `internal/factorycli/admission_journal.go`, `internal/protocol/build.go` |
| Procedure fleet admission | `internal/controlplane/procedures.go`, `internal/factorycli/procedures.go`, `internal/protocol/procedures.go` |
| Server startup and config | `cmd/factory-server/main.go`, `cmd/factory-server/config.go` |
| HTTP routes and auth | `internal/controlplane/http.go`, `internal/controlplane/worker_auth.go` |
| Pipeline templates and stages | `internal/controlplane/pipelines.go`, `internal/controlplane/stage_runs.go`, `internal/protocol/tasks.go` |
| Task, Run, and Work model | `internal/controlplane/tasks.go`, `internal/controlplane/work.go`, `internal/controlplane/resume.go`, `internal/controlplane/replace.go`, `internal/protocol/tasks.go` |
| Work list, detail, and cost | `internal/controlplane/work.go`, `internal/controlplane/work_detail.go`, `internal/controlplane/work_http.go`, `internal/controlplane/overview_cost.go` |
| Planning and roadmap | `internal/controlplane/planning.go`, `internal/controlplane/planning_http.go`, `internal/controlplane/roadmap.go`, `internal/controlplane/roadmap_work.go`, `internal/controlplane/roadmap_http.go` |
| Pause | `internal/controlplane/settings.go`, `internal/controlplane/settings_http.go`, `internal/controlplane/store.go` |
| Schedule admission | `internal/controlplane/task_scheduler.go`, `internal/controlplane/schedule_cron.go` |
| Routing and claims | `internal/controlplane/task_claim.go`, `internal/controlplane/state.go` |
| Lease sweep and recovery | `internal/controlplane/server.go`, `internal/controlplane/recovery.go` |
| Worker manager | `internal/worker/manager.go`, `internal/worker/registration.go`, `internal/worker/claiming.go` |
| Attempt execution | `internal/worker/attempt_lifecycle.go`, `internal/worker/supervisor.go`, `internal/worker/events.go` |
| Git, checkpoints, and worktrees | `internal/worker/git.go`, `internal/worker/agent_update.go`, `internal/worker/repository_cache.go`, `internal/worker/reconcile.go` |
| Protocol limits and types | `internal/protocol/types.go`, `internal/protocol/prompt.go`, `internal/protocol/stage_report.go` |
| Schema | `migrations/027_routines_work.sql`, `migrations/030_task_run_session.sql`, `migrations/031_work_lifecycle.sql`, `migrations/032_agent_update_requests.sql`, `migrations/033_pipeline_templates.sql`, `migrations/034_resume_recovery.sql`, `migrations/044_orchestrator_brief_and_pause.sql`, `migrations/045_work_list.sql`, `migrations/046_planning_state.sql`, `migrations/047_read_only_stages_and_planning_work.sql` |
| Browser UI | `web/src/App.tsx`, `web/src/Work.tsx`, `web/src/WorkDetail.tsx`, `web/src/work-format.ts`, `web/src/Pipelines.tsx`, `web/src/Tasks.tsx`, `web/src/Runs.tsx`, `web/src/Workers.tsx`, `web/src/Repositories.tsx` |

## Verification

- `internal/controlplane/planning_test.go` proves the full status transition
  table in both directions, that a frozen or built checkpoint refuses plan
  edits while still accepting answers, that pebbles are refused unless the
  checkpoint is frozen, and every text bound and identifier rule.
- `internal/controlplane/planning_migration_test.go` proves migration 46
  applies to a fresh database and to one already at 45 that holds real rows,
  and that its keys, status CHECK, foreign keys, and cascades hold.
- `internal/controlplane/planning_http_test.go` proves every route round trips
  and that each refusal reaches the caller as a stable code.
- `internal/controlplane/roadmap_test.go` proves the roadmap the Planning page
  reads is built correctly from seeded rows, including pebble grouping, the
  catch-all for ungrouped pebbles, the derived waiting list, and the Work join
  by recorded id and by title.
- `internal/controlplane/roadmap_passes_test.go` runs real critique Work items
  through admission, claim, and completion, and proves the passes they become:
  round, model, summed attempt cost, measured duration, Work state as outcome,
  the live pass, the roll-up to checkpoint and project, that rounds are counted
  distinctly from attempts, and that a finished critique's result reads back
  over `GET /api/v1/work/{work_id}`.
- `internal/controlplane/critique_test.go` proves the built-in Critique
  Pipeline's every stage setting, that its prompt is the skill body verbatim
  followed by the task, that it cannot be deleted, that a tampered copy is
  repaired on restart, and that an unchanged restart moves nothing.
- `internal/controlplane/planning_admission_test.go` proves the planning triple
  is stored on the Work, folded like a planning project id, absent on ordinary
  Work, refused field by field with its own code, replayed through the request
  key, and admitted as `process_exit`.
- `internal/controlplane/read_only_stage_test.go` and
  `internal/worker/read_only_stage_test.go` prove the read-only flag is refused
  on mechanical stages, survives save, read, freeze, and claim, is refused on a
  runtime that cannot enforce it, becomes the exact `--disallowedTools`
  argument, is absent on an ordinary stage, and drops both reporting contracts
  from the prompt.
- `internal/controlplane/work_lifecycle_test.go` proves outcome-contract
  freezing, backend compatibility, Work states, bounded update history,
  replacement guards, ordered targets, and legacy prompt limits.
- `internal/controlplane/resume_test.go` proves answer and pending-SHA
  persistence, exact start enforcement, retry and cancellation retention,
  bounded continuation history, and exact replacement replay.
- `internal/worker/resume_test.go` proves clean and published checkpoint rules,
  moved and missing ref failures, pending-SHA precedence, and exact known-PR ref
  restoration.
- `internal/controlplane/build_test.go`, `internal/protocol/build_test.go`, and
  `internal/factorycli/build_test.go` prove atomic Build admission,
  normalization, replay ordering, runtime freezing, duplicate and rebuild
  guards, scheduler claim, typed commit status, journal recovery, locking, and
  wait exit codes.
- `internal/controlplane/procedures_test.go`,
  `internal/protocol/procedures_test.go`, and
  `internal/factorycli/procedures_test.go` prove Procedure listing, explicit and
  all-repository selection, atomic freezing, replay before mutable reads,
  rebuild lineage, journal recovery, and fair cross-Run claims.
- `internal/controlplane/tasks_migration_test.go` opens populated historical
  databases and proves identity, lifecycle, scheduled process-exit completion,
  and foreign-key preservation.
- `internal/controlplane/pipelines_test.go` proves Pipeline validation, prompt
  interpolation, immutable snapshots, ordered stage transitions, and final Run
  completion.
- `web/e2e/control-plane.spec.ts` proves the visual editor, Task selection, and
  three fresh agent processes completing in sequence through a real server and
  Worker.
- `web/e2e/work-board.spec.ts` proves, against the same real server and Worker,
  that one multi-repository Run becomes one card per repository, that a
  repository tab shows only its own, that a Work record opens on its brief, that
  its parent Run is reachable, that a runtime reporting no cost never renders as
  zero, and that pause refuses admission until resume.
- `internal/controlplane/pause_test.go` names every admission and dispatch path
  so a new one added without a gate fails there, and proves request-key replay
  still returns its original result while paused.
- `internal/controlplane/work_page_test.go` proves the multi-repository split,
  repository and state filtering, cursor paging that neither repeats nor skips,
  and that an unreported cost stays unset.
- `internal/controlplane/work_detail_test.go` and
  `internal/protocol/stage_report_test.go` prove that verification counts only
  what Factory ran or an agent explicitly contracted, that a result which does
  not follow the contract yields nothing rather than a guess, and that stage
  handoffs distinguish a stage that failed from one that never started.
- `go test ./cmd/... ./internal/...` covers entry points, CLI routing and output,
  API contracts, storage, Worker lifecycle, and release construction.
- `just format-check`, `just vet`, `just boundary`, and `just test` provide the
  repository-wide formatting, static-analysis, dependency, and test proof.
- `just test-tooling` proves the Node-free build produces the complete operator
  binary set.
- `just test-launcher` proves server and Worker readiness and signal handling.
- `just test-release` proves archive contents, metadata, reproducibility, and
  native execution.
