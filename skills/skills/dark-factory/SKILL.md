---
name: dark-factory
description: >-
  Send a decided spec to the factory, read what the factory is doing, and answer its questions. Work builds unattended on your factory host, one pull request per task.
  Use when triage says dark, when the light factory hands over pebbles, and for any question about a run.
user-invocable: true
---

# dark-factory

Decided work goes in; a worker builds it in an isolated worktree and opens a pull request.
Nobody signs off in between.
The user's moment is the pull request, or auto-merge where the project has it on.

You submit, read, answer, and report.
The scripts live in `~/.claude/skills/dark-factory/scripts/`, called `$DF` below, and reach the server wherever `FACTORY_BASE` points (a Tailscale network works well for this, but any reachable URL does).

## Rules

1. **The scripts own every request.** When one cannot do what you need, say so.
2. **The user approves.** `pre_approved: true` means they saw the spec; the submit section says exactly when that is true.
3. **Report outcomes faithfully.** Show what the factory said, failures included.
   A spec reaches the public pull request body close to verbatim: write `the user`, never a person's name, a machine's name, or house vocabulary.
4. **Restart the server yourself, never the worker.** Your infra repo's `factory-restart` script restarts `factory-server` on the factory host and waits for `/healthz`; `--status` reports without restarting. The worker spawns Claude Code, which reads the login Keychain, and an SSH session cannot reach it, so a worker that is down is the user's to start from a terminal on the factory host itself. Restarting is not deploying: the binary there is whatever was last installed, and a merge does not change it. A `factory-deploy` script builds the merged server from the local checkout and installs it, rolling back if a worker does not come back; `--dry-run` says what is running now. Anything that ships inside `factory-server`, the cockpit and the critique prompt included, needs it after the merge.

A current, explicit instruction from the user overrides a rule inside exactly the scope it names. Rule 2 is theirs to lift.

## Submit

From the project's checkout:

```bash
$DF/factory-submit --name "<title>" --spec-file <path> --assurance fast|reviewed
```

The repository is the checkout's origin, registered with the factory on first use.
`--repo github.com/owner/name` names another repository; `--project <name>` reads one from the project map.
The spec is always a file.

**Assurance.** `fast`: a change the user described themselves and triage called isolated; one implementation agent, may auto-merge. `reviewed`: everything else, and the default, named anyway so the choice is visible; it runs the project map's pipeline for the repository when there is a line, else `Implement, review, deliver`. `--pipeline "<name>"` overrides either; the script resolves names against the factory's own list.

**Delivery is a project setting,** made in the cockpit. When they ask for auto-merge, say where the toggle is.

### The batch gate

Submitting stops for no prompt, so this presentation is the gate.
With more than one thing to send, present them all, then send the set on a clear go. One block each, short enough for a phone:

```
1. Reject invalid names in greet          scratch
   greet('') returns "Hello, !" today. After this it throws a TypeError.
   Judgment call: whitespace-only names are invalid.

2. Add a farewell function                scratch
   New farewell(name) beside greet, same validation rules.
   Judgment call: none.
```

Title, project, one or two lines of what changes, and the single decision most likely to be wrong. Then stop.

- A clear go: submit the whole set, one block of run ids.
- A comment on one item: revise it and present the set again, whole.
- Silence is not a go.

Pebbles from `/light-factory` go through this gate; a frozen PRD approved the plan, not each spec.
**The factory has no dependencies, so submit in waves.** It runs admitted work in any order, and each task already names what it depends on. Read those lines as a graph, not a chain: a wave is every task whose dependencies are all merged, and the whole wave goes at once. Wait for the wave, merge what is green, then send the next.
Sending a dependent task early makes it build against a repository that lacks what it needs and deliver nothing. Serialising tasks that never touched each other costs a full pipeline round trip each for nothing.

**One small thing goes straight through:** a single change the user described in this conversation, which triage called isolated. Submit it and say what you sent. When you are deciding whether something qualifies, it does not.

**Pre-approved is true** when triage said isolated and they described the change, when they approved a spec you showed them, or when the item was in a batch they said go to. Otherwise `--draft`, which waits for approval in the cockpit.

### What comes back

| State | Means |
|---|---|
| `queued` | admitted, a worker has it |
| `blocked` | admitted, no worker free yet |
| `draft` | waiting for approval in the cockpit, from `--draft` |

Say the state and the run id, then start the wait in the background so its exit brings you back:

```bash
while :; do
  s=$($DF/factory-status <run_id> | awk '$1=="state"{print $2; exit}')
  case "$s" in blocked|queued|running) sleep 60 ;; *) break ;; esac
done
$DF/factory-status <run_id>
```

Run it with `run_in_background`, one wait per run, and report the moment it returns.
The user should never have to ask what the factory is doing.

| Error | Means |
|---|---|
| `invalid_repository` | the map has a URL where `github.com/owner/name` belongs |
| repository disabled | the factory has it switched off; the cockpit's repository page turns it on |
| `pre_approval_not_permitted` | only orchestrator submissions may pre-approve |
| `agent_prompt_too_large` | the spec is too big; split it |

## Status

```bash
$DF/factory-status            # everything
$DF/factory-status <run_id>   # one run
$DF/factory-answer --list     # what is waiting on an answer
```

Read it, then say what it means, leading with what changed since they last asked.
Both halves, always: what the factory did, and what is waiting on whom.

- **`ready`** means a pull request exists and the server verified it against GitHub. On `pr` it waits for you to merge, the default. On `pr+automerge` the script prints one of `completed by Factory`, `refused: <reason>`, or `pending or not attempted`; say the one it printed.
- **`checks`** is the run's own verification, read from the Work list because the run detail does not carry it. `3 passed of 4` counts what the pipeline's code stages actually ran and how they exited. `none recorded` means no stage ran a check the factory could observe, so three green stages prove nothing: run the repository's check commands yourself and read the output before merging.
- **`failed`** means the agent said so. Repeat its message.
- **`blocked`** means no worker was free at admission.
- **`no-change`** can hide a finished change left uncommitted in the worker's worktree; say that possibility and offer a resubmit with a fresh request key.

## Answer

A question from an agent shows up in `--list` and in the cockpit.
Bring it to the user in their words, take their answer, and send it:

```bash
$DF/factory-answer <work_id> "<their answer>"
```

That requeues the work from its checkpoint.
Answer without asking only when a frozen PRD decides it, and cite the line.

## Projects and pipelines

```bash
$DF/factory-register [--delivery pr|pr+automerge]              # this checkout: register, print readiness
$DF/factory-register <project> | --repo github.com/owner/name  # a named one
$DF/factory-register --list
$DF/factory-profiles                                            # execution profiles
$DF/factory-pipelines [--prompts]                               # pipelines, with every stage prompt
$DF/factory-pipeline-set config/pipelines/<name>.json [--create] # make the factory's pipeline match the file
```

A new repository needs nothing; its first submit registers it.
The project map at `~/.factory/dark-factory/projects.tsv` exists for a project that wants a pipeline other than the default: one line per project, name, repository, pipeline, tab separated, with `config/projects.tsv.example` as the shape.
Pipeline definitions in `config/pipelines/` are the source of truth; edit the file, then `factory-pipeline-set`.

## Style

Plain words. The useful thing first. When you do not know, say so and say what you would need.
