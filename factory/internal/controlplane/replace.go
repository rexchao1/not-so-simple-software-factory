package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

func (s *Store) ReplaceWork(
	ctx context.Context,
	input protocol.ReplaceWorkRequest,
) (protocol.WorkReplacement, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.WorkID = strings.TrimSpace(input.WorkID)
	if input.RequestKey == "" || len([]byte(input.RequestKey)) > 200 {
		return protocol.WorkReplacement{}, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(input.RequestKey, "schedule:") {
		return protocol.WorkReplacement{}, invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}
	if !validUUID(input.WorkID) {
		return protocol.WorkReplacement{}, invalid("invalid_work_id", "work_id must be a UUID")
	}
	fingerprintBody, _ := json.Marshal(struct {
		Operation string `json:"operation"`
		WorkID    string `json:"work_id"`
	}{Operation: "replace", WorkID: input.WorkID})
	fingerprintArray := sha256.Sum256(fingerprintBody)
	fingerprint := fingerprintArray[:]

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	defer tx.Rollback()
	var existingRunID string
	var existingFingerprint []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, request_digest FROM runs WHERE request_key = ?
	`, input.RequestKey).Scan(&existingRunID, &existingFingerprint)
	if err == nil {
		if !bytes.Equal(existingFingerprint, fingerprint) {
			return protocol.WorkReplacement{}, conflict(
				"request_key_conflict", "request_key was already used with different replacement inputs",
			)
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkReplacement{}, unavailable(err)
		}
		detail, err := s.Run(ctx, existingRunID)
		if err != nil {
			return protocol.WorkReplacement{}, err
		}
		return protocol.WorkReplacement{
			Result: protocol.AdmissionReplayed, RequestKey: input.RequestKey, Run: detail,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	// Replacement creates a new Run and new Work, so it is an admission path.
	// Below the replay lookup above, per INV-13.
	if err := pauseGate(ctx, tx, pauseAdmissionMessage); err != nil {
		return protocol.WorkReplacement{}, err
	}

	var predecessor protocol.Work
	var taskID, taskSnapshotJSON string
	var outcomeContract protocol.OutcomeContract
	var oldPublishBranch string
	err = tx.QueryRowContext(ctx, `
		SELECT session.run_id, session.repository_id, session.repository_identity,
		       session.resolved_prompt, session.required_runtime, session.timeout_seconds,
		       session.state, session.target_position, session.target_key, session.target_kind,
		       session.source_kind, session.source_key, session.source_reference,
		       session.context_snapshot, session.publish_branch,
		       session.execution_profile_id, session.execution_profile_version,
		       session.execution_backend, session.execution_provider, session.execution_model,
		       session.resource_class, session.commit_resolution_policy,
		       run.task_id, run.task_snapshot, run.outcome_contract
		FROM sessions session
		JOIN runs run ON run.id = session.run_id
		WHERE session.id = ?
	`, input.WorkID).Scan(
		&predecessor.RunID, &predecessor.RepositoryID, &predecessor.RepositoryIdentity,
		&predecessor.ResolvedPrompt, &predecessor.RequiredRuntime, &predecessor.TimeoutSeconds,
		&predecessor.State, &predecessor.Target.Position, &predecessor.Target.TargetKey,
		&predecessor.Target.TargetKind, &predecessor.Target.SourceKind,
		&predecessor.Target.SourceKey, &predecessor.Target.SourceReference,
		&predecessor.Target.ContextSnapshot, &oldPublishBranch,
		&predecessor.Execution.ProfileID, &predecessor.Execution.ProfileVersion,
		&predecessor.Execution.Backend, &predecessor.Execution.Provider,
		&predecessor.Execution.Model, &predecessor.Execution.ResourceClass,
		&predecessor.Execution.CommitResolutionPolicy, &taskID, &taskSnapshotJSON,
		&outcomeContract,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkReplacement{}, ErrNotFound
	}
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	if predecessor.State != protocol.WorkReady && predecessor.State != protocol.WorkSucceeded &&
		predecessor.State != protocol.WorkFailed && predecessor.State != protocol.WorkNoChange &&
		predecessor.State != protocol.WorkCancelled {
		return protocol.WorkReplacement{}, conflict("replacement_not_terminal", "only terminal Work can be replaced")
	}
	if err := validateWorkRetryGuards(
		ctx, tx, input.WorkID, predecessor.RepositoryID, predecessor.Target.TargetKind,
		predecessor.Target.SourceKind, predecessor.Target.SourceKey,
	); err != nil {
		return protocol.WorkReplacement{}, err
	}
	if err := validateReplacementEligibility(
		ctx, tx, taskID, predecessor.RepositoryID, predecessor.RepositoryIdentity,
		outcomeContract, predecessor.Execution,
	); err != nil {
		return protocol.WorkReplacement{}, err
	}

	var snapshot protocol.TaskSnapshot
	if err := json.Unmarshal([]byte(taskSnapshotJSON), &snapshot); err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	predecessor.Execution.Runtime = predecessor.RequiredRuntime
	predecessor.Execution.TimeoutSeconds = predecessor.TimeoutSeconds
	executionSnapshot, err := json.Marshal(predecessor.Execution)
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	runID, err := newID()
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	workID, err := newID()
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	newPublishBranch := workPublishBranch(workID)
	resolvedPrompt := predecessor.ResolvedPrompt
	if taskID == protocol.StandardBuildProcedureID {
		resolvedPrompt = replacePublishBranch(resolvedPrompt, oldPublishBranch, newPublishBranch)
	}
	target := protocol.WorkTarget{
		ID: workID, Position: 0, TargetKey: predecessor.Target.TargetKey,
		TargetKind: predecessor.Target.TargetKind, RepositoryID: predecessor.RepositoryID,
		RepositoryIdentity: predecessor.RepositoryIdentity, SourceKind: predecessor.Target.SourceKind,
		SourceKey: predecessor.Target.SourceKey, SourceReference: predecessor.Target.SourceReference,
		ContextSnapshot: predecessor.Target.ContextSnapshot, PublishBranch: newPublishBranch,
	}
	stageDefaults, err := stageDefaultsTx(ctx, tx)
	if err != nil {
		return protocol.WorkReplacement{}, err
	}
	resolvedStages, err := resolveSessionStages(snapshot, resolvedPrompt, runID, target, stageDefaults)
	if err != nil {
		return protocol.WorkReplacement{}, err
	}

	assignedWorkerID, blockedReason, err := s.replacementRoute(
		ctx, tx, predecessor.RepositoryID, predecessor.RepositoryIdentity,
		predecessor.RequiredRuntime, predecessor.Execution, now,
	)
	if err != nil {
		return protocol.WorkReplacement{}, err
	}
	state := "queued"
	if assignedWorkerID == "" {
		state = "blocked"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			id, request_key, request_digest, task_id, task_snapshot, source,
			requested_execution_profile_id, execution_snapshot, outcome_contract,
			admitted_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'manual', NULL, ?, ?, ?, ?)
	`, runID, input.RequestKey, fingerprint, taskID, taskSnapshotJSON,
		executionSnapshot, outcomeContract, now, now); err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	targetsJSON, err := json.Marshal([]protocol.WorkTarget{target})
	if err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET targets_snapshot = ? WHERE id = ?`, targetsJSON, runID); err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions(
			id, run_id, repository_id, repository_identity, resolved_prompt, required_runtime,
			timeout_seconds, state, blocked_reason, assigned_worker_id, admitted_at,
			execution_profile_id, execution_profile_version, execution_backend, execution_provider,
			execution_model, resource_class, commit_resolution_policy,
			target_position, target_key, target_kind, source_kind, source_key,
			source_reference, context_snapshot, publish_branch, predecessor_work_id,
			execution_owner, waiting_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, 'none', ?)
	`, workID, runID, predecessor.RepositoryID, predecessor.RepositoryIdentity,
		resolvedPrompt, predecessor.RequiredRuntime, predecessor.TimeoutSeconds, state,
		nullableString(blockedReason), nullableString(assignedWorkerID), now,
		predecessor.Execution.ProfileID, predecessor.Execution.ProfileVersion,
		predecessor.Execution.Backend, predecessor.Execution.Provider,
		predecessor.Execution.Model, predecessor.Execution.ResourceClass,
		predecessor.Execution.CommitResolutionPolicy, target.TargetKey, target.TargetKind,
		target.SourceKind, target.SourceKey, target.SourceReference, target.ContextSnapshot,
		target.PublishBranch, input.WorkID,
		boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes)); err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	if err := insertSessionStages(ctx, tx, workID, resolvedStages); err != nil {
		return protocol.WorkReplacement{}, err
	}
	if assignedWorkerID != "" {
		executionID, err := newID()
		if err != nil {
			return protocol.WorkReplacement{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO executions(
				id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'queued', ?, ?)
		`, executionID, workID, assignedWorkerID, predecessor.RequiredRuntime, now, now); err != nil {
			return protocol.WorkReplacement{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkReplacement{}, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	if err != nil {
		return protocol.WorkReplacement{}, err
	}
	return protocol.WorkReplacement{
		Result: protocol.AdmissionAdmitted, RequestKey: input.RequestKey, Run: detail,
	}, nil
}

func validateReplacementEligibility(
	ctx context.Context,
	tx *sql.Tx,
	taskID, repositoryID, repositoryIdentity string,
	contract protocol.OutcomeContract,
	execution protocol.ExecutionSnapshot,
) error {
	var archived, migrationOnly, readOnly int
	err := tx.QueryRowContext(ctx, `
		SELECT archived, migration_only, read_only FROM tasks WHERE id = ?
	`, taskID).Scan(&archived, &migrationOnly, &readOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return conflict("procedure_not_available", "the predecessor Procedure no longer exists")
	}
	if err != nil {
		return unavailable(err)
	}
	if taskID != protocol.StandardBuildProcedureID && (archived != 0 || migrationOnly != 0 || readOnly != 0) {
		return conflict("procedure_not_available", "the predecessor Procedure is archived or unavailable")
	}
	var repositoryAvailable int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM repositories
			WHERE id = ? AND remote_identity = ?
			  AND (centrally_managed = 0 OR enabled = 1)
		)
	`, repositoryID, repositoryIdentity).Scan(&repositoryAvailable); err != nil {
		return unavailable(err)
	}
	if repositoryAvailable == 0 {
		return conflict("repository_not_available", "the predecessor repository is disabled, deleted, or changed identity")
	}
	if contract == protocol.OutcomeAgentUpdate && !protocol.WorkerDispatched(execution.Backend) {
		return conflict("agent_update_backend_unsupported", "the frozen execution backend cannot resume agent_update Work")
	}
	return nil
}

func (s *Store) replacementRoute(
	ctx context.Context,
	tx *sql.Tx,
	repositoryID, identity, runtime string,
	execution protocol.ExecutionSnapshot,
	now int64,
) (string, string, error) {
	if protocol.WorkerDispatched(execution.Backend) {
		return s.resumeRoute(ctx, tx, repositoryID, identity, runtime, now)
	}
	var available int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM execution_profile_versions version
			JOIN execution_profiles profile ON profile.id = version.profile_id
			JOIN workers worker ON worker.id = ? AND worker.synthetic = 1
			WHERE version.profile_id = ? AND version.version = ?
			  AND profile.enabled = 1 AND profile.healthy = 1
		)
	`, syntheticWorkerID(execution.ProfileID), execution.ProfileID, execution.ProfileVersion).Scan(&available); err != nil {
		return "", "", unavailable(err)
	}
	if available == 0 {
		return "", "", conflict("execution_profile_version_unavailable", "the frozen execution profile version is unavailable")
	}
	return syntheticWorkerID(execution.ProfileID), "", nil
}

func replacePublishBranch(prompt, oldBranch, newBranch string) string {
	if oldBranch == "" || oldBranch == newBranch {
		return prompt
	}
	return strings.Replace(prompt, "Factory publish branch: "+oldBranch,
		"Factory publish branch: "+newBranch, 1)
}
