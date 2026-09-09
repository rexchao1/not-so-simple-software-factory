# agent-skills

This repo is the manager for the agent skills you run on every device.
A session here exists to add a skill, try one out, remove one, change a session hook, or edit this file.
It is not a general codebase: prefer running `bin/skills` over hand-rolling file operations.

## The model

This repo owns three kinds of thing, and all work the same way: one source of truth here, copied out to every target.

`skills/<name>/SKILL.md` is the source of truth for a skill.
Installing copies each skill directory into every target's user-level skill directory, so a target keeps working when this repo is absent or on another disk.

`hooks/hooks.json` is the source of truth for session hooks.
Each target nests the same hook table under a top-level `hooks` key, so one file renders both.
The write is a merge into whatever else the file holds, never a whole-file write, because Claude's target file is its entire settings document.

`home/AGENTS.md` is the source of truth for the instructions every session gets, in every project: who the agent is, triage first, the factory rules, no agent attribution on GitHub.
`home/CLAUDE.md` is one line, `@AGENTS.md`, because Claude Code reads `CLAUDE.md` and imports from there; edit AGENTS.md, never CLAUDE.md.
Both install to `~/.claude/`. Codex has no verified user-level equivalent, so it has no target here.
The home files have no unmanaged state to key on, because they are two fixed files rather than a set of named things.
Install refuses whenever either target differs and shows the line counts, so the human decides whether that is their edit or a stale copy.

| Target | Skill directory | Hooks file | Home instructions |
|---|---|---|---|
| `claude` | `~/.claude/skills` | `~/.claude/settings.json` | `~/.claude/CLAUDE.md`, `~/.claude/AGENTS.md` |
| `codex` | `~/.codex/skills` | `~/.codex/hooks.json` | none |

A skill present in a target but absent from `skills/` is *unmanaged*: it came from somewhere else and this repo does not own it.
A hook is unmanaged on the same terms, keyed on its command, so a hook of ours whose timeout drifted in a target is still ours to repair rather than someone else's to preserve.
`bin/skills status` lists both kinds separately, and no command touches an unmanaged one without `--unmanaged` for a skill or `--force` for a hook.

## The tool

`bin/skills` is the only implementation; `install.sh` is a thin wrapper kept because the infra bootstrap calls it.
Run `bin/skills` with no arguments for usage.

| Command | For |
|---|---|
| `bin/skills status` | What is here, where each skill is installed, what drifted, what is unmanaged. Start here. |
| `bin/skills new <name>` | Scaffold `skills/<name>/SKILL.md` with correct frontmatter. |
| `bin/skills install [--only a,b] [--targets t] [--dry-run]` | Copy into the targets. Lints first and refuses to install if the lint fails. |
| `bin/skills uninstall <name>...` | Drop the installed copies, keep the source. This is how you stop using a skill without deleting it. |
| `bin/skills remove <name>... --yes` | Uninstall everywhere and delete the source. Prints the exact paths and refuses without `--yes`. |
| `bin/skills lint` | Lint alone: skill frontmatter, plus `hooks/hooks.json`. |
| `bin/skills test` | Lint, then the discovery canary. |
| `bin/skills hooks status` | Which of this repo's hooks each target holds, and what it holds that this repo does not define. |
| `bin/skills hooks install [--targets t] [--dry-run] [--force]` | Write the hook table into each target. Refuses rather than dropping a hook this repo does not define. |
| `bin/skills home status` | Whether each target holds this repo's home instructions file, and how far a drifted one has moved. |
| `bin/skills home install [--targets t] [--dry-run] [--force]` | Copy `home/CLAUDE.md` and `home/AGENTS.md` into each target. Refuses rather than overwriting a file that differs. |

## Workflows

**Add a skill.**
`bin/skills new <name>`, write the body, `bin/skills install --only <name>`, then confirm with `bin/skills status`.

**Try a skill without committing to it.**
Install it to one target only (`--targets claude`), use it for a while, then either keep it or `bin/skills uninstall <name>`.
A skill only becomes real to a harness once it is installed, so editing `skills/<name>/SKILL.md` alone changes nothing in a running session.

**Stop using a skill.**
`bin/skills uninstall <name>` first.
Reach for `remove --yes` only when the source should go too.

**Change the home instructions.**
Edit `home/AGENTS.md`, run `bin/skills home install`, then start a fresh session to see it.
If install refuses, the target moved: fold what you want into `home/AGENTS.md` rather than reaching for `--force`.

**Change a hook.**
Edit `hooks/hooks.json`, run `bin/skills hooks install`, then confirm with `bin/skills status`.
Removing a hook is the same move: drop it from the file and reinstall.
If install refuses, a target holds a hook this repo does not define; adopt it into `hooks/hooks.json` rather than reaching for `--force`.

**Change this file.**
`CLAUDE.md` imports `AGENTS.md`, so edit `AGENTS.md` and both harnesses see it.
This file is context for a session in this repo only; it is not installed anywhere and has no effect on other projects.

## Rules

- Never write anywhere in a target except a skill directory this repo owns or the `hooks` key of that target's hooks file.
  Everything else in `~/.claude/settings.json` belongs to Claude Code and to you, and the merge exists to leave it alone.
  `~/.codex/skills/.system` is Codex's own and is never touched; every command skips dotted directories.
- Session hooks run as `npx -y <package>` rather than a bare command name.
  A bare name needs a global npm install, which is per-device state no repo owns, and it evaporates the next time the node version moves; `npx` resolves on any device that has node.
  The cost is a cold first run, so the infra bootstrap warms the npx cache once per device.
  Do not add a hook whose only job is to announce a tool that a skill here already documents: the skill covers discovery, and the hook would spend context on every session to repeat it.
- `bin/skills hooks` needs `jq`.
  `status` degrades to a one-line note when it is missing rather than failing; the hook commands refuse outright.
- A new skill's frontmatter needs `name` equal to its directory, a `description`, and `user-invocable: true` or `false`.
  The lint enforces all three plus the no-em-dash rule.
- Write the `description` as what it does, then a sentence starting with "Use when" that says exactly when to load it.
  The description is the only thing a harness sees before deciding to load the skill, so it carries the whole loading decision.
- Keep a skill body standalone: no private paths, no secrets, no assumption about which harness loaded it.
  These are public files that get copied onto several machines.
- Anything derived from another project goes in `NOTICE.md` with its license and source commit.
- `bin/skills test` shells out to `claude -p` and needs Keychain access, so it only works from a GUI terminal, not a bare SSH session.
  It also cannot be trusted from inside a Claude Code session, since that nests one harness in another.
  Run it yourself and read the result.
