package worker

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// dockerExecutable is the client the Worker shells out to. It is a package
// variable rather than a constant so the sandbox tests can point it at a
// recording stub without needing Docker installed.
var dockerExecutable = "docker"

// sandboxEnvironmentAllowlist is the whole environment an agent gets inside a
// container. It is an allowlist rather than a denylist because INV-6 says no
// agent process runs with the operator credential in its environment, and a
// denylist can only exclude the credentials someone remembered to name.
//
// The persistent backend still copies os.Environ() wholesale, which is the
// clause of INV-6 that only the docker backend satisfies.
var sandboxEnvironmentAllowlist = []string{
	// The report channel. Without these factory update cannot reach the
	// Worker from inside the container, and no outcome is recordable.
	"FACTORY_RUN_ID",
	"FACTORY_SESSION_ID",
	"FACTORY_WORK_ID",
	"FACTORY_ATTEMPT_ID",
	"FACTORY_UPDATE_SOCKET",
	"FACTORY_UPDATE_TOKEN",
	// The agent's own credential. A long-lived OAuth token from
	// claude setup-token, which needs no Keychain and keeps subscription
	// authentication rather than falling back to a metered API key.
	"CLAUDE_CODE_OAUTH_TOKEN",
	// Delivery.
	"GH_TOKEN",
	"GITHUB_TOKEN",
	// Ordinary process hygiene.
	"HOME",
	"PATH",
	"LANG",
	"TZ",
}

// The Worker reads these from its own environment to build a broker container.
// The two AGENT_VAULT names are what vault-run exports from a device file, so a
// Worker started under "vault-run --device factory-worker" already holds them.
//
// AGENT_VAULT_TOKEN is deliberately absent from sandboxEnvironmentAllowlist.
// The container is given a proxy URL that contains the token, never the token
// as a variable of its own, so nothing in the container can read the credential
// back out and present it to the broker API.
const (
	brokerTokenVariable     = "AGENT_VAULT_TOKEN"
	brokerVaultVariable     = "AGENT_VAULT_VAULT"
	brokerCAVariable        = "FACTORY_BROKER_CA"
	brokerProxyHostVariable = "FACTORY_BROKER_PROXY_HOST"
)

const (
	// brokerGatewayName is the container's name for the Worker's host. The
	// broker binds loopback, and a container's own 127.0.0.1 is not that
	// host. Docker Desktop resolves this name already; a Linux engine needs
	// the --add-host mapping below, which is why the flag is always sent.
	brokerGatewayName = "host.docker.internal"

	// brokerProxyHostDefault is the proxy as seen from inside the container.
	brokerProxyHostDefault = brokerGatewayName + ":14322"

	// brokerContainerCAPath is where the proxy root CA is mounted. Fixed and
	// absolute on purpose: HOME is passed through from the host and names a
	// directory that does not exist in the container, so anything relative to
	// it would resolve nowhere.
	brokerContainerCAPath = "/etc/factory/broker-ca.pem"

	// brokerNoProxy must not name brokerGatewayName. localhost and 127.0.0.1
	// are the container's own loopback; the proxy is not there, and the report
	// channel is a unix socket rather than a TCP port, so it needs no entry
	// here. It would if the update socket were ever exposed over TCP.
	//
	// Nothing else is exempted, so every host the agent reaches goes through
	// the proxy, including the ones the broker has no rule for. Measured
	// rather than assumed: a host with no rule is passed through, and
	// api.anthropic.com answers a proxied request exactly as it answers a
	// direct one. The cost is that a broker container's egress depends on the
	// broker being up, which is what the posture is for.
	brokerNoProxy = "localhost,127.0.0.1"
)

// brokerCARelativePath is where agent-vault installs its proxy root, under the
// operator's home. The Worker looks there so that the documented launch
// command, "vault-run --device factory-worker -- factory worker", is enough on
// its own: vault-run exports the token and the vault but no CA path, and a
// posture that needed a second variable nobody sets would be a posture that
// never worked.
var brokerCARelativePath = []string{".agent-vault", "ca", "ca.crt.pem"}

// brokerSettings is the Worker's own broker configuration, resolved once per
// container from the environment the supervisor assembled.
type brokerSettings struct {
	token     string
	vault     string
	caPath    string
	proxyHost string
}

// sandboxNetworkArgument maps a declared posture onto what docker understands.
// allowlist never reaches here: the control plane rejects it at profile
// validation, because docker alone cannot restrict egress to a host list.
//
// broker is bridge networking. It differs from open in what the container is
// given, not in what it can reach, and that difference is applied by
// sandboxArguments rather than here.
func sandboxNetworkArgument(posture string) string {
	if posture == protocol.NetworkOpen || posture == protocol.NetworkBroker {
		return "bridge"
	}
	return "none"
}

// environmentValues indexes a KEY=VALUE slice. Entries without a separator are
// not environment variables and are dropped.
func environmentValues(source []string) map[string]string {
	values := map[string]string{}
	for _, entry := range source {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[key] = value
	}
	return values
}

// brokerSettingsFromEnvironment resolves what a broker container needs, and
// fails rather than starting a container that cannot reach the broker. A run
// that believed it had a credential route when it did not would fail somewhere
// in the middle of the agent's work, with an error about the third party API
// rather than about the Worker's own configuration.
func brokerSettingsFromEnvironment(source []string) (brokerSettings, error) {
	values := environmentValues(source)
	settings := brokerSettings{
		token:     values[brokerTokenVariable],
		vault:     values[brokerVaultVariable],
		caPath:    values[brokerCAVariable],
		proxyHost: values[brokerProxyHostVariable],
	}
	if settings.proxyHost == "" {
		settings.proxyHost = brokerProxyHostDefault
	}
	// HOME comes from source rather than os.UserHomeDir so this function reads
	// nothing the caller did not hand it. The Worker's environment is the whole
	// input, which is what makes the failure modes testable.
	if settings.caPath == "" && values["HOME"] != "" {
		settings.caPath = filepath.Join(append([]string{values["HOME"]}, brokerCARelativePath...)...)
	}
	// Ordered, not a map, so the message names the same variable every time.
	for _, required := range []struct{ name, value string }{
		{brokerTokenVariable, settings.token},
		{brokerVaultVariable, settings.vault},
		{brokerCAVariable, settings.caPath},
	} {
		if required.value == "" {
			return brokerSettings{}, fmt.Errorf(
				"the broker network posture needs %s in the Worker environment; "+
					"start the Worker under vault-run so it holds a broker credential",
				required.name,
			)
		}
	}
	if _, _, err := net.SplitHostPort(settings.proxyHost); err != nil {
		return brokerSettings{}, fmt.Errorf("%s must be host:port: %w", brokerProxyHostVariable, err)
	}
	if !filepath.IsAbs(settings.caPath) {
		return brokerSettings{}, fmt.Errorf("%s must be an absolute path, got %q", brokerCAVariable, settings.caPath)
	}
	// Docker creates a directory at a bind source that does not exist. Without
	// this check the container would receive an empty directory where the CA
	// belongs and every TLS handshake through the proxy would fail with a
	// certificate error that names nothing useful.
	info, err := os.Stat(settings.caPath)
	if err != nil {
		return brokerSettings{}, fmt.Errorf("broker CA %s: %w", settings.caPath, err)
	}
	if info.IsDir() {
		return brokerSettings{}, fmt.Errorf("broker CA %s is a directory, not a certificate", settings.caPath)
	}
	return settings, nil
}

// brokerContainerEnvironment is the environment a broker container gets on top
// of the allowlist. These values are derived by the Worker, never copied from
// its environment: the proxy URL names a host that only means anything inside
// the container, and the CA path only exists there.
func brokerContainerEnvironment(settings brokerSettings) map[string]string {
	proxy := (&url.URL{
		Scheme: "http",
		User:   url.UserPassword(settings.token, settings.vault),
		Host:   settings.proxyHost,
	}).String()

	derived := map[string]string{}
	// Both cases, deliberately. curl reads http_proxy in lower case only, and
	// Claude Code takes the first of https_proxy, HTTPS_PROXY, http_proxy,
	// HTTP_PROXY that is set. Setting one case leaves one client unproxied.
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		derived[name] = proxy
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		derived[name] = brokerNoProxy
	}
	// One mounted file, every runtime's name for it. The proxy terminates TLS,
	// so a client that does not trust this root sees a certificate error rather
	// than a credential.
	for _, name := range []string{
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "DENO_CERT",
	} {
		derived[name] = brokerContainerCAPath
	}
	// Node's own proxy support, off by default and available from 22.21. It is
	// inert on an older runtime and on Claude Code, which proxies itself, but
	// it is what any other Node tooling in the image reads.
	derived["NODE_USE_ENV_PROXY"] = "1"
	return derived
}

// sandboxEnvironment builds the container environment from the allowlist,
// taking values from the process environment the supervisor already assembled.
// Returns the docker -e arguments, sorted so the argument vector is stable and
// testable.
//
// derived is added on top, and is not filtered by the allowlist. The allowlist
// exists to stop the Worker's own environment leaking into a container, and
// these values never came from there: they are computed for this container and
// meaningless outside it. A derived name overrides an allowlist name, because
// a Worker that has decided how a container reaches the network should not be
// overruled by whatever the same variable happened to hold on the host.
func sandboxEnvironment(source []string, derived map[string]string) []string {
	values := environmentValues(source)
	resolved := map[string]string{}
	for _, key := range sandboxEnvironmentAllowlist {
		value, present := values[key]
		if !present || value == "" {
			continue
		}
		resolved[key] = value
	}
	for key, value := range derived {
		if value == "" {
			continue
		}
		resolved[key] = value
	}
	allowed := make([]string, 0, len(resolved))
	for key, value := range resolved {
		allowed = append(allowed, key+"="+value)
	}
	sort.Strings(allowed)
	return allowed
}

// sandboxArguments builds the full docker run argument vector for one agent
// process.
//
// The worktree is mounted at the same absolute path inside the container as
// outside. The agent's prompt names host paths and the delivery evidence the
// Worker revalidates after the process stops is keyed on them, so a matching
// path removes a translation layer that would otherwise leak into INV-4.
func sandboxArguments(
	sandbox protocol.Sandbox,
	worktree string,
	updateSocket string,
	environment []string,
	runtimeExecutable string,
	runtimeArguments []string,
) ([]string, error) {
	arguments := []string{
		"run", "--rm", "--init",
		"--network", sandboxNetworkArgument(sandbox.Network),
		"--volume", worktree + ":" + worktree,
		"--workdir", worktree,
	}
	// The report channel is a unix socket on the Worker's filesystem. Its
	// directory is mounted so factory update can reach it from inside.
	if updateSocket != "" {
		directory := filepath.Dir(updateSocket)
		arguments = append(arguments, "--volume", directory+":"+directory)
	}
	if sandbox.CPU != "" {
		arguments = append(arguments, "--cpus", sandbox.CPU)
	}
	if sandbox.Memory != "" {
		arguments = append(arguments, "--memory", sandbox.Memory)
	}

	var derived map[string]string
	if sandbox.Network == protocol.NetworkBroker {
		settings, err := brokerSettingsFromEnvironment(environment)
		if err != nil {
			return nil, err
		}
		derived = brokerContainerEnvironment(settings)
		// Docker Desktop resolves this name on its own; a Linux engine does
		// not. The flag is harmless where it is redundant, and the alternative
		// is a posture that works on one operator's machine and not another's.
		arguments = append(arguments, "--add-host", brokerGatewayName+":host-gateway")
		// Read-only, and one file rather than its directory. The container
		// needs to trust this root, not to be able to replace it.
		arguments = append(arguments, "--volume", settings.caPath+":"+brokerContainerCAPath+":ro")
	}

	// The proxy URL carries the Worker's broker token, so it is visible in
	// this argument vector, in docker inspect, and in the container's own
	// environment. That is the design: the container has to present the
	// credential to reach the proxy. What it buys is that the token is scoped
	// to the broker, revocable in one place, and is not any upstream API key.
	// The third party keys stay on the host and are never in the container.
	for _, entry := range sandboxEnvironment(environment, derived) {
		arguments = append(arguments, "--env", entry)
	}
	arguments = append(arguments, sandbox.Image, runtimeExecutable)
	return append(arguments, runtimeArguments...), nil
}

// dockerAvailable reports whether this machine can run the sandbox at all. Used
// by the integration test to skip rather than fail on a machine without Docker.
func dockerAvailable() bool {
	_, err := os.Stat("/var/run/docker.sock")
	if err == nil {
		return true
	}
	return os.Getenv("DOCKER_HOST") != ""
}
