package controlplane

import (
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func costOf(value float64) *float64 { return &value }

// TestSummariseVerificationCountsOnlyWhatFactoryRan is the honesty rule for
// section D: a code stage's exit status is Factory's own evidence, and an
// agent stage's prose is not evidence at all until something parses it.
func TestSummariseVerificationCountsOnlyWhatFactoryRan(t *testing.T) {
	summary := summariseVerification([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded,
			Result: "Ran 1,248 tests, all passing."},
		{Position: 1, Name: "Test", Kind: "code", Command: "go test ./internal/controlplane",
			State: protocol.StageSucceeded},
		{Position: 2, Name: "Vet", Kind: "code", Command: "go vet ./...", State: protocol.StageSucceeded},
		{Position: 3, Name: "Lint", Kind: "code", Command: "npm run lint",
			State: protocol.StageFailed, Error: "exit status 1"},
		{Position: 4, Name: "Deliver", Kind: "code", Command: "gh pr create", State: protocol.StagePending},
	})
	if summary.RecordedChecks != 4 {
		t.Fatalf("recorded %d checks, want the 4 code stages", summary.RecordedChecks)
	}
	if summary.Passed != 2 || summary.Failed != 1 || summary.NotRun != 1 {
		t.Fatalf("summary = %+v, want 2 passed, 1 failed, 1 not run", summary)
	}
	// The agent claimed 1,248 tests. Factory ran four commands and says four.
	for _, item := range summary.Items {
		if item.Source != protocol.VerificationSourceCodeStage {
			t.Fatalf("check %q has source %q, want code-stage", item.Name, item.Source)
		}
		if strings.Contains(item.Name, "1,248") || strings.Contains(item.Detail, "1,248") {
			t.Fatalf("an agent's test count leaked into verification: %+v", item)
		}
	}
	if summary.Items[0].Name != "go test ./internal/controlplane" {
		t.Fatalf("check name = %q, want the command Factory ran", summary.Items[0].Name)
	}
	if summary.Items[2].State != protocol.VerificationFailed || summary.Items[2].Detail == "" {
		t.Fatalf("failed check lost its reason: %+v", summary.Items[2])
	}
}

// A pipeline with no code stage records nothing rather than inferring a pass
// from the agent having finished.
func TestSummariseVerificationRecordsNothingWithoutCodeStages(t *testing.T) {
	summary := summariseVerification([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded,
			Result: "Everything passes."},
	})
	if summary.RecordedChecks != 0 || len(summary.Items) != 0 {
		t.Fatalf("summary = %+v, want no recorded checks", summary)
	}
}

func TestDeriveStageHandoffsDistinguishesDeliveredFromNeverSent(t *testing.T) {
	handoffs := deriveStageHandoffs([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded,
			Result: "Guarded the claim lease."},
		{Position: 1, Name: "Review", Kind: "agent", State: protocol.StageFailed,
			Result: "Found an unreleased lease.", ReviewVerdict: protocol.ReviewVerdictRequestChanges},
		{Position: 2, Name: "Deliver", Kind: "delivery", State: protocol.StagePending},
	})
	if len(handoffs) != 2 {
		t.Fatalf("derived %d handoffs, want one per edge", len(handoffs))
	}
	if !handoffs[0].Delivered || handoffs[0].Summary != "Guarded the claim lease." {
		t.Fatalf("succeeded stage did not deliver its result: %+v", handoffs[0])
	}
	if handoffs[0].Kind != "agent-result" {
		t.Fatalf("handoff kind = %q, want agent-result", handoffs[0].Kind)
	}
	// A failed predecessor still has evidence worth reading, but the successor
	// did not receive a completed hand-off and the edge must not claim it did.
	if handoffs[1].Delivered {
		t.Fatal("a failed stage was reported as having delivered its evidence")
	}
	if handoffs[1].Kind != "review-verdict" {
		t.Fatalf("handoff kind = %q, want review-verdict", handoffs[1].Kind)
	}
	if handoffs[1].FromState != protocol.StageFailed {
		t.Fatalf("handoff carried state %q, want the real predecessor state", handoffs[1].FromState)
	}
}

func TestDeriveStageHandoffsBoundsTheSummary(t *testing.T) {
	handoffs := deriveStageHandoffs([]protocol.StageRun{
		{Position: 0, Kind: "code", Command: "go test ./...", State: protocol.StageSucceeded,
			Result: strings.Repeat("x", protocol.MaxStageHandoffBytes*4)},
		{Position: 1, Kind: "agent", State: protocol.StagePending},
	})
	if len(handoffs[0].Summary) > protocol.MaxStageHandoffBytes/2 {
		t.Fatalf("handoff summary is %d bytes, over its bound", len(handoffs[0].Summary))
	}
	if !handoffs[0].Truncated {
		t.Fatal("a clipped summary was not reported as truncated")
	}
	if handoffs[0].Kind != "command-output" {
		t.Fatalf("handoff kind = %q, want command-output", handoffs[0].Kind)
	}
}

func TestDeriveStageHandoffsIgnoresASingleStagePipeline(t *testing.T) {
	if handoffs := deriveStageHandoffs([]protocol.StageRun{{Position: 0}}); handoffs != nil {
		t.Fatalf("a one-stage pipeline produced %d handoffs", len(handoffs))
	}
}

// TestSummariseWorkCostNeverInventsAZero is the money rule: a runtime that
// reported nothing produces no total, and a stage that ran a model and
// reported nothing is counted so a partial total can say it is partial.
func TestSummariseWorkCostNeverInventsAZero(t *testing.T) {
	unreported := summariseWorkCost([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded},
		{Position: 1, Name: "Test", Kind: "code", Command: "go test ./...", State: protocol.StageSucceeded},
	}, nil)
	if unreported.TotalUSD != nil {
		t.Fatalf("total = %v, want nil when nothing reported a cost", *unreported.TotalUSD)
	}
	// The agent stage reached a model and said nothing; the code stage never
	// reaches one, so only the first counts as unavailable.
	if unreported.UnavailableStages != 1 {
		t.Fatalf("unavailable stages = %d, want 1", unreported.UnavailableStages)
	}

	reported := summariseWorkCost([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded,
			CostUSD: costOf(0.12), Models: map[string]protocol.ModelUsage{
				"sonnet": {InputTokens: 100, OutputTokens: 20, CostUSD: 0.12},
			}},
		{Position: 1, Name: "Review", Kind: "agent", State: protocol.StageSucceeded,
			CostUSD: costOf(0.06), Models: map[string]protocol.ModelUsage{
				"sonnet": {InputTokens: 40, OutputTokens: 10, CostUSD: 0.06},
			}},
		{Position: 2, Name: "Test", Kind: "code", Command: "go test ./...", State: protocol.StageSucceeded},
	}, []protocol.Attempt{
		{AttemptNumber: 1, State: "failed", CostUSD: costOf(0.44)},
		{AttemptNumber: 2, State: "succeeded", CostUSD: costOf(0.18)},
	})
	// Summed over attempts, not stages: 0.44 + 0.18. See the retry test below
	// for why the stage rows cannot be the source.
	if reported.TotalUSD == nil || *reported.TotalUSD < 0.619 || *reported.TotalUSD > 0.621 {
		t.Fatalf("total = %v, want the sum over attempts", reported.TotalUSD)
	}
	if reported.UnavailableStages != 0 {
		t.Fatalf("unavailable stages = %d, want 0", reported.UnavailableStages)
	}
	if got := reported.ByModel["sonnet"]; got.CostUSD < 0.179 || got.InputTokens != 140 {
		t.Fatalf("per-model rollup = %+v", got)
	}
	// A retry is not free, and the failed attempt's spend stays visible.
	if len(reported.ByAttempt) != 2 || reported.ByAttempt[0].CostUSD == nil {
		t.Fatalf("attempt breakdown = %+v", reported.ByAttempt)
	}
	if *reported.ByAttempt[0].CostUSD != 0.44 {
		t.Fatalf("failed attempt cost = %v, want it preserved", *reported.ByAttempt[0].CostUSD)
	}
}

// TestSummariseVerificationSeparatesFactsFromClaims is the labelling rule: a
// code stage's exit status and an agent's report both appear, and an operator
// can always tell which is which.
func TestSummariseVerificationSeparatesFactsFromClaims(t *testing.T) {
	summary := summariseVerification([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded, Result: `Changes:
- Guarded the claim lease.

Verification:
- go build ./... — passed
- manual smoke of the board — not-run

Risk:
- None.`},
		{Position: 1, Name: "Test", Kind: "code", Command: "go test ./...", State: protocol.StageSucceeded},
	})
	if summary.RecordedChecks != 3 {
		t.Fatalf("recorded %d checks, want 3: %+v", summary.RecordedChecks, summary.Items)
	}
	if summary.Passed != 2 || summary.NotRun != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	// Factory's own evidence is listed before the evidence it is relaying.
	if summary.Items[0].Source != protocol.VerificationSourceCodeStage {
		t.Fatalf("first item = %+v, want the code stage", summary.Items[0])
	}
	if summary.Items[0].Name != "go test ./..." {
		t.Fatalf("code stage check = %q", summary.Items[0].Name)
	}
	agentReported := 0
	for _, item := range summary.Items[1:] {
		if item.Source != protocol.VerificationSourceAgentReported {
			t.Fatalf("item %+v should be labelled agent-reported", item)
		}
		agentReported++
	}
	if agentReported != 2 {
		t.Fatalf("counted %d agent-reported checks, want 2", agentReported)
	}
}

// An agent that ignores the contract costs a parsed row, not a wrong one.
func TestSummariseVerificationIgnoresUncontractedProse(t *testing.T) {
	summary := summariseVerification([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StageSucceeded,
			Result: "I ran the full suite and all 1,248 tests passed."},
	})
	if summary.RecordedChecks != 0 {
		t.Fatalf("free prose produced %d checks: %+v", summary.RecordedChecks, summary.Items)
	}
}

// TestSummariseWorkCostSurvivesARetry is the reason the total is summed over
// attempts. RetrySession sets every stage row's cost back to NULL, so a
// stage-derived total would report only the newest attempt while ByAttempt
// still showed what the earlier one spent, and would disagree with the
// Overview, which sums attempts.
func TestSummariseWorkCostSurvivesARetry(t *testing.T) {
	// The state after a retry: stages wiped, attempt history intact.
	cost := summariseWorkCost([]protocol.StageRun{
		{Position: 0, Name: "Implement", Kind: "agent", State: protocol.StagePending},
		{Position: 1, Name: "Review", Kind: "agent", State: protocol.StagePending},
	}, []protocol.Attempt{
		{AttemptNumber: 1, State: "failed", CostUSD: costOf(0.44)},
		{AttemptNumber: 2, State: "succeeded", CostUSD: costOf(0.18)},
	})
	if cost.TotalUSD == nil || *cost.TotalUSD < 0.619 || *cost.TotalUSD > 0.621 {
		t.Fatalf("total = %v, want the spend of both attempts to survive the retry", cost.TotalUSD)
	}
	// A total that omitted the failed attempt would tell an operator the retry
	// was free.
	if len(cost.ByAttempt) != 2 {
		t.Fatalf("attempt breakdown = %+v", cost.ByAttempt)
	}
	var attemptTotal float64
	for _, attempt := range cost.ByAttempt {
		if attempt.CostUSD != nil {
			attemptTotal += *attempt.CostUSD
		}
	}
	if attemptTotal != *cost.TotalUSD {
		t.Fatalf("total %v disagrees with the sum of its own attempt rows %v",
			*cost.TotalUSD, attemptTotal)
	}
}
