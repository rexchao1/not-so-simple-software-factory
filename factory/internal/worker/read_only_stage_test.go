package worker

import (
	"slices"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// The read-only guarantee is a runtime argument and nothing else, so this is
// the test that says whether it exists. The flag and its value are compared
// exactly: --disallowedTools is what `claude --help` prints, and a value that
// lost one tool would still look right at a glance.
func TestReadOnlyStageDeniesTheWritingTools(t *testing.T) {
	arguments, _, _, err := runtimeArguments(protocol.RuntimeClaudeCode, "/tmp/result",
		protocol.StageExecution{Model: "opus", Effort: "xhigh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(arguments, "--disallowedTools")
	if index < 0 {
		t.Fatalf("a read-only stage runs without --disallowedTools: %v", arguments)
	}
	if index == len(arguments)-1 {
		t.Fatalf("--disallowedTools was passed with no value: %v", arguments)
	}
	if got := arguments[index+1]; got != "Edit,Write,MultiEdit,NotebookEdit,Bash" {
		t.Fatalf("--disallowedTools = %q, want Edit,Write,MultiEdit,NotebookEdit,Bash", got)
	}
	// The rest of the command line is untouched by the flag.
	joined := strings.Join(arguments, " ")
	for _, wanted := range []string{"--model opus", "--effort xhigh", "--print"} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("arguments %q lost %q", joined, wanted)
		}
	}
}

// An ordinary stage has to keep every tool. A flag that leaked onto normal
// work would break every build in a way no test of the read-only path sees.
func TestOrdinaryStageDeniesNoTools(t *testing.T) {
	arguments, _, _, err := runtimeArguments(protocol.RuntimeClaudeCode, "/tmp/result",
		protocol.StageExecution{Model: "sonnet", Effort: "medium"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(arguments, "--disallowedTools") ||
		slices.Contains(arguments, "--disallowed-tools") {
		t.Fatalf("a stage that is not read only denies tools: %v", arguments)
	}
}

// Only claude-code has a tool-denial flag. A read-only stage on codex or pi
// has to fail rather than run with the guarantee quietly dropped.
func TestReadOnlyStageFailsOnARuntimeThatCannotDenyTools(t *testing.T) {
	for _, runtime := range []string{protocol.RuntimeCodex, protocol.RuntimePi} {
		t.Run(runtime, func(t *testing.T) {
			arguments, _, _, err := runtimeArguments(runtime, "/tmp/result",
				protocol.StageExecution{}, true)
			if err == nil {
				t.Fatalf("a read-only stage started on %s: %v", runtime, arguments)
			}
			if !strings.Contains(err.Error(), "claude-code") {
				t.Fatalf("error = %v, want it to name the runtime that can enforce this", err)
			}
		})
	}
}

// A read-only stage takes the read-only wrapper even when it is the final
// stage of an agent_update Run. The update contract asks for a pull request
// URL and a pushed branch, neither of which a stage that cannot write can
// produce, so the read-only posture wins over the contract.
func TestReadOnlyStagePromptDropsBothReportingContracts(t *testing.T) {
	claim := protocol.Claim{
		Session: protocol.ClaimedSession{
			TaskName: "Critique", OutcomeContract: protocol.OutcomeAgentUpdate,
			Target: protocol.WorkTarget{PublishBranch: "factory/work-1"},
		},
		Repository: protocol.Repository{RemoteIdentity: "github.com/example/repo"},
	}
	value := worktree{Branch: "factory/work-1", BaseBranch: "main"}
	stage := protocol.StageRun{Prompt: "Return JSON and nothing else.", ReadOnly: true}
	prompt := buildStagePrompt(claim, value, stage, true)
	if strings.Contains(prompt, "factory update") {
		t.Errorf("a read-only stage received the agent-update contract: %s", prompt)
	}
	if strings.Contains(prompt, protocol.StageReportContract) {
		t.Errorf("a read-only stage was asked for a Changes and Verification report: %s", prompt)
	}
	if !strings.HasSuffix(prompt, "Return JSON and nothing else.") {
		t.Errorf("a read-only prompt does not end on the stage's own instruction: %s", prompt)
	}
	stage.ReadOnly = false
	if writable := buildStagePrompt(claim, value, stage, true); !strings.Contains(writable, "factory update") {
		t.Errorf("an ordinary final stage lost its update contract: %s", writable)
	}
}
