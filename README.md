# agent-workflow

A software factory plus the agent skills that drive it: a local-first control plane that runs coding agents against your repositories, and a set of Claude Code / Codex skills that decide what work goes straight into a checkout, what goes to the factory as a spec, and what needs a planning session first.

## What's here

| Directory | What |
|---|---|
| [`factory/`](factory/) | The control plane. Define work once, run it across repositories, watch agents, worktrees, and pull requests from a browser. A personal fork of [`owainlewis/factory`](https://github.com/owainlewis/factory) (MIT). |
| [`skills/`](skills/) | Agent skills for Claude Code and Codex: triage a request, do it now, send it to the factory (`dark-factory`), or plan it first (`light-factory`), plus a handful of general-purpose skills (writing, diagnosing bugs, cutting AI tells from prose, and more). |

The two work together but don't require each other: `skills/` is useful with no factory running (triage, diagnostic-reasoning, unslop, writing-for-agents, etc. all stand alone), and `factory/` is a complete tool on its own if you drive it from its own CLI or web UI instead of through an agent skill.

## Quick start

**Run the factory:**

```sh
cd factory
just build
mkdir -p ~/.factory
cp examples/worker.toml ~/.factory/worker.toml
just run
```

Open [http://127.0.0.1:7337](http://127.0.0.1:7337). See [`factory/README.md`](factory/README.md) for the full quick start, including remote workers and managed GitHub repositories.

**Install the skills:**

```sh
cd skills
./install.sh --targets claude   # or codex, or both
```

See [`skills/README.md`](skills/README.md) for the skill list, the manager (`bin/skills`), and how to add your own.

**Wire them together:** the `dark-factory` and `light-factory` skills talk to the factory's operator API over `FACTORY_BASE`, which has no built-in default — point it at wherever you run the factory server (`export FACTORY_BASE=https://your-factory-host`). `skills/home/AGENTS.md` is a template for the instructions every agent session gets; read it before installing it, since parts of it (the restart/deploy script paths, the "machines" section) describe a specific two-machine setup and are meant to be adapted to yours, not used verbatim.

## Provenance and licensing

Both directories carry their own `LICENSE` and, where content came from elsewhere, a `NOTICE.md`:

- `factory/` is MIT, forked from [`owainlewis/factory`](https://github.com/owainlewis/factory).
- `skills/` is MIT; several skills were adapted from [`kunchenguid/firstmate`](https://github.com/kunchenguid/firstmate), [`mattpocock/skills`](https://github.com/mattpocock/skills), and [`cursor/plugins`](https://github.com/cursor/plugins) — see `skills/NOTICE.md` for exactly which files and which license.

This top-level repository is also MIT; see `LICENSE`.

## Ideas for this repo

Not built yet, in no particular order:

- CI that runs `skills/tests/lint.sh` and the factory's own test suite on every push.
- A setup script or doc that walks through the whole loop end to end on a fresh machine: build the factory, install the skills, submit a first spec.
- A short demo (asciinema or a gif) of triage picking `here` vs `dark` vs `light` on a real request.
- Issue and pull request templates.
- A default `home/AGENTS.md` that doesn't assume a two-machine (workstation + factory host) setup, for people running everything on one machine.
