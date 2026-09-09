package worker

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// mustSandboxArguments is the non-broker path, where the only error
// sandboxArguments can return is a broker configuration one.
func mustSandboxArguments(
	t *testing.T,
	sandbox protocol.Sandbox,
	worktree string,
	updateSocket string,
	environment []string,
	runtimeExecutable string,
	runtimeArguments []string,
) []string {
	t.Helper()
	arguments, err := sandboxArguments(
		sandbox, worktree, updateSocket, environment, runtimeExecutable, runtimeArguments,
	)
	if err != nil {
		t.Fatalf("sandboxArguments: %v", err)
	}
	return arguments
}

// brokerWorkerEnvironment is a Worker started under vault-run, with a real CA
// file on disk because sandboxArguments stats it.
func brokerWorkerEnvironment(t *testing.T, extra ...string) []string {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "mitm-ca.pem")
	if err := os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return append([]string{
		"PATH=/usr/bin",
		"AGENT_VAULT_TOKEN=broker-token",
		"AGENT_VAULT_VAULT=chao",
		"FACTORY_BROKER_CA=" + ca,
	}, extra...)
}

func TestSandboxNetworkPostureMapsOntoDocker(t *testing.T) {
	cases := map[string]string{
		protocol.NetworkNone: "none",
		protocol.NetworkOpen: "bridge",
		// broker is bridge networking. What makes it broker rather than open
		// is the proxy variables and the CA mount, not the network mode.
		protocol.NetworkBroker: "bridge",
		// allowlist never reaches the Worker, because the control plane
		// rejects it at profile validation. If it ever did, the safe reading
		// is the closed one, not the open one.
		protocol.NetworkAllowlist:  "none",
		"":                         "none",
		"something-invented-later": "none",
	}
	for posture, want := range cases {
		if got := sandboxNetworkArgument(posture); got != want {
			t.Fatalf("sandboxNetworkArgument(%q) = %q, want %q", posture, got, want)
		}
	}
}

// INV-6: no agent process runs with the operator credential in its
// environment. Inside a container the environment is built from an allowlist,
// so anything not named is absent by construction.
func TestSandboxEnvironmentDropsEverythingOutsideTheAllowlist(t *testing.T) {
	source := []string{
		"FACTORY_UPDATE_TOKEN=report-token",
		"FACTORY_ATTEMPT_ID=attempt-1",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-token",
		"PATH=/usr/bin",
		"HOME=/home/factory",
		// The things that must not cross the boundary.
		"FACTORY_WORKER_ENROLLMENT_TOKEN=operator-secret",
		"AWS_SECRET_ACCESS_KEY=operator-secret",
		"OP_SERVICE_ACCOUNT_TOKEN=operator-secret",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		// An empty value is not worth passing.
		"TZ=",
	}
	environment := sandboxEnvironment(source, nil)
	for _, entry := range environment {
		if strings.Contains(entry, "operator-secret") {
			t.Fatalf("environment carries an operator credential: %q", entry)
		}
	}
	for _, unwanted := range []string{"SSH_AUTH_SOCK", "TZ"} {
		for _, entry := range environment {
			if strings.HasPrefix(entry, unwanted+"=") {
				t.Fatalf("environment carries %s: %q", unwanted, entry)
			}
		}
	}
	for _, required := range []string{
		"FACTORY_UPDATE_TOKEN=report-token",
		"FACTORY_ATTEMPT_ID=attempt-1",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-token",
		"PATH=/usr/bin",
		"HOME=/home/factory",
	} {
		if !slices.Contains(environment, required) {
			t.Fatalf("environment is missing %q: %v", required, environment)
		}
	}
	if !slices.IsSorted(environment) {
		t.Fatalf("environment is not sorted, so the argument vector is unstable: %v", environment)
	}
}

func TestSandboxArgumentsMountTheWorktreeAtTheSamePath(t *testing.T) {
	arguments := mustSandboxArguments(t,
		protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkNone, CPU: "2", Memory: "4g"},
		"/Users/alice/.factory/workers/worker/worktrees/attempt-1",
		"/Users/alice/.factory/sockets/attempt-1.sock",
		[]string{"PATH=/usr/bin", "FACTORY_UPDATE_TOKEN=report-token"},
		"/usr/local/bin/claude",
		[]string{"--print", "--output-format", "stream-json"},
	)
	joined := strings.Join(arguments, " ")

	worktree := "/Users/alice/.factory/workers/worker/worktrees/attempt-1"
	if !strings.Contains(joined, "--volume "+worktree+":"+worktree) {
		t.Fatalf("worktree is not mounted at its own path: %v", arguments)
	}
	if !strings.Contains(joined, "--workdir "+worktree) {
		t.Fatalf("workdir is not the worktree: %v", arguments)
	}
	socketDirectory := filepath.Dir("/Users/alice/.factory/sockets/attempt-1.sock")
	if !strings.Contains(joined, "--volume "+socketDirectory+":"+socketDirectory) {
		t.Fatalf("the report socket directory is not mounted, so factory update cannot reach the Worker: %v", arguments)
	}
	if !strings.Contains(joined, "--network none") {
		t.Fatalf("network posture is missing: %v", arguments)
	}
	if !strings.Contains(joined, "--cpus 2") || !strings.Contains(joined, "--memory 4g") {
		t.Fatalf("resource limits are missing: %v", arguments)
	}

	// The image must come last before the runtime, and the runtime arguments
	// must survive in order, or the agent is invoked differently inside the
	// container than outside it.
	image := slices.Index(arguments, "factory/agent:1")
	if image < 0 || image != len(arguments)-5 {
		t.Fatalf("image is not immediately before the runtime command: %v", arguments)
	}
	if got := arguments[image+1:]; !slices.Equal(got,
		[]string{"/usr/local/bin/claude", "--print", "--output-format", "stream-json"}) {
		t.Fatalf("runtime command = %v", got)
	}
}

func TestSandboxArgumentsOmitUnsetLimitsAndSocket(t *testing.T) {
	arguments := mustSandboxArguments(t,
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkOpen},
		"/w", "", nil, "/bin/sh", []string{"-c", "true"},
	)
	joined := strings.Join(arguments, " ")
	if strings.Contains(joined, "--cpus") || strings.Contains(joined, "--memory") {
		t.Fatalf("unset limits must not appear: %v", arguments)
	}
	if strings.Count(joined, "--volume") != 1 {
		t.Fatalf("only the worktree should be mounted: %v", arguments)
	}
	if !strings.Contains(joined, "--network bridge") {
		t.Fatalf("open posture is missing: %v", arguments)
	}
}

// AC-7 and INV-6 against real Docker. The assertion is not that the argument
// vector looks right, it is that a container built this way cannot reach the
// network.
func TestSandboxNetworkNoneCannotReachTheNetwork(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available on this machine")
	}
	if _, err := exec.LookPath(dockerExecutable); err != nil {
		t.Skip("docker client is not on PATH")
	}
	worktree := t.TempDir()

	confined := mustSandboxArguments(t,
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		"/bin/sh", []string{"-c", "wget -q -T 5 -O - https://example.com"},
	)
	output, err := exec.Command(dockerExecutable, confined...).CombinedOutput()
	if err == nil {
		t.Fatalf("AC-7 violated: a network=none container reached the network: %s", output)
	}
	t.Logf("network=none container failed as required: %v: %s", err, strings.TrimSpace(string(output)))

	// The negative half. If the image simply cannot run, the assertion above
	// would pass for the wrong reason.
	reachable := mustSandboxArguments(t,
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "", []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		"/bin/sh", []string{"-c", "echo container-ran"},
	)
	output, err = exec.Command(dockerExecutable, reachable...).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "container-ran") {
		t.Fatalf("the confined container could not run at all, so the egress assertion proves nothing: %v: %s", err, output)
	}
}

// The environment the container actually receives, measured rather than
// inferred from the argument vector.
func TestSandboxContainerEnvironmentExcludesHostSecrets(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available on this machine")
	}
	if _, err := exec.LookPath(dockerExecutable); err != nil {
		t.Skip("docker client is not on PATH")
	}
	worktree := t.TempDir()
	arguments := mustSandboxArguments(t,
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone},
		worktree, "",
		[]string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"FACTORY_UPDATE_TOKEN=report-token",
			"AWS_SECRET_ACCESS_KEY=operator-secret",
		},
		"/bin/sh", []string{"-c", "env"},
	)
	output, err := exec.Command(dockerExecutable, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v: %s", err, output)
	}
	if strings.Contains(string(output), "operator-secret") {
		t.Fatalf("INV-6 violated: the container environment carries a host secret:\n%s", output)
	}
	if !strings.Contains(string(output), "FACTORY_UPDATE_TOKEN=report-token") {
		t.Fatalf("the report channel did not reach the container:\n%s", output)
	}
}

func TestDockerAvailableReadsTheEnvironment(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		t.Skip("this machine has a docker socket, so the negative case is not observable")
	}
	t.Setenv("DOCKER_HOST", "")
	if dockerAvailable() {
		t.Fatal("dockerAvailable must be false with no socket and no DOCKER_HOST")
	}
	t.Setenv("DOCKER_HOST", "unix:///somewhere/docker.sock")
	if !dockerAvailable() {
		t.Fatal("dockerAvailable must honour DOCKER_HOST")
	}
}

// The broker posture's whole point: the container is handed a route to a
// credential, never the credential the route eventually presents upstream.
func TestSandboxBrokerPostureGivesAProxyRouteAndACA(t *testing.T) {
	worktree := t.TempDir()
	environment := brokerWorkerEnvironment(t,
		"FACTORY_UPDATE_TOKEN=report-token",
		"AWS_SECRET_ACCESS_KEY=operator-secret",
	)
	arguments := mustSandboxArguments(t,
		protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkBroker},
		worktree, "", environment, "/bin/sh", []string{"-c", "true"},
	)
	joined := strings.Join(arguments, " ")

	if !strings.Contains(joined, "--network bridge") {
		t.Fatalf("broker posture is not bridge networking: %v", arguments)
	}
	if !strings.Contains(joined, "--add-host host.docker.internal:host-gateway") {
		t.Fatalf("the container has no route to the Worker's host: %v", arguments)
	}
	ca := ""
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, "FACTORY_BROKER_CA="); found {
			ca = value
		}
	}
	if !strings.Contains(joined, "--volume "+ca+":/etc/factory/broker-ca.pem:ro") {
		t.Fatalf("the proxy CA is not mounted read-only at the fixed path: %v", arguments)
	}

	proxy := "http://broker-token:chao@host.docker.internal:14322"
	// Both cases. curl reads http_proxy in lower case only, so setting one
	// case leaves one client in the image talking to the internet directly.
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if !slices.Contains(arguments, name+"="+proxy) {
			t.Fatalf("%s is not set to the broker proxy: %v", name, arguments)
		}
	}
	for _, name := range []string{
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE", "GIT_SSL_CAINFO", "DENO_CERT",
	} {
		if !slices.Contains(arguments, name+"=/etc/factory/broker-ca.pem") {
			t.Fatalf("%s does not point at the mounted CA: %v", name, arguments)
		}
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if !slices.Contains(arguments, name+"=localhost,127.0.0.1") {
			t.Fatalf("%s is wrong: %v", name, arguments)
		}
	}
	// NO_PROXY naming the gateway would route every request around the broker
	// and straight out, which is the failure this posture exists to prevent.
	for _, entry := range arguments {
		if strings.HasPrefix(strings.ToUpper(entry), "NO_PROXY=") &&
			strings.Contains(entry, "host.docker.internal") {
			t.Fatalf("NO_PROXY bypasses the broker: %q", entry)
		}
	}

	// INV-6 still holds, and the broker token is not a variable of its own.
	if strings.Contains(joined, "operator-secret") {
		t.Fatalf("INV-6 violated under the broker posture: %v", arguments)
	}
	for _, name := range []string{"AGENT_VAULT_TOKEN", "AGENT_VAULT_VAULT", "FACTORY_BROKER_CA"} {
		for _, entry := range arguments {
			if strings.HasPrefix(entry, name+"=") {
				t.Fatalf("the Worker's own broker configuration leaked into the container: %q", entry)
			}
		}
	}
}

// None of this appears under any other posture. A run that did not ask for the
// broker must not quietly acquire a credential route.
func TestSandboxNonBrokerPosturesGetNoProxy(t *testing.T) {
	for _, posture := range []string{protocol.NetworkNone, protocol.NetworkOpen} {
		arguments := mustSandboxArguments(t,
			protocol.Sandbox{Image: "alpine:3", Network: posture},
			t.TempDir(), "", brokerWorkerEnvironment(t), "/bin/sh", []string{"-c", "true"},
		)
		joined := strings.Join(arguments, " ")
		for _, forbidden := range []string{"PROXY", "proxy", "broker-ca.pem", "--add-host"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("posture %q leaked broker wiring (%q): %v", posture, forbidden, arguments)
			}
		}
	}
}

// A Worker that was not started under vault-run cannot honour the posture. It
// must say so at the boundary rather than start an agent that will fail later
// with an error about someone else's API.
func TestSandboxBrokerPostureRefusesWithoutWorkerCredentials(t *testing.T) {
	full := brokerWorkerEnvironment(t)
	ca := ""
	for _, entry := range full {
		if value, found := strings.CutPrefix(entry, "FACTORY_BROKER_CA="); found {
			ca = value
		}
	}
	cases := map[string][]string{
		"AGENT_VAULT_TOKEN": {"AGENT_VAULT_VAULT=chao", "FACTORY_BROKER_CA=" + ca},
		"AGENT_VAULT_VAULT": {"AGENT_VAULT_TOKEN=broker-token", "FACTORY_BROKER_CA=" + ca},
		// No CA variable and no HOME to derive one from. With a HOME this
		// resolves to agent-vault's install path instead, which the default
		// test below covers.
		"FACTORY_BROKER_CA": {"AGENT_VAULT_TOKEN=broker-token", "AGENT_VAULT_VAULT=chao"},
	}
	for missing, environment := range cases {
		_, err := sandboxArguments(
			protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkBroker},
			t.TempDir(), "", environment, "/bin/sh", []string{"-c", "true"},
		)
		if err == nil {
			t.Fatalf("a broker container started with no %s", missing)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("the error does not name the missing variable %s: %v", missing, err)
		}
	}
}

// Docker creates a directory at a bind source that does not exist, which would
// give the container an empty directory where its trust root belongs and fail
// every TLS handshake with a certificate error that names nothing useful.
func TestSandboxBrokerPostureRejectsAnUnusableCA(t *testing.T) {
	base := []string{"AGENT_VAULT_TOKEN=broker-token", "AGENT_VAULT_VAULT=chao"}
	cases := map[string]string{
		"missing":    filepath.Join(t.TempDir(), "absent.pem"),
		"relative":   "broker/mitm-ca.pem",
		"adirectory": t.TempDir(),
	}
	for name, path := range cases {
		_, err := sandboxArguments(
			protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkBroker},
			t.TempDir(), "", append(base, "FACTORY_BROKER_CA="+path),
			"/bin/sh", []string{"-c", "true"},
		)
		if err == nil {
			t.Fatalf("a %s CA path was accepted: %q", name, path)
		}
	}
}

func TestSandboxBrokerProxyHostIsOverridableAndValidated(t *testing.T) {
	environment := brokerWorkerEnvironment(t, "FACTORY_BROKER_PROXY_HOST=broker.internal:9000")
	arguments := mustSandboxArguments(t,
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkBroker},
		t.TempDir(), "", environment, "/bin/sh", []string{"-c", "true"},
	)
	if !slices.Contains(arguments, "HTTPS_PROXY=http://broker-token:chao@broker.internal:9000") {
		t.Fatalf("the proxy host override was ignored: %v", arguments)
	}
	// The mapping is still sent. A Linux engine needs it whenever the operator
	// pointed the posture back at the host gateway under another name, and it
	// costs nothing when the proxy is somewhere else entirely.
	if !strings.Contains(strings.Join(arguments, " "), "--add-host host.docker.internal:host-gateway") {
		t.Fatalf("the host gateway mapping was dropped: %v", arguments)
	}

	_, err := sandboxArguments(
		protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkBroker},
		t.TempDir(), "", brokerWorkerEnvironment(t, "FACTORY_BROKER_PROXY_HOST=no-port-here"),
		"/bin/sh", []string{"-c", "true"},
	)
	if err == nil {
		t.Fatal("a proxy host with no port was accepted")
	}
}

// A token with URL metacharacters must survive as itself. A token that arrived
// at the proxy mangled would look like a revoked credential, and the operator
// would go looking at the broker instead of here.
func TestSandboxBrokerProxyURLEscapesTheCredential(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	settings, err := brokerSettingsFromEnvironment([]string{
		"AGENT_VAULT_TOKEN=to:ken/with@symbols",
		"AGENT_VAULT_VAULT=cha o",
		"FACTORY_BROKER_CA=" + ca,
	})
	if err != nil {
		t.Fatalf("brokerSettingsFromEnvironment: %v", err)
	}
	proxy := brokerContainerEnvironment(settings)["HTTPS_PROXY"]
	parsed, err := url.Parse(proxy)
	if err != nil {
		t.Fatalf("the proxy URL does not parse: %q: %v", proxy, err)
	}
	if parsed.User.Username() != "to:ken/with@symbols" {
		t.Fatalf("the token did not survive the round trip: %q", parsed.User.Username())
	}
	password, _ := parsed.User.Password()
	if password != "cha o" {
		t.Fatalf("the vault name did not survive the round trip: %q", password)
	}
	if parsed.Host != "host.docker.internal:14322" {
		t.Fatalf("proxy host = %q", parsed.Host)
	}
}

// The Phase 7 acceptance test, against the real broker. An agent in a broker
// container authenticates to a third party API holding no key for it.
//
// It skips unless this process itself holds a broker credential, which means
// it runs under "vault-run --device factory-worker -- go test ./internal/worker"
// and nowhere else. That is deliberate: the assertion is about a live proxy on
// the Worker's host, and a version of it that passed without one would be
// asserting nothing.
func TestSandboxBrokerPostureReachesGitHubWithNoGitHubToken(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker is not available on this machine")
	}
	if _, err := exec.LookPath(dockerExecutable); err != nil {
		t.Skip("docker client is not on PATH")
	}
	if _, err := brokerSettingsFromEnvironment(os.Environ()); err != nil {
		t.Skipf("no broker credential in this process: %v", err)
	}

	// Everything except the credentials the broker exists to replace. If
	// GITHUB_TOKEN survived into the container the test would pass for the
	// wrong reason, so it is stripped here and asserted absent below.
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GITHUB_TOKEN" || key == "GH_TOKEN" {
			continue
		}
		environment = append(environment, entry)
	}

	sandbox := protocol.Sandbox{Image: "curlimages/curl:8.10.1", Network: protocol.NetworkBroker}
	arguments := mustSandboxArguments(t, sandbox, t.TempDir(), "", environment,
		"/bin/sh", []string{"-c", "env | grep -c GITHUB_TOKEN; " +
			`curl -s -w '\nstatus=%{http_code}\n' --max-time 25 https://api.github.com/user`},
	)
	for _, entry := range arguments {
		if strings.HasPrefix(entry, "GITHUB_TOKEN=") || strings.HasPrefix(entry, "GH_TOKEN=") {
			t.Fatalf("a GitHub credential reached the container, so the proof is void: %q", entry)
		}
	}

	output, err := exec.Command(dockerExecutable, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v: %s", err, output)
	}
	body := string(output)
	// grep -c prints 0 and exits 1, which the `;` swallows. The count is the
	// container's own answer about its environment, not ours about the argv.
	if !strings.HasPrefix(body, "0\n") {
		t.Fatalf("the container reported a GITHUB_TOKEN in its environment:\n%s", body)
	}
	if !strings.Contains(body, "status=200") {
		t.Fatalf("the broker did not authenticate the request:\n%s", body)
	}
	if !strings.Contains(body, `"login"`) {
		t.Fatalf("GitHub did not return an identity, so nothing was authenticated:\n%s", body)
	}
	t.Logf("authenticated to api.github.com through the broker holding no GitHub credential")

	// The negative control, and the revocation half of the acceptance
	// criteria. A credential the broker does not honour must not reach
	// GitHub, or the 200 above proves nothing about where the Authorization
	// header came from. A revoked token behaves as this one does: the proxy
	// refuses the CONNECT, so curl never opens the tunnel and reports no HTTP
	// status at all rather than a 407 on the request itself.
	revoked := make([]string, 0, len(environment))
	for _, entry := range environment {
		if key, _, _ := strings.Cut(entry, "="); key == "AGENT_VAULT_TOKEN" {
			entry = "AGENT_VAULT_TOKEN=this-token-was-revoked"
		}
		revoked = append(revoked, entry)
	}
	denied := mustSandboxArguments(t, sandbox, t.TempDir(), "", revoked,
		"/bin/sh", []string{"-c",
			`curl -s -w 'status=%{http_code}\n' --max-time 25 https://api.github.com/user; echo exit=$?`},
	)
	output, _ = exec.Command(dockerExecutable, denied...).CombinedOutput()
	body = string(output)
	if strings.Contains(body, "status=200") || strings.Contains(body, `"login"`) {
		t.Fatalf("a credential the broker does not honour still reached GitHub:\n%s", body)
	}
	if !strings.Contains(body, "status=000") {
		t.Fatalf("the proxy did not refuse the tunnel, so the failure is not the broker's:\n%s", body)
	}
	t.Logf("a revoked credential is refused at the proxy: %s", strings.TrimSpace(body))
}

// The documented launch command is "vault-run --device factory-worker --
// factory worker", and vault-run exports no CA path. The Worker finds
// agent-vault's own root under HOME, so that command is sufficient on its own.
func TestSandboxBrokerCADefaultsToTheAgentVaultInstallPath(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, ".agent-vault", "ca")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ca := filepath.Join(installed, "ca.crt.pem")
	if err := os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	// Exactly what vault-run exports, plus HOME. No FACTORY_BROKER_CA.
	environment := []string{
		"HOME=" + home,
		"AGENT_VAULT_TOKEN=broker-token",
		"AGENT_VAULT_VAULT=chao",
		"AGENT_VAULT_ADDR=http://127.0.0.1:14321",
	}
	settings, err := brokerSettingsFromEnvironment(environment)
	if err != nil {
		t.Fatalf("vault-run's own environment is not enough to start a broker container: %v", err)
	}
	if settings.caPath != ca {
		t.Fatalf("caPath = %q, want the agent-vault install path %q", settings.caPath, ca)
	}
	// An explicit variable still wins, for a non-default install.
	override := filepath.Join(t.TempDir(), "elsewhere.pem")
	if err := os.WriteFile(override, []byte("x"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	settings, err = brokerSettingsFromEnvironment(append(environment, "FACTORY_BROKER_CA="+override))
	if err != nil {
		t.Fatal(err)
	}
	if settings.caPath != override {
		t.Fatalf("the explicit CA path was ignored: %q", settings.caPath)
	}

	// A HOME with no agent-vault installed is still an error, not a silent
	// fallback to an empty bind source.
	if _, err := brokerSettingsFromEnvironment([]string{
		"HOME=" + t.TempDir(), "AGENT_VAULT_TOKEN=broker-token", "AGENT_VAULT_VAULT=chao",
	}); err == nil {
		t.Fatal("a HOME with no agent-vault CA was accepted")
	}
}
