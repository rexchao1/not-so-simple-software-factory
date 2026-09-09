#!/usr/bin/env bash
#
# Start factory-server and factory-worker under tmux on the host.
#
# RUN THIS FROM Terminal.app ON THE MAC, not over SSH.
#
# Claude Code keeps its credentials in the macOS Keychain. A login session
# started over SSH cannot read that keychain, so `claude auth status --json`
# returns loggedIn false and the worker reports the claude-code runtime as
# unauthenticated. Starting the tmux server from a GUI session gives every
# process inside it keychain access, including the agent the worker spawns.
#
# Once started this way, the sessions survive logout of the SSH client and can
# be inspected over SSH with `tmux capture-pane`. Plan 1 does not install a
# launchd service, so this script is also what you run after a reboot.

set -euo pipefail

BIN="$HOME/.factory/bin"
CONFIG="$HOME/.factory/worker.toml"

if [[ -n "${SSH_CONNECTION:-}" ]]; then
  echo "WARNING: this looks like an SSH session."
  echo "The worker will report claude-code as unauthenticated, because the"
  echo "macOS Keychain is not reachable from here."
  echo
  printf "Continue anyway? [y/N] "
  read -r reply
  [[ "$reply" =~ ^[Yy] ]] || exit 1
fi

for f in "$BIN/factory-server" "$BIN/factory-worker" "$CONFIG"; do
  [[ -e "$f" ]] || { echo "missing: $f"; exit 1; }
done

echo "Checking Claude Code authentication in this session..."
if command -v claude >/dev/null 2>&1; then
  if claude auth status --json 2>/dev/null | grep -q '"loggedIn": *true'; then
    echo "  ok, logged in"
  else
    echo "  NOT logged in in this session."
    echo "  Run 'claude' here and complete the login, then re-run this script."
    exit 1
  fi
else
  echo "  claude is not on PATH. Check ~/.zprofile."
  exit 1
fi

echo "Stopping any existing factory tmux sessions..."
tmux kill-session -t factory-server 2>/dev/null || true
tmux kill-session -t factory-worker 2>/dev/null || true

echo "Starting factory-server..."
tmux new -d -s factory-server "zsh -lc 'exec $BIN/factory-server'"
sleep 3

echo "Starting factory-worker..."
tmux new -d -s factory-worker "zsh -lc 'exec $BIN/factory-worker -config $CONFIG'"
sleep 5

echo
echo "Sessions:"
tmux ls
echo
echo "Workers:"
"$BIN/factory" workers
echo
echo "If HEALTH is still not healthy, read the capability detail with:"
echo "  $BIN/factory --json workers"
