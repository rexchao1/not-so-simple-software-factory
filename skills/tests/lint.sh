#!/usr/bin/env bash
# Lint for this repo's installable content.
# Every skills/*/SKILL.md: file exists, opens and closes YAML frontmatter, name
# equals the directory name, description present, user-invocable is true or
# false, no em dashes anywhere.
# hooks/hooks.json: parses, and every hook carries a command.
# home/CLAUDE.md and home/AGENTS.md: exist, no em dashes, CLAUDE.md imports AGENTS.md.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fail=0

problem() { echo "lint: $1" >&2; fail=1; }

for skill_dir in "$here"/skills/*/; do
  name="$(basename "$skill_dir")"
  file="$skill_dir/SKILL.md"
  [ -f "$file" ] || { problem "$name: missing SKILL.md"; continue; }

  [ "$(head -n1 "$file")" = "---" ] || problem "$name: SKILL.md must start with '---'"

  # Frontmatter is lines 2..(first closing ---); require the close.
  close="$(awk 'NR>1 && $0=="---" {print NR; exit}' "$file")"
  [ -n "$close" ] || { problem "$name: frontmatter never closes"; continue; }
  front="$(sed -n "2,$((close-1))p" "$file")"

  got_name="$(printf '%s\n' "$front" | sed -n 's/^name:[[:space:]]*//p' | head -n1)"
  [ "$got_name" = "$name" ] || problem "$name: frontmatter name is '$got_name', must equal directory name"

  printf '%s\n' "$front" | grep -q '^description:' || problem "$name: frontmatter needs a description"

  got_invocable="$(printf '%s\n' "$front" | sed -n 's/^user-invocable:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -n1)"
  case "$got_invocable" in
    true|false) ;;
    "") problem "$name: frontmatter needs 'user-invocable: true' or 'user-invocable: false'" ;;
    *) problem "$name: user-invocable is '$got_invocable', must be true or false" ;;
  esac

  if grep -n $'\xe2\x80\x94' "$file" >/dev/null; then
    problem "$name: contains an em dash; use a plain dash"
  fi
done

for home_name in CLAUDE.md AGENTS.md; do
  home_file="$here/home/$home_name"
  if [ ! -f "$home_file" ]; then
    problem "home/$home_name: missing, and bin/skills install copies it to every target"
  elif grep -q $'\xe2\x80\x94' "$home_file"; then
    problem "home/$home_name: contains an em dash; use a plain dash"
  fi
done
grep -q '^@AGENTS.md$' "$here/home/CLAUDE.md" || problem "home/CLAUDE.md: must import AGENTS.md with a line reading @AGENTS.md"

hooks_file="$here/hooks/hooks.json"
if [ -f "$hooks_file" ]; then
  if ! command -v jq >/dev/null; then
    echo "lint: jq is not on PATH, skipping hooks/hooks.json" >&2
  elif ! jq -e . "$hooks_file" >/dev/null 2>&1; then
    problem "hooks/hooks.json: not valid JSON"
  else
    bad="$(jq -r '[to_entries[] | .value[]? | .hooks[]? | select(has("command") | not)] | length' "$hooks_file")"
    [ "$bad" = "0" ] || problem "hooks/hooks.json: $bad hook(s) with no command"
    if grep -q $'\xe2\x80\x94' "$hooks_file"; then
      problem "hooks/hooks.json: contains an em dash; use a plain dash"
    fi
  fi
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "lint: ok ($(ls -d "$here"/skills/*/ | wc -l | tr -d ' ') skills, $(command -v jq >/dev/null && jq '[to_entries[] | .value[]? | .hooks[]?] | length' "$hooks_file" || echo '?') hooks)"
