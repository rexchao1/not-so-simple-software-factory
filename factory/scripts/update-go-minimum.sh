#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! "$1" =~ ^go1\.25\.[0-9]+$ ]]; then
  printf 'usage: %s go1.25.PATCH\n' "${0##*/}" >&2
  exit 2
fi

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

next="${1#go}"
current="$(awk '$1 == "go" { print $2; exit }' go.mod)"
if [[ ! "$current" =~ ^1\.25\.[0-9]+$ ]]; then
  printf 'go.mod declares unsupported minimum %s; expected Go 1.25.x\n' "$current" >&2
  exit 1
fi
release_pin="$(tr -d '[:space:]' < .release/go-version)"
if [[ "$release_pin" != "$current" ]]; then
  printf '.release/go-version contains %s; expected %s from go.mod\n' "$release_pin" "$current" >&2
  exit 1
fi
if [[ "$current" == "$next" ]]; then
  printf 'Go minimum is already %s\n' "$current"
  exit 0
fi
current_patch="${current##*.}"
next_patch="${next##*.}"
if ((10#$next_patch < 10#$current_patch)); then
  printf 'Refusing to lower Go minimum from %s to %s\n' "$current" "$next"
  exit 0
fi

go mod edit -go="$next"
printf '%s\n' "$next" > .release/go-version
documents=(
  README.md
  CONTRIBUTING.md
  SECURITY.md
  docs/local.md
  .github/ISSUE_TEMPLATE/bug_report.yml
)
for document in "${documents[@]}"; do
  if ! grep -Fq "$current" "$document"; then
    printf '%s does not contain the declared Go minimum %s\n' "$document" "$current" >&2
    exit 1
  fi
done
CURRENT="$current" NEXT="$next" perl -pi -e 's/\Q$ENV{CURRENT}\E/$ENV{NEXT}/g' "${documents[@]}"
printf 'Updated Go minimum from %s to %s\n' "$current" "$next"
