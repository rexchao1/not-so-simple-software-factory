#!/usr/bin/env bash
# Install every skill in ./skills into the user-level skill directories that
# Claude Code and Codex read. Copies, never symlinks, so the target works even
# when this repo is absent or on a different disk.
#
# This is the stable entry point that the infra bootstrap calls. It is a thin
# wrapper; bin/skills is the implementation and has the rest of the commands.
#
# Usage: ./install.sh [--targets claude,codex] [--only a,b] [--dry-run]

set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
exec "$here/bin/skills" install "$@"
