# lib-repo.sh - one answer to "which repository is this", sourced by
# factory-submit and factory-register. Not executable: it is a library.
#
# The factory names a repository as github.com/owner/name, exactly three slash
# separated parts. That is admission's repository field and it is
# protocol.CreateManagedRepositoryRequest.remote_identity. Every form a git
# origin can take has to collapse to that one shape, and it has to collapse the
# same way in both scripts, or a repository gets registered under one spelling
# and submitted under another.
#
# The agent that calls these scripts is nearly always standing in a checkout of
# the repository it is talking about, so the checkout is the best default we
# have and the project map stops being a prerequisite for new work.

# normalize_repo_url <url>
# Prints github.com/owner/name, or prints nothing and returns 1.
# Accepts, with or without a trailing .git or /:
#   https://github.com/owner/name      http://github.com/owner/name
#   https://user@github.com/owner/name ssh://git@github.com/owner/name
#   git@github.com:owner/name          github.com/owner/name
normalize_repo_url() {
  local url="${1:-}" rest host path
  [ -n "$url" ] || return 1
  url="${url%/}"

  case "$url" in
    *://*)  rest="${url#*://}" ;;   # https, http, ssh, git
    *@*:*)  rest="${url%%:*}/${url#*:}" ;;  # scp: git@github.com:owner/name
    *)      rest="$url" ;;
  esac
  rest="${rest#*@}"                 # drop any userinfo

  host="$(printf '%s' "${rest%%/*}" | tr 'A-Z' 'a-z')"
  [ "$host" = "github.com" ] || return 1

  path="${rest#*/}"
  path="${path%/}"
  path="${path%.git}"
  path="${path%/}"

  # Exactly owner/name. Three parts is what the factory rejects with
  # 400 invalid_repository, so it is rejected here instead.
  case "$path" in
    */*/*|/*|*/|*" "*|*"?"*|*"#"*) return 1 ;;
    */*) ;;
    *) return 1 ;;
  esac
  [ -n "${path%%/*}" ] && [ -n "${path#*/}" ] || return 1

  printf 'github.com/%s\n' "$path"
}

# repo_from_checkout
# The repository of the checkout the caller is standing in. Prints
# github.com/owner/name, or explains on stderr and returns 1. Every failure
# names the two ways out, because the caller is an agent that can take either.
repo_from_checkout() {
  local url norm
  if [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" != "true" ]; then
    echo "error: not inside a git checkout, so there is no repository to derive" >&2
    _repo_hint
    return 1
  fi
  url="$(git remote get-url origin 2>/dev/null)"
  if [ -z "$url" ]; then
    echo "error: this checkout has no 'origin' remote to derive a repository from" >&2
    _repo_hint
    return 1
  fi
  norm="$(normalize_repo_url "$url")" || {
    echo "error: origin is not a github.com/owner/name remote: $url" >&2
    echo "hint: the factory manages github.com identities only." >&2
    _repo_hint
    return 1
  }
  printf '%s\n' "$norm"
}

_repo_hint() {
  echo "hint: pass --repo github.com/owner/name, or --project NAME from the map." >&2
}
