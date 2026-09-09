#!/usr/bin/env bash
# bin/skills home, run against a throwaway HOME.
#
# Every command here runs with HOME pointed at a temp directory, so no test can
# reach the ~/.claude/CLAUDE.md this machine actually reads. That matters more
# than usual: the target is a single file the tool overwrites, not a directory
# it owns, and a bad run would take your own real instructions with it.
#
# Not wired into `bin/skills test`: that runs the discovery canary, which shells
# out to claude and needs Keychain access. This needs neither.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
SKILLS="$ROOT/bin/skills"
SRC="$ROOT/home/CLAUDE.md"

fail=0
ok()    { printf 'ok - %s\n' "$1"; }
notok() { printf 'not ok - %s\n' "$1"; fail=1; }

assert_eq() { # assert_eq <label> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1"
  else notok "$1: expected [$2] got [$3]"; fi
}

assert_contains() { # assert_contains <label> <needle> <haystack>
  case "$3" in
    *"$2"*) ok "$1" ;;
    *) notok "$1: [$2] not found in [$3]" ;;
  esac
}

assert_missing() { # assert_missing <label> <needle> <haystack>
  case "$3" in
    *"$2"*) notok "$1: [$2] should not appear in [$3]" ;;
    *) ok "$1" ;;
  esac
}

FAKE_HOME="$(mktemp -d)"
trap 'rm -rf "$FAKE_HOME"' EXIT
TARGET="$FAKE_HOME/.claude/CLAUDE.md"

run() { HOME="$FAKE_HOME" "$SKILLS" "$@" 2>&1; }

# --- not installed ---------------------------------------------------------
out="$(run home status)"; rc=$?
assert_eq "status exits 0 with nothing installed" 0 "$rc"
assert_contains "status reports not installed" "not installed" "$out"
[ -e "$TARGET" ] && notok "status created the target file" || ok "status wrote nothing"

# --- dry run writes nothing ------------------------------------------------
out="$(run home install --targets claude --dry-run)"; rc=$?
assert_eq "dry run exits 0" 0 "$rc"
assert_contains "dry run says what it would do" "would write home instructions" "$out"
[ -e "$TARGET" ] && notok "dry run wrote the target file" || ok "dry run wrote nothing"

# --- install ---------------------------------------------------------------
out="$(run home install --targets claude)"; rc=$?
assert_eq "install exits 0" 0 "$rc"
assert_contains "install says what it did" "wrote home instructions" "$out"
if cmp -s "$SRC" "$TARGET"; then ok "target matches home/CLAUDE.md"
else notok "target does not match home/CLAUDE.md"; fi

out="$(run home status)"
assert_contains "status reports identical" "installed and identical" "$out"

# --- second install is a no-op ---------------------------------------------
before="$(stat -f %m "$TARGET" 2>/dev/null || stat -c %Y "$TARGET")"
out="$(run home install --targets claude)"; rc=$?
assert_eq "second install exits 0" 0 "$rc"
assert_contains "second install reports current" "current" "$out"
assert_missing "second install does not rewrite" "wrote home instructions" "$out"
after="$(stat -f %m "$TARGET" 2>/dev/null || stat -c %Y "$TARGET")"
assert_eq "second install left the file alone" "$before" "$after"

# --- drift -----------------------------------------------------------------
printf '\n## someone edited this by hand\n' >> "$TARGET"
drifted="$(cat "$TARGET")"

out="$(run home status)"
assert_contains "status reports drift" "installed and drifted" "$out"
assert_contains "status shows the line counts" "line(s) only there" "$out"

out="$(run home install --targets claude)"; rc=$?
assert_eq "drifted install refuses" 2 "$rc"
assert_contains "refusal names --force" "--force" "$out"
assert_eq "refusal left the target alone" "$drifted" "$(cat "$TARGET")"

out="$(run home install --targets claude --force)"; rc=$?
assert_eq "--force exits 0" 0 "$rc"
assert_contains "--force says what it did" "wrote home instructions" "$out"
if cmp -s "$SRC" "$TARGET"; then ok "--force restored the source"
else notok "--force did not restore the source"; fi

# --- codex has no home instructions file -----------------------------------
out="$(run home install --targets codex)"; rc=$?
assert_eq "codex install exits 0" 0 "$rc"
assert_contains "codex is skipped by name" "skipped  codex" "$out"

# --- plain install and plain status include it ------------------------------
# Cleared first, so the assertion is that plain install would write the file,
# not the weaker one that it merely mentions a target already up to date.
rm -f "$TARGET"
out="$(run install --targets claude --dry-run)"; rc=$?
assert_eq "plain install exits 0" 0 "$rc"
assert_contains "plain install covers the home file" "would write home instructions" "$out"
[ -e "$TARGET" ] && notok "plain install dry run wrote the target file" || ok "plain install dry run wrote nothing"

out="$(run status)"
assert_contains "plain status covers the home file" "home instructions: home/CLAUDE.md" "$out"

# --only names skills, and the home file is not a skill.
out="$(run install --only unslop --targets claude --dry-run)"
assert_missing "--only leaves the home file out" "home instructions" "$out"

# --- AGENTS.md rides along with CLAUDE.md ------------------------------------
AGENTS_TARGET="$FAKE_HOME/.claude/AGENTS.md"
out="$(run home install --targets claude --force)"; rc=$?
assert_eq "install with both files exits 0" 0 "$rc"
cmp -s "$ROOT/home/AGENTS.md" "$AGENTS_TARGET" && ok "AGENTS.md installed beside CLAUDE.md" || notok "AGENTS.md missing or differs after install"
grep -q '^@AGENTS.md$' "$TARGET" && ok "installed CLAUDE.md imports AGENTS.md" || notok "installed CLAUDE.md does not import AGENTS.md"
printf 'edited\n' >> "$AGENTS_TARGET"
out="$(run home install --targets claude 2>&1)"; rc=$?
assert_eq "drifted AGENTS.md refuses without --force" 2 "$rc"
assert_contains "refusal names AGENTS.md" "AGENTS.md differs" "$out"
out="$(run home status)"
assert_contains "status reports AGENTS.md drift" ".claude/AGENTS.md  installed and drifted" "$out"
run home install --targets claude --force >/dev/null
cmp -s "$ROOT/home/AGENTS.md" "$AGENTS_TARGET" && ok "--force restored AGENTS.md" || notok "--force did not restore AGENTS.md"

exit "$fail"
