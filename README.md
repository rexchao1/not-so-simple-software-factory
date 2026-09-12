# not-so-simple-software-factory

This is a light/dark factory workflow. The light factory and the dark factory are agent skills. They talk to an adapted local factory server. The agent you already sit with is the orchestrator that decides which skill to load.

I ran the server on a Mac mini so I could work on my laptop, send work over, and let Claude, Codex, or Pi keep going on the always-on machine while I was offline. The cockpit in the screenshots is from a fork of [Owain Lewis's factory](https://github.com/owainlewis/factory). What I added is planning, a roadmap, and the skills that connect an orchestrator session to that server.

## How the two factories fit

The thing I started with was a simple software factory: drop a GitHub issue on a queue, wait for a pull request. That is a dark factory. It did not work for me as a way to develop. You still come back and iterate with an agent on the issues it leaves behind, and there is no planning step, so the work drifts from what you actually wanted.

The light factory is the missing half. It is human-in-the-loop planning. You and the orchestrator research an idea, write a route of checkpoints, write a PRD with every decision cited, run a critic, review it in the browser, then freeze it and cut it into small tasks. Only then does anything go to the dark factory.

The dark factory is a skill that teaches the agent how to use a real factory. A decided spec goes in. A worker builds it in an isolated worktree and opens a pull request. Nobody signs off in between.

Triage sits in front of both, in the orchestrator session:

- Small work stays in the conversation.
- A single decided spec goes to the dark factory.
- Anything that needs a human opinion goes to the light factory first.

## In the cockpit

### Work board

Queued, running, and finished work across repositories.

<img src="assets/workSample.png" alt="Factory work board" width="500">

### Roadmap and planning

Milestones broken into checkpoints. Critique passes and results feed back into the same view.

<img src="assets/planningSample.png" alt="Factory planning and roadmap" width="500">

### Workflow execution

Agents running a multi-stage pipeline, each stage isolated.

<img src="assets/flowSample.png" alt="Factory workflow execution" width="500">

## What I learned, and why I do not use this as my daily workflow

I used to read about companies like OpenAI and Uber running dark factories. What I found is that a dark factory is only really useful for a small, precise issue that cannot be gotten wrong, on a codebase that already exists. A GitHub issue that should become a pull request while you are away. A scheduled job that picks off the next issue it can actually solve. An end-to-end test loop. That is the job.

On a small project it is the wrong tool. The factory will run several rounds of implementation and review on a change you could have combined with related work and reviewed once. Models have enough context now that packing those little tasks into one larger change, in one session, ships more pull requests and is much faster.

That is why this is not what I use day to day. I work in one session. I would come back to the dark factory if the codebase got large enough that small, thoroughly checked tasks with no real design in them started to pile up. Remote, always-on, issue in and pull request out, or a cron that keeps a loop like that going: that is where it earns its keep.

The roadmap was the part I kept. A lot of this workflow ended up in the skills, especially a consistent architecture so an agent can read a project without spending the context window on orientation. I will share the workflow I use now separately.

I do not recommend this as your main way of writing code. It does not burn that many credits, but it is slow, and it still leaves you iterating in a session. Try it anyway. The UI is genuinely fun to sit with.

## Quick start

**Factory server** (Claude, Codex, or Pi on the worker host):

```sh
cd factory
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337). Full setup is in [`factory/README.md`](factory/README.md).

**Skills** (Claude Code and/or Codex):

```sh
cd skills
./install.sh --targets claude   # or codex, or both
```

Point the factory skills at your server with `FACTORY_BASE`. There is no default. See [`skills/README.md`](skills/README.md).

## Provenance

- `factory/` is MIT, forked from [`owainlewis/factory`](https://github.com/owainlewis/factory).
- `skills/` is MIT. Some skills were adapted from [`kunchenguid/firstmate`](https://github.com/kunchenguid/firstmate), [`mattpocock/skills`](https://github.com/mattpocock/skills), and [`cursor/plugins`](https://github.com/cursor/plugins). See `skills/NOTICE.md`.

This repository is MIT. See [LICENSE](LICENSE).
