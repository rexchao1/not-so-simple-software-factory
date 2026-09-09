package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestFormatAgentPromptPreservesSafetyAndBranchContract(t *testing.T) {
	want := "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. " + AttributionPolicy +
		" Complete and verify the Session before returning a concise result.\n\n" +
		"Task: Fix the prompt\n" +
		"Repository: github.com/owainlewis/factory\n" +
		"Working branch: factory/123456789abc-abcdef123456\n" +
		"Target base branch: main\n\n" +
		"Keep the change focused." +
		"\n\n" + StageReportContract

	if got := FormatAgentPrompt(
		"Fix the prompt",
		"github.com/owainlewis/factory",
		"factory/123456789abc-abcdef123456",
		"main",
		"Keep the change focused.",
	); got != want {
		t.Fatalf("FormatAgentPrompt() = %q, want %q", got, want)
	}
}

func TestFormatAgentUpdatePromptNamesImmutablePublishBranch(t *testing.T) {
	const publishBranch = "factory/work-1111111111111111"
	prompt := FormatAgentUpdatePrompt(
		"Fix the prompt",
		"github.com/owainlewis/factory",
		"factory/123456789abc-abcdef123456",
		"main",
		publishBranch,
		"Keep the change focused.",
	)
	if !strings.Contains(prompt, "Working branch: factory/123456789abc-abcdef123456") ||
		!strings.Contains(prompt, "Factory publish branch: "+publishBranch) ||
		!strings.Contains(prompt, AgentUpdatePromptContract) {
		t.Fatalf("agent-update prompt = %q", prompt)
	}
	if !AgentUpdatePromptFits(
		"Fix the prompt", "github.com/owainlewis/factory", publishBranch, "Keep the change focused.",
	) {
		t.Fatal("bounded agent-update prompt rejected the exact publish branch")
	}
}

// TestAttributionPolicyReachesEveryFactoryOwnedPrompt is the acceptance
// criterion for the policy: it must appear in every prompt Factory itself
// builds, and it must appear as one wording rather than several.
//
// FormatAgentPrompt is asserted separately because it is the single funnel
// every model-facing prompt passes through on every runtime, which is what
// makes covering the other paths a consequence rather than a coincidence.
func TestAttributionPolicyReachesEveryFactoryOwnedPrompt(t *testing.T) {
	const branch = "factory/123456789abc-abcdef123456"
	scheduled, err := ResolveTaskSchedulePrompt("Do the work.", time.Unix(0, 0).UTC(), "0 9 * * 1", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	for name, prompt := range map[string]string{
		"agent prompt": FormatAgentPrompt(
			"Fix", "github.com/example/repo", branch, "main", "Do the work."),
		"agent update prompt": FormatAgentUpdatePrompt(
			"Fix", "github.com/example/repo", branch, "main", "factory/work-1", "Do the work."),
		"scheduled occurrence prompt": FormatAgentPrompt(
			"Fix", "github.com/example/repo", branch, "main", scheduled),
		"standard build procedure prompt": FormatAgentPrompt(
			"Fix", "github.com/example/repo", branch, "main", StandardBuildProcedurePrompt),
		"standard build procedure spec": StandardBuildProcedurePrompt,
	} {
		if !strings.Contains(prompt, AttributionPolicy) {
			t.Errorf("%s does not carry the attribution policy", name)
		}
	}
}

// One policy, stated once. Two near-identical sentences in one prompt read as
// two rules and waste the agent's context on the same instruction twice.
func TestAttributionPolicyIsStatedOnlyOnce(t *testing.T) {
	prompt := FormatAgentUpdatePrompt(
		"Fix", "github.com/example/repo", "factory/local", "main", "factory/work-1", "Do the work.")
	if count := strings.Count(prompt, AttributionPolicy); count != 1 {
		t.Fatalf("the attribution policy appears %d times in one prompt, want 1", count)
	}
	// The older hand-written variants must be gone, or a reword would leave
	// the stale copy behind.
	for _, stale := range []string{
		"Do not add AI attribution trailers such as",
		"or any AI attribution trailer to commits",
	} {
		if strings.Contains(prompt, stale) {
			t.Errorf("a superseded attribution wording survives: %q", stale)
		}
	}
}
