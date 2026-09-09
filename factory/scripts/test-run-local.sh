#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/bin"

cat >"$temporary/bin/factory-server" <<'EOF'
#!/bin/sh
resolved_listen() {
  config=${FACTORY_SERVER_CONFIG:-${FACTORY_DATA_HOME:?}/config.toml}
  if [ -f "$config" ]; then
    sed -n 's/^[[:space:]]*listen[[:space:]]*=[[:space:]]*"\([^"]*\)"[[:space:]]*$/\1/p' "$config" | head -1
  else
    echo "127.0.0.1:7337"
  fi
}
if [ "${1:-}" = "-print-listen" ]; then
  resolved_listen
  exit 0
fi
if [ -n "${FACTORY_TEST_SERVER_PID_FILE:-}" ]; then
  echo "$$" >"$FACTORY_TEST_SERVER_PID_FILE"
fi
if [ "${1:-}" = "-listen" ]; then
  listen=$2
else
  listen=$(resolved_listen)
fi
exec node -e '
  const { existsSync } = require("node:fs");
  const { createServer } = require("node:http");
  const port = Number(process.argv[1].split(":").at(-1));
  createServer((request, response) => {
    response.setHeader("content-type", "application/json");
    if (request.url === "/healthz") {
      response.end("{\"status\":\"ok\"}");
      if (process.env.FACTORY_TEST_SERVER_EXIT_AFTER_HEALTH) {
        response.on("finish", () => setTimeout(() => process.exit(0), 25));
      }
      return;
    }
    if (process.env.FACTORY_TEST_HANG_WORKERS) {
      setTimeout(() => process.exit(0), 3000);
      return;
    }
    const registered = existsSync(process.env.FACTORY_TEST_WORKER_MARKER);
    if (request.url === "/api/v1/workers/new-unhealthy-worker") {
      response.end(JSON.stringify({
        id:"new-unhealthy-worker",
        health:registered && process.env.FACTORY_TEST_HEALTHY_WORKER ? "healthy" : "unhealthy",
        online:registered
      }));
      return;
    }
    const workers = [{id:"existing-healthy-worker",health:"healthy",online:true}];
    if (registered) {
      workers.push({
        id:"new-unhealthy-worker",
        health:process.env.FACTORY_TEST_HEALTHY_WORKER ? "healthy" : "unhealthy",
        online:true
      });
    }
    response.end(JSON.stringify({workers}));
  }).listen(port, "127.0.0.1");
' "$listen"
EOF

cat >"$temporary/bin/factory-worker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "identity" ]; then
  echo "new-unhealthy-worker"
  exit 0
fi
if [ -n "${FACTORY_TEST_WORKER_PID_FILE:-}" ]; then
  echo "$$" >"$FACTORY_TEST_WORKER_PID_FILE"
fi
: >"$FACTORY_TEST_WORKER_MARKER"
trap 'exit 0' INT TERM
while :; do sleep 1; done
EOF

chmod +x "$temporary/bin/factory-server" "$temporary/bin/factory-worker"
: >"$temporary/worker.toml"
mkdir "$temporary/retired"
: >"$temporary/retired/factory.sqlite3"
missing_data_root="$temporary/retired/missing/control-plane"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')

set +e
output=$(
  FACTORY_BUILD_DIR="$temporary/bin" \
    FACTORY_DATA_HOME="$missing_data_root" \
    FACTORY_LISTEN="127.0.0.1:$port" \
    FACTORY_SKIP_BUILD=1 \
    FACTORY_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_V2_WORKER_READY_SECONDS=1 \
    "$root/scripts/run-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e

if [ "$status" -ne 1 ]; then
  echo "launcher exit status = $status, want 1" >&2
  echo "$output" >&2
  exit 1
fi
if ! printf '%s\n' "$output" |
  grep -q "Factory worker did not register as healthy within 1 seconds"; then
  echo "launcher did not explain unhealthy readiness failure" >&2
  echo "$output" >&2
  exit 1
fi
if printf '%s\n' "$output" | grep -q "Factory is ready"; then
  echo "launcher reported an unhealthy worker as ready" >&2
  echo "$output" >&2
  exit 1
fi
if [ -e "$temporary/retired/missing" ]; then
  echo "launcher created a Factory data root below retired state before validation" >&2
  exit 1
fi

rm -f "$temporary/worker-started"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')
set +e
output=$(
  FACTORY_BUILD_DIR="$temporary/bin" \
    FACTORY_DATA_HOME="$temporary/data" \
    FACTORY_LISTEN="127.0.0.1:$port" \
    FACTORY_SKIP_BUILD=1 \
    FACTORY_TEST_SERVER_EXIT_AFTER_HEALTH=1 \
    FACTORY_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_WORKER_READY_SECONDS=2 \
    "$root/scripts/run-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e
if [ "$status" -ne 1 ] ||
  ! printf '%s\n' "$output" | grep -q "Factory server stopped during worker startup"; then
  echo "launcher did not report its server exiting during worker startup" >&2
  echo "$output" >&2
  exit 1
fi

rm -f "$temporary/worker-started"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')
set +e
output=$(
  FACTORY_BUILD_DIR="$temporary/bin" \
    FACTORY_DATA_HOME="$temporary/data" \
    FACTORY_LISTEN="127.0.0.1:$port" \
    FACTORY_SKIP_BUILD=1 \
    FACTORY_TEST_HANG_WORKERS=1 \
    FACTORY_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_WORKER_READY_SECONDS=1 \
    "$root/scripts/run-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e
if [ "$status" -ne 1 ] ||
  ! printf '%s\n' "$output" |
    grep -q "Factory worker did not register as healthy within 1 seconds"; then
  echo "launcher did not bound a stalled readiness response" >&2
  echo "$output" >&2
  exit 1
fi

node - "$root" "$temporary" "$temporary/worker.toml" <<'EOF'
const { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } = require("node:fs");
const { createServer } = require("node:net");
const { spawn } = require("node:child_process");
const { join } = require("node:path");

function availablePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

function pidFrom(path) {
  if (!existsSync(path)) return null;
  return Number(readFileSync(path, "utf8").trim());
}

function isAlive(pid) {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}

function terminateFrom(path) {
  const pid = pidFrom(path);
  if (isAlive(pid)) process.kill(pid, "SIGTERM");
}

async function verifySignal(signal, suffix, listenVariable) {
  const root = process.argv[2];
  const temporary = process.argv[3];
  const config = process.argv[4];
  const marker = join(temporary, `worker-started-${suffix}`);
  const serverPidFile = join(temporary, `server-${suffix}.pid`);
  const workerPidFile = join(temporary, `worker-${suffix}.pid`);
  rmSync(marker, { force: true });
  const port = await availablePort();
  let output = "";
  let signalled = false;
  let timedOut = false;

  const environment = {
    ...process.env,
    FACTORY_BUILD_DIR: join(temporary, "bin"),
    FACTORY_DATA_HOME: join(temporary, "data"),
    FACTORY_TEST_HEALTHY_WORKER: "1",
    FACTORY_TEST_SERVER_PID_FILE: serverPidFile,
    FACTORY_TEST_WORKER_MARKER: marker,
    FACTORY_TEST_WORKER_PID_FILE: workerPidFile,
    FACTORY_WORKER_READY_SECONDS: "2"
  };
  delete environment.FACTORY_LISTEN;
  delete environment.FACTORY_V2_LISTEN;
  delete environment.FACTORY_SKIP_BUILD;
  delete environment.FACTORY_V2_SKIP_BUILD;
  if (listenVariable === "config.toml") {
    mkdirSync(environment.FACTORY_DATA_HOME, { recursive: true });
    writeFileSync(join(environment.FACTORY_DATA_HOME, "config.toml"), `listen = "127.0.0.1:${port}"\n`);
    environment.FACTORY_SKIP_BUILD = "1";
  } else {
    environment[listenVariable] = `127.0.0.1:${port}`;
  }
  if (listenVariable === "FACTORY_LISTEN") {
    environment.FACTORY_V2_LISTEN = "127.0.0.1:1";
    environment.FACTORY_SKIP_BUILD = "1";
  } else {
    environment.FACTORY_V2_SKIP_BUILD = "1";
  }

  const launcher = spawn(join(root, "scripts/run-local.sh"), [config], {
    env: environment,
    stdio: ["ignore", "pipe", "pipe"]
  });

  const capture = (chunk) => {
    output += chunk;
    if (!signalled && output.includes("Factory is ready")) {
      signalled = true;
      launcher.kill(signal);
    }
  };
  launcher.stdout.on("data", capture);
  launcher.stderr.on("data", capture);

  const timeout = setTimeout(() => {
    timedOut = true;
    launcher.kill("SIGKILL");
    terminateFrom(workerPidFile);
    terminateFrom(serverPidFile);
  }, 5000);

  const result = await new Promise((resolve, reject) => {
    launcher.once("error", reject);
    launcher.once("close", (code, exitSignal) => resolve({ code, exitSignal }));
  });
  clearTimeout(timeout);

  const serverPid = pidFrom(serverPidFile);
  const workerPid = pidFrom(workerPidFile);
  const leakedServer = isAlive(serverPid);
  const leakedWorker = isAlive(workerPid);
  if (leakedWorker) process.kill(workerPid, "SIGTERM");
  if (leakedServer) process.kill(serverPid, "SIGTERM");

  if (timedOut) throw new Error(`${signal} shutdown exceeded 5 seconds\n${output}`);
  if (!signalled) throw new Error(`launcher never became ready for ${signal}\n${output}`);
  if (!output.includes(`Factory is ready at http://127.0.0.1:${port}/`)) {
    throw new Error(`${listenVariable} did not select port ${port}\n${output}`);
  }
  if (result.code !== 0 || result.exitSignal !== null) {
    throw new Error(`${signal} shutdown returned code=${result.code} signal=${result.exitSignal}\n${output}`);
  }
  if (output.includes("stopped unexpectedly")) {
    throw new Error(`${signal} shutdown was reported as unexpected\n${output}`);
  }
  if (leakedServer || leakedWorker) {
    throw new Error(`${signal} leaked server=${leakedServer} worker=${leakedWorker}\n${output}`);
  }
}

(async () => {
  await verifySignal("SIGTERM", "term", "FACTORY_LISTEN");
  await verifySignal("SIGINT", "int", "FACTORY_V2_LISTEN");
  await verifySignal("SIGTERM", "config", "config.toml");
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
EOF

echo "Factory launcher honors bootstrap listen settings, rejects unhealthy workers, retired-state writes, server loss, and stalled readiness responses; signals stop cleanly."
