package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// WorkDetail assembles one Work item's full record: its brief, its stages and
// what each passed to the next, its normalised verification, and its cost.
func (s *Store) WorkDetail(ctx context.Context, id string) (protocol.WorkDetail, error) {
	work, err := s.Work(ctx, id)
	if err != nil {
		return protocol.WorkDetail{}, err
	}
	detail := protocol.WorkDetail{Work: work, RunID: work.RunID}

	var snapshotJSON, brief, source, assurance string
	var updatedAt int64
	err = s.db.QueryRowContext(ctx, `
		SELECT run.task_id, run.task_snapshot, run.orchestrator_brief, run.source,
		       run.assurance, session.updated_at
		FROM runs run
		JOIN sessions session ON session.run_id = run.id
		WHERE session.id = ?
	`, id).Scan(&detail.TaskID, &snapshotJSON, &brief, &source, &assurance, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	detail.Source, detail.Assurance = source, protocol.AssuranceMode(assurance)
	detail.UpdatedAt = fromMillis(updatedAt)

	var snapshot protocol.TaskSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return protocol.WorkDetail{}, unavailable(err)
	}
	// The stored name carries admission's uniquifying suffix, so the submitted
	// title wins wherever there is one.
	detail.TaskName = firstNonEmptyString(snapshot.SubmittedName, snapshot.Name)
	detail.TaskPrompt = snapshot.Prompt
	if snapshot.Pipeline.ID != "" {
		pipeline := snapshot.Pipeline
		detail.Pipeline = &pipeline
	}
	if brief != "" {
		var decoded protocol.WorkBrief
		if err := json.Unmarshal([]byte(brief), &decoded); err == nil && decoded != (protocol.WorkBrief{}) {
			detail.Brief = &decoded
		}
	}

	if work.AssignedWorkerID != "" {
		// A missing Worker row is not an error here: the Work still has an id
		// to correlate on, and the page simply shows that instead.
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM workers WHERE id = ?`, work.AssignedWorkerID,
		).Scan(&detail.WorkerName); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return protocol.WorkDetail{}, unavailable(err)
		}
	}
	if detail.Siblings, err = s.workSiblings(ctx, work.RunID, id); err != nil {
		return protocol.WorkDetail{}, err
	}
	detail.Handoffs = deriveStageHandoffs(work.Stages)
	detail.Verification = summariseVerification(work.Stages)
	detail.Cost = summariseWorkCost(work.Stages, work.Attempts)
	detail.NeedsAttention = workNeedsAttention(protocol.WorkListSummary{
		State: work.State, BlockedReason: work.BlockedReason, TerminalAt: work.TerminalAt,
	}, s.now())
	return detail, nil
}

// workSiblings lists the other repositories' shares of the same Run, which is
// how the detail page offers a way back to the rest of a multi-repository
// admission without loading all of it.
func (s *Store) workSiblings(ctx context.Context, runID, excludeID string) ([]protocol.WorkSibling, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repository_identity, state FROM sessions
		WHERE run_id = ? AND id != ? ORDER BY target_position, id
	`, runID, excludeID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var siblings []protocol.WorkSibling
	for rows.Next() {
		var sibling protocol.WorkSibling
		if err := rows.Scan(&sibling.ID, &sibling.RepositoryIdentity, &sibling.State); err != nil {
			return nil, unavailable(err)
		}
		siblings = append(siblings, sibling)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return siblings, nil
}

// deriveStageHandoffs reconstructs what each stage passed to the next.
//
// Nothing is stored: the Worker builds this envelope at execution time from
// the same stage rows, so a second persisted copy would be a projection that
// can drift from its source with no way to tell which is authoritative. See
// formatStageEvidence in internal/worker.
//
// Only a stage that finished delivers anything. A successor that ran after a
// stage which never completed received no evidence at all, and the edge says
// so rather than showing an empty summary that reads like an empty result.
func deriveStageHandoffs(stages []protocol.StageRun) []protocol.StageHandoff {
	if len(stages) < 2 {
		return nil
	}
	handoffs := make([]protocol.StageHandoff, 0, len(stages)-1)
	for index := 0; index+1 < len(stages); index++ {
		from, to := stages[index], stages[index+1]
		handoff := protocol.StageHandoff{
			FromStage: from.Position, ToStage: to.Position,
			Kind: handoffKind(from), FromState: from.State,
		}
		if from.State == protocol.StageSucceeded || from.State == protocol.StageFailed {
			summary := strings.TrimSpace(from.Result)
			bounded := boundedUTF8Bytes(summary, protocol.MaxStageHandoffBytes/2)
			handoff.Summary = bounded
			handoff.Truncated = len(bounded) < len(summary)
			handoff.Delivered = from.State == protocol.StageSucceeded
		}
		handoffs = append(handoffs, handoff)
	}
	return handoffs
}

// handoffKind names what sort of evidence crossed the edge, so the UI can say
// "review verdict" or "command output" rather than treating every hand-off as
// undifferentiated agent prose.
func handoffKind(stage protocol.StageRun) string {
	switch {
	case stage.ReviewVerdict != "":
		return "review-verdict"
	case protocol.IsCodeStage(stage.Kind):
		return "command-output"
	case protocol.IsDeliveryStage(stage.Kind):
		return "delivery-evidence"
	default:
		return "agent-result"
	}
}

// summariseVerification normalises the two kinds of verification evidence,
// keeping them labelled and never blending them.
//
// A code stage is authoritative: Factory ran the command and holds its exit
// status. An agent stage's report is a claim, parsed conservatively out of the
// contracted block and marked agent-reported; a stage whose result does not
// follow the contract contributes nothing at all rather than a guess.
//
// Code stages are listed first, so the evidence Factory can stand behind reads
// before the evidence it is merely relaying.
//
// The count is of checks, never of tests. Factory does not know how many test
// cases a command contained and does not guess.
func summariseVerification(stages []protocol.StageRun) protocol.VerificationSummary {
	summary := protocol.VerificationSummary{}
	var reported []protocol.VerificationCheck
	for _, stage := range stages {
		if !protocol.IsCodeStage(stage.Kind) {
			// An agent stage cannot verify itself into a code stage's
			// authority, but what it claims is still worth showing.
			reported = append(reported, protocol.ParseStageReportChecks(stage.Result)...)
			continue
		}
		check := protocol.VerificationCheck{
			Name:   firstNonEmptyString(stage.Command, stage.Name),
			Source: protocol.VerificationSourceCodeStage,
		}
		switch stage.State {
		case protocol.StageSucceeded:
			check.State = protocol.VerificationPassed
		case protocol.StageFailed:
			check.State = protocol.VerificationFailed
			check.Detail = boundedUTF8Bytes(strings.TrimSpace(stage.Error), 512)
		default:
			// Pending, running and cancelled all mean Factory holds no exit
			// status for this command, which is not the same as a pass.
			check.State = protocol.VerificationNotRun
		}
		summary.Items = append(summary.Items, check)
	}
	summary.Items = append(summary.Items, reported...)
	for _, check := range summary.Items {
		summary.RecordedChecks++
		switch check.State {
		case protocol.VerificationPassed:
			summary.Passed++
		case protocol.VerificationFailed:
			summary.Failed++
		default:
			summary.NotRun++
		}
	}
	return summary
}

// summariseWorkCost breaks spend down by stage, attempt and model.
//
// Every total is nil unless something actually reported a figure. A stage that
// ran a model and reported nothing is counted in UnavailableStages so that a
// partial total can say it is partial rather than passing as complete.
func summariseWorkCost(stages []protocol.StageRun, attempts []protocol.Attempt) protocol.WorkCost {
	cost := protocol.WorkCost{}
	for _, stage := range stages {
		entry := protocol.StageCost{
			Position: stage.Position, Name: stage.Name,
			Kind: stage.Kind, Model: stage.Model, Usage: stage.Usage,
		}
		if stage.CostUSD != nil {
			value := *stage.CostUSD
			entry.CostUSD = &value
		} else if usesAModel(stage) && stage.State == protocol.StageSucceeded {
			// A code or delivery stage reaches no model, so its absent cost is
			// "not applicable" rather than "unavailable".
			cost.UnavailableStages++
		}
		cost.ByStage = append(cost.ByStage, entry)
		for model, usage := range stage.Models {
			if cost.ByModel == nil {
				cost.ByModel = map[string]protocol.ModelUsage{}
			}
			cost.ByModel[model] = addModelUsage(cost.ByModel[model], usage)
		}
	}
	// The total is summed over attempts, never over stages. RetrySession sets
	// every stage row's cost back to NULL, so a stage-derived total would drop
	// what earlier attempts spent while ByAttempt below still reported it, and
	// would disagree with the Overview, which sums attempts.
	var total float64
	measured := false
	for _, attempt := range attempts {
		entry := protocol.AttemptCost{
			AttemptNumber: attempt.AttemptNumber, State: attempt.State, Usage: attempt.Usage,
		}
		if attempt.CostUSD != nil {
			value := *attempt.CostUSD
			entry.CostUSD = &value
			total += value
			measured = true
		}
		cost.ByAttempt = append(cost.ByAttempt, entry)
	}
	if measured {
		cost.TotalUSD = &total
	}
	return cost
}

// usesAModel reports whether a stage reaches a runtime at all. A code stage
// runs a declared command and a delivery stage runs fixed Git operations, so
// neither can have a cost and neither counts as unavailable.
func usesAModel(stage protocol.StageRun) bool {
	return !protocol.IsCodeStage(stage.Kind) && !protocol.IsDeliveryStage(stage.Kind)
}

func addModelUsage(into, add protocol.ModelUsage) protocol.ModelUsage {
	into.InputTokens += add.InputTokens
	into.CacheCreationInputTokens += add.CacheCreationInputTokens
	into.CacheReadInputTokens += add.CacheReadInputTokens
	into.OutputTokens += add.OutputTokens
	into.CostUSD += add.CostUSD
	return into
}
