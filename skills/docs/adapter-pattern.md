# Per-harness adapter pattern

This is the shape firstmate used to run one set of hook scripts under several agent harnesses.
It is recorded here as a pattern to reuse when the factory needs hooks that work on both Claude Code and Codex, not as code to copy.
Reference copies of the original config files are under `reference/firstmate-adapters/`.

## Shape

- One canonical skills directory, `.agents/skills/`, with `.claude/skills` as a symlink to it.
  Every harness that reads a skills directory sees the same files.
- One set of plain shell scripts in `bin/` that do the actual work, named by event: session start, pre-tool check, turn-end guard.
  The scripts take a `--claude`, `--codex`, or `--cursor` flag when the payload format differs.
- One thin config file per harness that maps that harness's hook events onto those scripts:
  - Claude Code: `.claude/settings.json`, `hooks` keyed by `SessionStart`, `PreToolUse` (with `matcher`), `Stop`.
    `$CLAUDE_PROJECT_DIR` locates the repo.
    A `Stop` hook can set `asyncRewake: true` with a long timeout to wake the session later.
  - Codex: `.codex/hooks.json`, same event names.
    Codex has no project-dir variable, so each command is a `bash -lc` one-liner that reads the payload from stdin, resolves the repo with `pwd -P`, checks the script exists and is executable, and uses `jq` to confirm the hooks file itself still names that script before running it.
    Every guard exits 0 on failure so a missing tool never blocks the harness.
  - Cursor: `.cursor/hooks.json` with `version: 1` and lower-camel event names (`sessionStart`, `preToolUse`, `stop`), `$CURSOR_PROJECT_DIR`, and a `loop_limit` on `stop`.
  - Grok: `.grok/hooks/*.json`, one file per hook.
  - OpenCode: `.opencode/plugins/*.js`, JavaScript plugins rather than JSON.
  - Pi: `.pi/extensions/*.ts`, TypeScript extensions.
- Cross-harness guards: the Claude hooks start with `[ -z "${GROK_AGENT:-}${GROK_HOOK_EVENT:-}" ] || exit 0` so a Grok session that also reads `.claude/settings.json` does not run the Claude hooks twice.

## What to keep from this

- Put behaviour in scripts, keep harness config files declarative and small.
- Make every hook fail open (exit 0) unless it is a deliberate safety gate.
- Verify the harness facts once and write them down; see `harness-notes.md`.
- Test that the hooks actually fire under each harness, since a mis-keyed event name fails silently.
