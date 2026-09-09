package protocol

// Effort levels accepted by the claude-code runtime, measured against Claude
// Code 2.1.246 with `claude --help`.
//
// These are validated when a Pipeline or a draft override is saved, never when
// a run starts. The reason is measured, not stylistic: an unknown --effort
// value produces a warning on stderr and then runs at the default anyway, so a
// typo would silently downgrade every stage it touched with no evidence beyond
// a line in a Worker log. An unknown --model at least fails loudly, but it
// fails after the queue, the clone, and the lease. INV-12 puts both at the
// save.
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortXHigh  = "xhigh"
	EffortMax    = "max"
)

// claudeCodeModels is the curated alias list. Aliases rather than full model
// names, because an alias always points at the current model of that tier and
// so does not drift as models are released. This is the one place that changes
// when a new tier appears.
var claudeCodeModels = []string{"opus", "sonnet", "haiku", "fable"}

// StageExecution is how one stage is executed. Both fields are optional, and
// an empty field means "inherit from the next level down".
type StageExecution struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Empty reports whether this carries no instruction at all, in which case the
// Worker passes no argument. AC-11 depends on this.
func (e StageExecution) Empty() bool {
	return e.Model == "" && e.Effort == ""
}

// StageDefaults is the global floor of the precedence chain, stored as one row
// in factory_settings.
type StageDefaults struct {
	Model  string `json:"default_model"`
	Effort string `json:"default_effort"`
}

func SupportedEfforts() []string {
	return []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
}

func SupportedEffort(value string) bool {
	for _, supported := range SupportedEfforts() {
		if value == supported {
			return true
		}
	}
	return false
}

// SupportedModels returns the curated aliases for a runtime. Only claude-code
// has a list: codex and pi are not installed on this host, and publishing
// aliases for a runtime nobody can run would be a guess presented as a fact.
func SupportedModels(runtime string) []string {
	if runtime == RuntimeClaudeCode {
		return append([]string(nil), claudeCodeModels...)
	}
	return nil
}

func SupportedModel(runtime, value string) bool {
	for _, supported := range SupportedModels(runtime) {
		if value == supported {
			return true
		}
	}
	return false
}

// ResolveStageExecution applies the precedence chain. Most specific wins, and
// each field resolves independently so a draft can override the model while
// leaving effort to the Pipeline.
func ResolveStageExecution(override, stage StageExecution, defaults StageDefaults) StageExecution {
	return StageExecution{
		Model:  firstNonEmpty(override.Model, stage.Model, defaults.Model),
		Effort: firstNonEmpty(override.Effort, stage.Effort, defaults.Effort),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
