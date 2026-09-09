package protocol

// Stage kinds separate model work from mechanical work. An agent stage carries
// a prompt and spawns a runtime. A code stage runs a configured command. A
// delivery stage runs Factory's fixed push and pull-request operation. Neither
// mechanical kind invokes a model.
const (
	StageKindAgent    = "agent"
	StageKindCode     = "code"
	StageKindDelivery = "delivery"

	MaxStageCommandBytes = 4096
)

// ReadOnlyDisallowedTools is the tool list a read-only stage denies, in the
// form claude-code's --disallowedTools takes.
//
// Every tool that can change the checkout is named, and Bash is named with
// them because a shell is a way to reach all of the others. The list is
// deliberately not derived from an allowlist: naming what may not happen keeps
// a tool added to a future release from arriving silently permitted, which is
// the failure this flag exists to prevent.
//
// The flag itself is claude-code's, measured against `claude --help`, which
// prints it as --disallowedTools with a comma or space separated value.
const ReadOnlyDisallowedTools = "Edit,Write,MultiEdit,NotebookEdit,Bash"

// ReadOnlyRuntime reports whether a runtime can enforce a read-only stage.
// Only claude-code has a tool-denial flag, so a read-only stage on any other
// runtime is a guarantee Factory cannot keep and is refused rather than
// downgraded to a request in the prompt.
func ReadOnlyRuntime(runtime string) bool {
	return runtime == RuntimeClaudeCode
}

// Network postures for a sandboxed execution profile. The design names three;
// broker is a fourth, added by Phase 7. allowlist needs an egress proxy that
// restricts egress to a host list and is rejected at validation rather than
// silently treated as open.
//
// broker is not that egress filter. It is bridge networking plus a route to
// the credential broker on the Worker's host, so an agent reaches third party
// APIs without holding their keys. Egress is still unrestricted, which is why
// it is a separate posture from allowlist rather than an implementation of it.
const (
	NetworkNone      = "none"
	NetworkAllowlist = "allowlist"
	NetworkOpen      = "open"
	NetworkBroker    = "broker"
)

// StageKind resolves the stored value. Stages frozen before stage kinds
// existed carry an empty string and must keep behaving as agent stages.
func StageKind(value string) string {
	if value == "" {
		return StageKindAgent
	}
	return value
}

func SupportedStageKind(value string) bool {
	kind := StageKind(value)
	return kind == StageKindAgent || kind == StageKindCode || kind == StageKindDelivery
}

func IsCodeStage(value string) bool     { return StageKind(value) == StageKindCode }
func IsDeliveryStage(value string) bool { return StageKind(value) == StageKindDelivery }

func SupportedNetworkPosture(value string) bool {
	return value == NetworkNone || value == NetworkAllowlist ||
		value == NetworkOpen || value == NetworkBroker
}

// ImplementedNetworkPosture is the narrower question the fork can answer today.
func ImplementedNetworkPosture(value string) bool {
	return value == NetworkNone || value == NetworkOpen || value == NetworkBroker
}

// Sandbox is the frozen container posture for one Run. It is copied onto the
// execution snapshot at admission, so a profile edited mid Run cannot change
// the posture an already dispatched attempt executes under.
type Sandbox struct {
	Image   string `json:"image"`
	Network string `json:"network"`
	CPU     string `json:"cpu,omitempty"`
	Memory  string `json:"memory,omitempty"`
}
