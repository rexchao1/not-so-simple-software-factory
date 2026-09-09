# Run a Worker on a remote VM

Factory keeps its browser and operator API on loopback. Remote VMs connect to a
separate HTTPS listener that exposes only enrollment and the Worker lifecycle.
The server does not make inbound connections to the VM.

## Configure the server

Install a TLS certificate whose names include the address used by the VM. Add
the remote listener to `~/.factory/config.toml`:

```toml
listen = "127.0.0.1:7337"
database = "server/factory.sqlite3"

worker_listen = "0.0.0.0:7443"
worker_tls_cert = "/etc/factory/tls/server.crt"
worker_tls_key = "/etc/factory/tls/server.key"
```

All three `worker_*` settings are required together. The local browser API
remains available only at `127.0.0.1:7337`. Permit inbound TCP traffic to the
Worker port only from trusted Worker networks.

## Create a one-time enrollment

First print the stable identity on the VM without starting the Worker:

```sh
factory-worker identity --config /etc/factory/worker.toml
```

Then create an enrollment bound to that identity through the local operator API
on the server host:

```sh
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d '{"worker_id":"PASTE_WORKER_ID"}' \
  http://127.0.0.1:7337/api/v1/worker-enrollments
```

The response contains the bound `worker_id`, an `enrollment_token`, and its
expiry. It is valid for ten minutes and can be exchanged once by that identity.
Transfer that short-lived value to the VM through your normal secret channel.

## Configure the VM

```toml
server = "https://factory.example.com:7443"
name = "build-vm-01"
runtimes = ["codex", "claude-code"]
max_concurrent = 10
enrollment_token = "factory_enroll_REDACTED"

# Set this for a private certificate authority. Public certificates use the
# operating system trust store and do not need it.
ca_certificate = "/etc/factory/tls/ca.crt"

[labels]
region = "eu-west"
host = "build-vm-01"
```

Start `factory-worker` normally. It exchanges the one-time token over TLS and
saves the returned Worker credential as `worker-credential` inside its
owner-only data directory. The credential is never printed or placed in the
enrollment command. Remove `enrollment_token` from the TOML after the first
successful start.

The VM reuses its protected `worker-id` and credential after restarts. Deleting
the data directory creates a new identity. To rotate a credential, stop the
Worker, remove `worker-credential` and `worker-credential.pending`, create a new enrollment, update the
short-lived token, and restart it.

The saved credential is bound to the exact Factory server origin. Changing the
`server` setting while reusing the data directory fails before making a request,
so a credential cannot be disclosed to a different endpoint. Explicitly rotate
the credential when intentionally moving a Worker identity between servers.

## Connection behavior

Remote and local Workers use the same Jobs, claims, leases, cancellation, event,
and completion contract. Labels, capacity, and coding-agent capabilities appear
on the Worker profile. A disconnected VM becomes offline after its registration
heartbeat expires. An active attempt becomes lost after its lease expires. When
the VM reconnects with the same data directory it returns under the same stable
Worker identity.

The remote endpoint rejects operator APIs and checks every attempt against the
authenticated Worker identity. Factory does not provision VMs, distribute
agent or GitHub credentials, or manage Kubernetes pods.
