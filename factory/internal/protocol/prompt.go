package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	MaxResolvedPromptBytes = 64 << 10
	MaxAgentPromptBytes    = 72 << 10
	MaxAgentBranchBytes    = 1 << 10
	// Stage handoffs carry concise verification or review evidence, not raw
	// command logs. Keeping them small prevents a noisy check from consuming
	// the next agent's context window.
	MaxStageHandoffBytes = 8 << 10
)

// AttributionPolicy is the one Factory-owned wording of the rule. It exists as
// a single constant because three separately worded copies had already grown
// across the prompt paths, and a policy stated three ways is three policies.
//
// FormatAgentPrompt is the single funnel every model-facing prompt passes
// through on every runtime, so applying it there covers each stage of each
// pipeline, resumed continuations included, without a per-path copy.
//
// This is prompt text and nothing more. Factory adds no commit hook, rewrites
// no commit, and rejects no delivery over it.
const AttributionPolicy = "Do not add “Generated with Claude Code”, " +
	"“Co-Authored-By: Claude”, or equivalent AI attribution trailers to commits, " +
	"pull requests, source files, documentation, or generated artifacts."

const AgentUpdatePromptContract = `Factory update contract:
This Work is unfinished until you call factory update. Use status running for useful progress only. Before exiting, report exactly one outcome: ready, needs-input, failed, or no-change. Ready requires --pr with the GitHub pull request URL. Needs-input ends this Attempt and requires a clean worktree with all changed work committed and pushed to the immutable Factory publish branch. Always include a concise non-empty --message.`

func ResolveTaskSchedulePrompt(prompt string, scheduledAt time.Time, cron, timezone string) (string, error) {
	occurrence, err := json.Marshal(struct {
		Type        string    `json:"type"`
		ScheduledAt time.Time `json:"scheduled_at"`
		Cron        string    `json:"cron"`
		Timezone    string    `json:"timezone"`
	}{"schedule", scheduledAt.UTC(), cron, timezone})
	if err != nil {
		return "", err
	}
	return prompt +
		"\n\nSchedule instruction:\n\n" +
		"Execute this Task for the scheduled occurrence. There is no provider item to revalidate." +
		"\n\nTrusted schedule occurrence:\n\n" + string(occurrence), nil
}

// StageReportContract asks for the bounded report Factory parses into the
// Outcome view. It is stated last in the wrapper, after the untrusted task
// text, so a task prompt cannot displace it.
//
// It asks only for what Factory can use. Anything an agent writes here is its
// own claim and is labelled agent-reported wherever it is shown, so the
// contract does not pretend to make the agent authoritative.
const StageReportContract = `End your result with this exact block, and keep it short:

Changes:
- <up to five bullets>

Verification:
- <command or check> - passed|failed|not-run

Risk:
- <none, or one concise caveat>

List a check under Verification only if you actually ran it. Do not report a test count Factory cannot see; name the command instead.`

// ReadOnlyAgentPosture replaces the "complete and verify" sentence on a
// read-only stage. The runtime is already denying the tools, so this only
// tells the agent why an edit would fail rather than asking it not to try.
const ReadOnlyAgentPosture = "This stage is read only: the runtime denies every tool that edits files or runs commands, so read what you need and put the whole answer in your result."

func FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return formatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt, false)
}

// FormatReadOnlyAgentPrompt is the wrapper a read-only stage gets.
//
// It differs from FormatAgentPrompt in one way that matters: it does not
// append StageReportContract. That contract asks the agent to end its result
// with a Changes and Verification block, and a read-only stage has no changes
// and ran no commands, so the block would be a fabricated report. It would
// also destroy the result: a read-only stage is how Factory runs a pass whose
// whole output is a machine-readable document, and appending prose after it
// leaves the caller with something that no longer parses.
func FormatReadOnlyAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt string) string {
	return formatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt, true)
}

func formatAgentPrompt(
	title, repository, workingBranch, targetBaseBranch, resolvedPrompt string, readOnly bool,
) string {
	posture := "Complete and verify the Session before returning a concise result."
	tail := "\n\n" + StageReportContract
	if readOnly {
		posture, tail = ReadOnlyAgentPosture, ""
	}
	return "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. " + AttributionPolicy +
		" " + posture + "\n\n" +
		"Task: " + title + "\n" +
		"Repository: " + repository + "\n" +
		"Working branch: " + workingBranch + "\n" +
		"Target base branch: " + targetBaseBranch + "\n\n" +
		resolvedPrompt +
		tail
}

func AgentPromptFits(title, repository, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentPrompt(title, repository, maxBranch, maxBranch, resolvedPrompt))) <= MaxAgentPromptBytes
}

func FormatAgentUpdatePrompt(
	title, repository, workingBranch, targetBaseBranch, publishBranch, resolvedPrompt string,
) string {
	return FormatAgentPrompt(title, repository, workingBranch, targetBaseBranch, resolvedPrompt) +
		"\n\nFactory publish branch: " + publishBranch +
		"\n\n" + AgentUpdatePromptContract
}

func AgentUpdatePromptFits(title, repository, publishBranch, resolvedPrompt string) bool {
	maxBranch := strings.Repeat("x", MaxAgentBranchBytes)
	return len([]byte(FormatAgentUpdatePrompt(
		title, repository, maxBranch, maxBranch, publishBranch, resolvedPrompt,
	))) <= MaxAgentPromptBytes
}
