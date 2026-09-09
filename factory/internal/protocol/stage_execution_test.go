package protocol

import "testing"

func TestSupportedEffort(t *testing.T) {
	for _, value := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !SupportedEffort(value) {
			t.Fatalf("effort %q should be supported", value)
		}
	}
	for _, value := range []string{"", "LOW", "extreme", "medium ", "1"} {
		if SupportedEffort(value) {
			t.Fatalf("effort %q should not be supported", value)
		}
	}
}

func TestSupportedModel(t *testing.T) {
	for _, value := range []string{"opus", "sonnet", "haiku", "fable"} {
		if !SupportedModel(RuntimeClaudeCode, value) {
			t.Fatalf("model %q should be supported for claude-code", value)
		}
	}
	if SupportedModel(RuntimeClaudeCode, "gpt-5") {
		t.Fatal("an unknown alias must not be supported")
	}
	// Only claude-code has a curated list. The other runtimes are not
	// installed on the host, so offering aliases for them would be a guess.
	if SupportedModel(RuntimeCodex, "opus") {
		t.Fatal("codex has no curated model list yet")
	}
}

func TestResolveStageExecutionPrecedence(t *testing.T) {
	defaults := StageDefaults{Model: "sonnet", Effort: "medium"}
	stage := StageExecution{Model: "opus", Effort: "high"}
	override := StageExecution{Model: "haiku", Effort: "low"}

	for _, testCase := range []struct {
		name       string
		override   StageExecution
		stage      StageExecution
		defaults   StageDefaults
		wantModel  string
		wantEffort string
	}{
		{"override wins", override, stage, defaults, "haiku", "low"},
		{"stage beats defaults", StageExecution{}, stage, defaults, "opus", "high"},
		{"defaults are the floor", StageExecution{}, StageExecution{}, defaults, "sonnet", "medium"},
		{"all empty stays empty", StageExecution{}, StageExecution{}, StageDefaults{}, "", ""},
		{"fields resolve independently",
			StageExecution{Model: "haiku"}, StageExecution{Effort: "high"}, defaults, "haiku", "high"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := ResolveStageExecution(testCase.override, testCase.stage, testCase.defaults)
			if got.Model != testCase.wantModel || got.Effort != testCase.wantEffort {
				t.Fatalf("got %+v, want model %q effort %q", got, testCase.wantModel, testCase.wantEffort)
			}
		})
	}
}
