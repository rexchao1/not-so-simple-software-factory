#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
listen_override=${FACTORY_LISTEN:-${FACTORY_V2_LISTEN:-}}
data_home=${FACTORY_DATA_HOME:-${FACTORY_V2_DATA_HOME:-}}
if [ -z "$data_home" ]; then
  if [ -z "${HOME:-}" ]; then
    echo "Factory startup requires HOME or FACTORY_DATA_HOME." >&2
    exit 1
  fi
  legacy_data_home=
  legacy_config=
  if [ -f "$HOME/.factory/worker.toml" ] ||
    [ -f "$HOME/.factory/server/factory.sqlite3" ] ||
    [ -f "$HOME/.factory/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$HOME/.factory/workers" ]; then
    data_home="$HOME/.factory"
  elif [ -f "$root/.factory-v2/worker.toml" ] ||
    [ -f "$root/.factory-v2/data/server/factory.sqlite3" ] ||
    [ -f "$root/.factory-v2/data/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$root/.factory-v2/data/workers" ]; then
    legacy_data_home="$root/.factory-v2/data"
    legacy_config="$root/.factory-v2/worker.toml"
  elif [ -f "$HOME/.factory-v2/worker.toml" ] ||
    [ -f "$HOME/.factory-v2/server/factory.sqlite3" ] ||
    [ -f "$HOME/.factory-v2/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$HOME/.factory-v2/workers" ]; then
    legacy_data_home="$HOME/.factory-v2"
    legacy_config="$HOME/.factory-v2/worker.toml"
  fi
  if [ -n "$legacy_data_home" ]; then
    echo "Factory found preview state and refused to replace it with an empty ~/.factory home." >&2
    if [ -f "$legacy_config" ]; then
      echo "Resolve its work by running:" >&2
      echo "  FACTORY_DATA_HOME=\"$legacy_data_home\" \"$root/scripts/run-local.sh\" \"$legacy_config\"" >&2
    else
      echo "The matching worker configuration was not found at $legacy_config." >&2
      echo "Set FACTORY_DATA_HOME=$legacy_data_home when reopening factory-server, and restore the matching worker configuration before starting a worker." >&2
    fi
    echo "Archive the old state after its attempts and retained worktrees are resolved." >&2
    exit 1
  fi
  if [ -z "$data_home" ]; then
    data_home="$HOME/.factory"
  fi
fi
build_directory=${FACTORY_BUILD_DIR:-${FACTORY_V2_BUILD_DIR:-"$data_home/bin"}}
config=${1:-${FACTORY_WORKER_CONFIG:-${FACTORY_V2_WORKER_CONFIG:-"$data_home/worker.toml"}}}
worker_ready_seconds=${FACTORY_WORKER_READY_SECONDS:-${FACTORY_V2_WORKER_READY_SECONDS:-40}}
skip_build=${FACTORY_SKIP_BUILD:-${FACTORY_V2_SKIP_BUILD:-0}}

case "$(uname -s)" in
  Darwin|DragonFly|FreeBSD|Linux|NetBSD|OpenBSD|SunOS) ;;
  *)
    echo "Factory local workers are supported only on Unix." >&2
    exit 1
    ;;
esac

if [ ! -f "$config" ]; then
  echo "Factory worker configuration does not exist: $config" >&2
  echo "Copy examples/worker.toml there and set the repository paths first." >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "Factory local startup requires curl on PATH." >&2
  exit 1
fi

if [ "$skip_build" != "1" ]; then
  if ! command -v just >/dev/null 2>&1; then
    echo "Factory local startup requires just on PATH." >&2
    exit 1
  fi
  FACTORY_BUILD_DIR="$build_directory" \
    just --justfile "$root/Justfile" --working-directory "$root" build
fi

server_binary="$build_directory/factory-server"
worker_binary="$build_directory/factory-worker"
if [ ! -x "$server_binary" ] || [ ! -x "$worker_binary" ]; then
  echo "Factory binaries are missing. Run just build first." >&2
  exit 1
fi

export FACTORY_DATA_HOME="$data_home"

if [ -n "$listen_override" ]; then
  listen=$listen_override
else
  listen=$("$server_binary" -print-listen)
fi

server_pid=
worker_pid=

curl_before_deadline() {
  endpoint=$1
  deadline=$2
  now=$(date +%s)
  remaining=$((deadline - now))
  if [ "$remaining" -le 0 ]; then
    return 1
  fi
  curl --silent --show-error --fail --max-time "$remaining" "$endpoint"
}

stop_processes() {
  trap - INT TERM EXIT
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -TERM "$worker_pid" 2>/dev/null || true
  fi
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
  fi
  if [ -n "$worker_pid" ]; then
    wait "$worker_pid" 2>/dev/null || true
  fi
  if [ -n "$server_pid" ]; then
    wait "$server_pid" 2>/dev/null || true
  fi
}

stop_after_signal() {
  stop_processes
  exit 0
}

trap stop_after_signal INT TERM
trap stop_processes EXIT

echo "Starting Factory server on http://$listen/ ..."
if [ -n "$listen_override" ]; then
  "$server_binary" -listen "$listen" &
else
  "$server_binary" &
fi
server_pid=$!

ready=0
server_ready_seconds=10
server_ready_deadline=$(($(date +%s) + server_ready_seconds))
while [ "$(date +%s)" -lt "$server_ready_deadline" ]; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory server exited before becoming ready. Check the error above." >&2
    exit 1
  fi
  if curl_before_deadline "http://$listen/healthz" "$server_ready_deadline" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.1
done
if [ "$ready" != "1" ]; then
  echo "Factory server did not become healthy within $server_ready_seconds seconds." >&2
  exit 1
fi

echo "Starting Factory worker with $config ..."
worker_id=$("$worker_binary" identity --config "$config")
"$worker_binary" --config "$config" &
worker_pid=$!

registered=0
worker_ready_deadline=$(($(date +%s) + worker_ready_seconds))
while [ "$(date +%s)" -lt "$worker_ready_deadline" ]; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory server stopped during worker startup." >&2
    exit 1
  fi
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory worker exited during startup. Check the configuration and error above." >&2
    exit 1
  fi
  if curl_before_deadline "http://$listen/api/v1/workers/$worker_id" "$worker_ready_deadline" |
	grep -q "\"health\":\"healthy\",\"online\":true"; then
    registered=1
    break
  fi
  sleep 0.1
done
if [ "$registered" != "1" ]; then
  echo "Factory worker did not register as healthy within $worker_ready_seconds seconds. Check its health errors above." >&2
  exit 1
fi
if ! kill -0 "$server_pid" 2>/dev/null; then
  wait "$server_pid" || true
  echo "Factory server stopped before startup completed." >&2
  exit 1
fi
if ! kill -0 "$worker_pid" 2>/dev/null; then
  wait "$worker_pid" || true
  echo "Factory worker stopped before startup completed." >&2
  exit 1
fi

echo "Factory is ready at http://$listen/"
echo "Press Ctrl-C to stop the server and worker. State remains in $data_home."

while :; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory server stopped unexpectedly." >&2
    exit 1
  fi
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory worker stopped unexpectedly." >&2
    exit 1
  fi
  sleep 1
done
