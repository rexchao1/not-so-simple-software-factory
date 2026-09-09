# agent-skills

One repo for the agent skills you want on every device, installed by copy into the user-level skill directories that Claude Code (`~/.claude/skills`) and Codex (`~/.codex/skills`) read.
Skills follow the Agent Skills standard: one directory per skill, a `SKILL.md` with `name`, `description`, and `user-invocable` frontmatter, nothing else required.

It is also the place to manage those skills from.
Start a session in this repo to add, try, or remove one; `AGENTS.md` is the context that session gets.

A secrets broker, Tailscale use, and device bootstrap are useful to pair this repo with, but they hold real credentials and belong in a private repo of your own; nothing like that is included here.
The `dark-factory` and `light-factory` skills talk to a separate factory server; see [rexchao1/factory](https://github.com/rexchao1/factory) for that project.

## Layout

| Path | What |
|---|---|
| `skills/<name>/SKILL.md` | The skills. Every directory here is installed. |
| `bin/skills` | The manager. The only implementation of install and friends. |
| `home/AGENTS.md`, `home/CLAUDE.md` | The instructions every session gets in every project. AGENTS.md holds them, CLAUDE.md imports it. Installed to `~/.claude/`. |
| `AGENTS.md`, `CLAUDE.md` | Context for an agent session in this repo. `CLAUDE.md` imports `AGENTS.md`. Not installed anywhere. |
| `install.sh`, `install.ps1` | Install entry points. `install.sh` wraps `bin/skills install`; `install.ps1` is a standalone Windows installer. |
| `tests/lint.sh` | Frontmatter lint. `name` must equal the directory, `description` required, `user-invocable` must be `true` or `false`, no em dashes. |
| `tests/loads.test.sh` | Asks `claude -p` for its skill list and checks every skill here is named in it as a whole entry. Needs Keychain access, so run it from a GUI terminal. |
| `docs/harness-notes.md` | Verified behaviour of nine agent harnesses, kept as a reference. |
| `docs/adapter-pattern.md` | How one set of hook scripts was wired into several harnesses. |
| `docs/reference/firstmate-adapters/` | The original per-harness hook config files. |

## Manage

```sh
bin/skills status                          # what is here, what is installed, what drifted
bin/skills new <name>                      # scaffold a skill
bin/skills install                         # all skills and the home file, all targets, lints first
bin/skills install --only <name> --targets claude
bin/skills install --dry-run
bin/skills uninstall <name>                # drop the installed copies, keep the source
bin/skills remove <name> --yes             # uninstall everywhere and delete the source
bin/skills lint
bin/skills test                            # lint, then the discovery canary
bin/skills home status                     # whether ~/.claude/{CLAUDE,AGENTS}.md match home/
bin/skills home install [--force]          # copy home/CLAUDE.md and home/AGENTS.md to ~/.claude/
```

`status` also lists skills installed in a target but absent from this repo.
Those are unmanaged, and no command touches them unless you pass `--unmanaged`.

Installing replaces `~/.claude/skills/<name>` and `~/.codex/skills/<name>` for each skill and touches nothing else in those directories.
`install.sh` and `install.ps1` remain for the bootstrap and for Windows:

```sh
./install.sh --targets claude --dry-run
```

```powershell
.\install.ps1 -Targets claude -DryRun
```

## Skills

| Skill | Invocable | Purpose |
|---|---|---|
| `unslop` | `/unslop` | Cut AI tells from prose and put voice back in: puffery, AI vocabulary, em dashes, filler, jargon, passive voice. |
| `stow` | `/stow` | Sweep a session for durable knowledge and file it into existing conventions, with tiered decaying memory markers. |
| `diagnostic-reasoning` | model-loaded | Reproduce end to end first, separate trigger from masking condition from symptom, seek disconfirming evidence. |
| `ask-user-authority` | model-loaded | Decide whether a reviewer's "ask the user" finding is a correction within scope or a contract expansion that needs the user. |
| `dark-factory` | `/dark-factory` | Send a decided spec to the factory, read what it is doing, and answer its questions. One pull request per task, built on your factory host. |
| `light-factory` | `/light-factory` | Plan a big idea with the human: route, PRD with cited decisions, a fresh critic as factory Work, lavish review, freeze, pebbles for the dark factory. |
| `triage` | model-loaded | Decide whether a request is done here, sent to the dark factory as a spec, or planned in the light factory. |

## Hooks

One `SessionStart` hook, `npx -y chrome-devtools-axi`, which prints that tool's capability manifest so a session knows it exists.
An AXI CLI run with no arguments describes itself on stdout, and a `SessionStart` hook injects that into the session as context.

There is no `lavish-axi` hook: the `lavish` skill already carries the same manifest and invokes the tool through `npx`, so the hook only duplicated it into every session's context.


## Adding a skill

1. `bin/skills new <name>` scaffolds `skills/<name>/SKILL.md` with correct frontmatter.
2. Write the body standalone: no private paths, no assumptions about which harness loaded it.
3. `bin/skills install --only <name>`, then `bin/skills status` to confirm.

## Provenance

`stow`, `diagnostic-reasoning`, `ask-user-authority`, `docs/harness-notes.md`, and the adapter reference files were taken from [kunchenguid/firstmate](https://github.com/kunchenguid/firstmate) (MIT, Copyright 2026 Kun Chen) when that checkout was retired on 2026-09-03.
`stow` is the public version, unchanged.
The other two skills had their firstmate-specific wording (captain, yolo, crewmate) replaced with generic terms.
See `NOTICE.md`.
