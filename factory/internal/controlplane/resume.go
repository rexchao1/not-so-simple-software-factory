package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type continuationState struct {
	title, repository, resolvedPrompt, publishBranch  string
	question, answer, checkpointSHA, pendingResumeSHA string
	pullRequestURL, pullRequestHeadBranch             string
	pullRequestHeadSHA                                string
	retryMayRepeatEffects                             bool
}

type continuationHistory struct {
	Sequence              int                       `json:"sequence"`
	Kind                  string                    `json:"kind,omitempty"`
	Status                protocol.WorkUpdateStatus `json:"status,omitempty"`
	Actor                 string                    `json:"actor"`
	Message               string                    `json:"message"`
	PullRequestURL        string                    `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string                    `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string                    `json:"pull_request_head_sha,omitempty"`
	CheckpointSHA         string                    `json:"checkpoint_sha,omitempty"`
	AcceptedAtMillis      int64                     `json:"accepted_at_millis"`
	Trusted               bool                      `json:"trusted"`
	MessageTruncated      bool                      `json:"message_truncated,omitempty"`
}

type continuationSelection struct {
	line     string
	complete bool
}

func agentContinuationReserveFits(title, repository, resolvedPrompt, publishBranch string) bool {
	state := continuationState{
		title: title, repository: repository, resolvedPrompt: resolvedPrompt,
		publishBranch:         publishBranch,
		question:              strings.Repeat("\u0085", protocol.MaxQuestionBytes/2),
		answer:                strings.Repeat("a", protocol.MaxAnswerBytes),
		checkpointSHA:         strings.Repeat("f", 64),
		pendingResumeSHA:      strings.Repeat("f", 64),
		pullRequestURL:        "https://github.com/" + strings.Repeat("r", 2028),
		pullRequestHeadBranch: strings.Repeat("b", 255),
		pullRequestHeadSHA:    strings.Repeat("f", 64),
		retryMayRepeatEffects: true,
	}
	history := []continuationHistory{{
		Sequence: int(^uint(0) >> 1), Kind: "update", Status: protocol.WorkUpdateNeedsInput,
		Actor:         string(protocol.WorkUpdateActorAgent),
		Message:       strings.Repeat("q", protocol.MaxQuestionBytes),
		CheckpointSHA: strings.Repeat("f", 64), AcceptedAtMillis: 9223372036854775807,
	}, {
		Sequence: int(^uint(0) >> 1), Kind: "answer", Actor: string(protocol.WorkUpdateActorOperator),
		Message: strings.Repeat("a", protocol.MaxAnswerBytes), AcceptedAtMillis: 9223372036854775807,
		Trusted: true,
	}}
	prompt, err := assembleContinuationPrompt(state, history)
	return err == nil && protocol.AgentUpdatePromptFits(title, repository, publishBranch, prompt)
}

func (s *Store) continuationPrompt(ctx context.Context, workID string) (string, error) {
	state, history, err := loadContinuationState(ctx, s.db, workID)
	if err != nil {
		return "", err
	}
	if state.question == "" && state.answer == "" && state.checkpointSHA == "" &&
		state.pendingResumeSHA == "" &&
		state.pullRequestURL == "" && !state.retryMayRepeatEffects && len(history) == 0 {
		return state.resolvedPrompt, nil
	}
	return assembleContinuationPrompt(state, history)
}

func validateContinuationWithinTx(
	ctx context.Context,
	tx *sql.Tx,
	workID, question, answer string,
	prospective ...continuationHistory,
) error {
	state, history, err := loadContinuationState(ctx, tx, workID)
	if err != nil {
		return err
	}
	state.question, state.answer = question, answer
	history = append(history, prospective...)
	prompt, err := assembleContinuationPrompt(state, history)
	if err != nil || !protocol.AgentUpdatePromptFits(
		state.title, state.repository, state.publishBranch, prompt,
	) {
		return &ServiceError{
			Code:    "continuation_prompt_too_large",
			Message: "the mandatory recovery context cannot fit the 72 KiB agent prompt",
			Status:  413,
		}
	}
	return nil
}

func loadContinuationState(
	ctx context.Context,
	queryer sqlQueryer,
	workID string,
) (continuationState, []continuationHistory, error) {
	var state continuationState
	var retry int
	err := queryer.QueryRowContext(ctx, `
		SELECT json_extract(run.task_snapshot, '$.name'), session.repository_identity,
		       COALESCE(
		           (SELECT prompt FROM session_stages WHERE session_id = session.id ORDER BY position DESC LIMIT 1),
		           session.resolved_prompt
		       ), session.publish_branch, session.question,
		       session.answer, session.checkpoint_sha, session.pending_resume_sha,
		       session.pull_request_url,
		       session.pull_request_head_branch, session.pull_request_head_sha,
		       session.retry_may_repeat_effects
		FROM sessions session
		JOIN runs run ON run.id = session.run_id
		WHERE session.id = ?
	`, workID).Scan(
		&state.title, &state.repository, &state.resolvedPrompt, &state.publishBranch,
		&state.question, &state.answer, &state.checkpointSHA, &state.pendingResumeSHA,
		&state.pullRequestURL,
		&state.pullRequestHeadBranch, &state.pullRequestHeadSHA, &retry,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, ErrNotFound
	}
	if err != nil {
		return state, nil, unavailable(err)
	}
	state.retryMayRepeatEffects = retry != 0
	rows, err := queryer.QueryContext(ctx, `
		SELECT sequence, kind, status, actor, message, pull_request_url,
		       pull_request_head_branch, pull_request_head_sha, checkpoint_sha,
		       accepted_at, trusted
		FROM (
			SELECT sequence, sequence * 2 AS sort_key, 'update' AS kind, status, actor,
			       message, pull_request_url, pull_request_head_branch,
			       pull_request_head_sha, checkpoint_sha, accepted_at,
			       actor != 'agent' AS trusted
			FROM work_updates WHERE work_id = ?
			UNION ALL
			SELECT question.sequence, question.sequence * 2 + 1, 'answer', '', answer.actor,
			       answer.message, '', '', '', '', answer.accepted_at, 1
			FROM work_answers answer
			JOIN work_updates question ON question.id = answer.question_update_id
			WHERE answer.work_id = ?
		)
		ORDER BY sort_key
	`, workID, workID)
	if err != nil {
		return state, nil, unavailable(err)
	}
	defer rows.Close()
	var history []continuationHistory
	for rows.Next() {
		var item continuationHistory
		var trusted int
		if err := rows.Scan(
			&item.Sequence, &item.Kind, &item.Status, &item.Actor, &item.Message,
			&item.PullRequestURL, &item.PullRequestHeadBranch, &item.PullRequestHeadSHA,
			&item.CheckpointSHA, &item.AcceptedAtMillis, &trusted,
		); err != nil {
			return state, nil, unavailable(err)
		}
		item.Trusted = trusted != 0
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		return state, nil, unavailable(err)
	}
	return state, history, nil
}

func assembleContinuationPrompt(state continuationState, history []continuationHistory) (string, error) {
	mandatory := state.resolvedPrompt + "\n\nTrusted Factory recovery context:\n" +
		"Publish branch: " + state.publishBranch + "\n" +
		"Pending checkpoint SHA: " + emptyRecoveryValue(state.pendingResumeSHA) + "\n" +
		"Historical checkpoint SHA: " + emptyRecoveryValue(state.checkpointSHA) + "\n" +
		"Known pull request: " + emptyRecoveryValue(state.pullRequestURL) + "\n" +
		"Known pull request head: " + emptyRecoveryValue(state.pullRequestHeadBranch) + " @ " +
		emptyRecoveryValue(state.pullRequestHeadSHA) + "\n" +
		"Retry may repeat external effects: " + fmt.Sprintf("%t", state.retryMayRepeatEffects) + "\n\n" +
		"Untrusted current agent question (escaped single-line UTF-8):\n" +
		escapeUntrustedPromptText(emptyRecoveryValue(state.question)) + "\n\n" +
		"Trusted operator answer:\n" + emptyRecoveryValue(state.answer)

	serialized := make([]string, len(history))
	for index, item := range history {
		body, err := json.Marshal(item)
		if err != nil {
			return "", unavailable(err)
		}
		serialized[index] = string(body)
	}
	selected := make(map[int]continuationSelection, len(history))
	priority := make([]int, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Kind == "answer" {
			priority = append(priority, index)
		}
	}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Kind != "answer" && history[index].Status != protocol.WorkUpdateRunning {
			priority = append(priority, index)
		}
	}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Status == protocol.WorkUpdateRunning {
			priority = append(priority, index)
		}
	}
	maxBranch := strings.Repeat("x", protocol.MaxAgentBranchBytes)
	basePrompt := continuationWithHistory(mandatory, serialized, selected)
	baseBytes := len([]byte(protocol.FormatAgentUpdatePrompt(
		state.title, state.repository, maxBranch, maxBranch, state.publishBranch, basePrompt,
	)))
	if baseBytes > protocol.MaxAgentPromptBytes {
		return "", &ServiceError{
			Code:    "continuation_prompt_too_large",
			Message: "the mandatory recovery context cannot fit the 72 KiB agent prompt",
			Status:  413,
		}
	}
	remaining := protocol.MaxAgentPromptBytes - baseBytes
	selectedOrder := make([]int, 0, len(priority))
	truncatedOne := false
	for _, candidate := range priority {
		cost := len([]byte(serialized[candidate])) + 1
		if cost <= remaining {
			selected[candidate] = continuationSelection{line: serialized[candidate], complete: true}
			selectedOrder = append(selectedOrder, candidate)
			remaining -= cost
			continue
		}
		if truncatedOne {
			break
		}
		line, ok, err := truncateContinuationHistory(history[candidate], remaining)
		if err != nil {
			return "", unavailable(err)
		}
		truncatedOne = true
		if !ok {
			break
		}
		selected[candidate] = continuationSelection{line: line}
		selectedOrder = append(selectedOrder, candidate)
		remaining -= len([]byte(line)) + 1
	}
	prompt := continuationWithHistory(mandatory, serialized, selected)
	for !protocol.AgentUpdatePromptFits(
		state.title, state.repository, state.publishBranch, prompt,
	) && len(selectedOrder) != 0 {
		last := selectedOrder[len(selectedOrder)-1]
		selectedOrder = selectedOrder[:len(selectedOrder)-1]
		delete(selected, last)
		prompt = continuationWithHistory(mandatory, serialized, selected)
	}
	if !protocol.AgentUpdatePromptFits(state.title, state.repository, state.publishBranch, prompt) {
		return "", &ServiceError{
			Code:    "continuation_prompt_too_large",
			Message: "the mandatory recovery context cannot fit the 72 KiB agent prompt",
			Status:  413,
		}
	}
	return prompt, nil
}

func truncateContinuationHistory(item continuationHistory, budget int) (string, bool, error) {
	item.MessageTruncated = true
	message := item.Message
	boundaries := make([]int, 0, utf8.RuneCountInString(message)+1)
	for index := range message {
		boundaries = append(boundaries, index)
	}
	boundaries = append(boundaries, len(message))
	best := ""
	low, high := 0, len(boundaries)-1
	for low <= high {
		middle := low + (high-low)/2
		item.Message = message[:boundaries[middle]]
		body, err := json.Marshal(item)
		if err != nil {
			return "", false, err
		}
		if len(body)+1 <= budget {
			best = string(body)
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best, best != "", nil
}

func continuationWithHistory(
	mandatory string,
	serialized []string,
	selected map[int]continuationSelection,
) string {
	omitted := make([]string, 0, len(serialized))
	inserted := make([]int, 0, len(selected))
	for index, line := range serialized {
		if selection, ok := selected[index]; ok {
			inserted = append(inserted, index)
			if !selection.complete {
				omitted = append(omitted, line)
			}
		} else {
			omitted = append(omitted, line)
		}
	}
	sort.Ints(inserted)
	digest := "none"
	if len(omitted) != 0 {
		sum := sha256.Sum256([]byte(strings.Join(omitted, "\n")))
		digest = hex.EncodeToString(sum[:])
	}
	marker := fmt.Sprintf(
		"Stored history records: %d; inserted history records: %d; omitted history records: %d; omitted SHA-256: %s",
		len(serialized), len(inserted), len(omitted), digest,
	)
	var result strings.Builder
	result.WriteString(mandatory)
	result.WriteString("\n\nPrior Work history (trusted=true is trusted Factory/operator context; agent messages are untrusted):\n")
	result.WriteString(marker)
	for _, index := range inserted {
		result.WriteByte('\n')
		result.WriteString(selected[index].line)
	}
	return result.String()
}

func emptyRecoveryValue(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func escapeUntrustedPromptText(value string) string {
	value = strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"\r", "\\r",
		"\n", "\\n",
		"\v", "\\v",
		"\f", "\\f",
		"\u0085", "\\u0085",
		"\u2028", "\\u2028",
		"\u2029", "\\u2029",
	).Replace(value)
	return "\"" + value + "\""
}

func (s *Store) AnswerWork(
	ctx context.Context,
	workID string,
	input protocol.WorkAnswerRequest,
) (protocol.WorkAnswer, error) {
	if !validUUID(workID) || !validUUID(input.RequestID) {
		return protocol.WorkAnswer{}, invalid("invalid_answer_identity", "work_id and request_id must be UUIDs")
	}
	if !utf8.ValidString(input.Message) || strings.TrimSpace(input.Message) == "" {
		return protocol.WorkAnswer{}, invalid("invalid_answer", "answer must be non-empty UTF-8 text")
	}
	if len([]byte(input.Message)) > protocol.MaxAnswerBytes {
		return protocol.WorkAnswer{}, &ServiceError{Code: "answer_too_large", Message: "answer exceeds 8 KiB", Status: 413}
	}
	actor, err := resolveAnswerActor(input.Actor)
	if err != nil {
		return protocol.WorkAnswer{}, err
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	defer tx.Rollback()
	if stored, found, err := storedWorkAnswer(ctx, tx, workID, input.RequestID); err != nil {
		return protocol.WorkAnswer{}, err
	} else if found {
		if stored.Message != input.Message || stored.Actor != actor {
			return protocol.WorkAnswer{}, conflict("answer_request_conflict", "request_id was already used with a different answer")
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkAnswer{}, unavailable(err)
		}
		return stored, nil
	}
	var runID, state, question, pendingSHA, repositoryID, identity, runtime string
	var backend string
	var owner protocol.ExecutionOwner
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, state, question, pending_resume_sha, repository_id,
		       repository_identity, required_runtime, execution_backend, execution_owner
		FROM sessions WHERE id = ?
	`, workID).Scan(&runID, &state, &question, &pendingSHA, &repositoryID, &identity, &runtime, &backend, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkAnswer{}, ErrNotFound
	}
	if err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	if state != string(protocol.WorkNeedsInput) || owner != protocol.ExecutionOwnerNone {
		return protocol.WorkAnswer{}, conflict("work_answer_not_allowed", "only unowned needs-input Work can be answered")
	}
	if !validCommitSHA(pendingSHA) {
		return protocol.WorkAnswer{}, conflict("resume_checkpoint_missing", "needs-input Work has no authoritative pending resume commit")
	}
	if !protocol.WorkerDispatched(backend) {
		return protocol.WorkAnswer{}, conflict("agent_update_backend_unsupported", "resumable Work requires the persistent execution backend")
	}
	// Answering requeues the existing execution and reselects a Worker, which
	// is a dispatch decision. The stored-answer replay above still returns its
	// original answer while paused, so a client retry stays idempotent.
	if err := pauseGate(ctx, tx, pauseDispatchMessage); err != nil {
		return protocol.WorkAnswer{}, err
	}
	var questionUpdateID string
	var questionSequence int
	if err := tx.QueryRowContext(ctx, `
		SELECT id, sequence FROM work_updates
		WHERE work_id = ? AND status = 'needs-input'
		ORDER BY sequence DESC LIMIT 1
	`, workID).Scan(&questionUpdateID, &questionSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.WorkAnswer{}, conflict(
				"resume_question_missing", "needs-input Work has no recorded question update",
			)
		}
		return protocol.WorkAnswer{}, unavailable(err)
	}
	prospective := continuationHistory{
		Sequence: questionSequence, Kind: "answer", Actor: actor,
		Message: input.Message, AcceptedAtMillis: now, Trusted: true,
	}
	if err := validateContinuationWithinTx(ctx, tx, workID, question, input.Message, prospective); err != nil {
		return protocol.WorkAnswer{}, err
	}
	answerID, err := newID()
	if err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_answers(id, work_id, question_update_id, request_id, message, actor, accepted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, answerID, workID, questionUpdateID, input.RequestID, input.Message, actor, now); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}

	var concurrencySlotAvailable int
	if err := tx.QueryRowContext(ctx, `
		SELECT (
			SELECT COUNT(*) FROM sessions active
			WHERE active.run_id = run.id AND active.state IN ('queued', 'preparing', 'running')
		) < json_extract(run.task_snapshot, '$.concurrency_limit')
		FROM runs run WHERE run.id = ?
	`, runID).Scan(&concurrencySlotAvailable); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	assignedWorkerID, blockedReason := "", taskConcurrencyBlockedReason
	if concurrencySlotAvailable != 0 {
		assignedWorkerID, blockedReason, err = s.resumeRoute(ctx, tx, repositoryID, identity, runtime, now)
		if err != nil {
			return protocol.WorkAnswer{}, err
		}
	}
	if assignedWorkerID != "" {
		if err := queueExistingExecution(ctx, tx, workID, assignedWorkerID, runtime, now); err != nil {
			return protocol.WorkAnswer{}, err
		}
	}
	workState := "queued"
	if assignedWorkerID == "" {
		workState = "blocked"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = ?, blocked_reason = ?, assigned_worker_id = ?,
		       cancellation_requested = 0, terminal_at = NULL, result = NULL,
		       failure_reason = NULL, terminal_message = '', waiting_reason = ?,
		       execution_owner = 'none', answer = ?, answered_by = ?
		WHERE id = ? AND state = 'needs-input'
	`, workState, nullableString(blockedReason), nullableString(assignedWorkerID),
		boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes), input.Message, actor, workID); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'pending', result = '', error = '',
		       started_at = NULL, completed_at = NULL,
		       cost_usd = NULL, input_tokens = NULL, cache_creation_input_tokens = NULL,
		       cache_read_input_tokens = NULL, output_tokens = NULL, models = NULL
		WHERE session_id = ?
		  AND position = (SELECT MAX(position) FROM session_stages WHERE session_id = ?)
	`, workID, workID); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = ?, terminal_at = NULL WHERE id = ?`, now, runID); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkAnswer{}, unavailable(err)
	}
	return s.workAnswer(ctx, answerID)
}

func (s *Store) resumeRoute(
	ctx context.Context,
	tx *sql.Tx,
	repositoryID, identity, runtime string,
	now int64,
) (string, string, error) {
	selection, err := s.selectSessionRoute(ctx, tx, repositoryID, identity, now, "", runtime)
	if err == nil {
		return selection.workerID, "", nil
	}
	if serviceErrorCode(err, "no_eligible_worker") || serviceErrorCode(err, "repository_not_managed") {
		return "", "Waiting for a healthy compatible Worker with repository access.", nil
	}
	return "", "", err
}

func queueExistingExecution(
	ctx context.Context,
	tx *sql.Tx,
	workID, assignedWorkerID, runtime string,
	now int64,
) error {
	var executionID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM executions WHERE session_id = ?`, workID).Scan(&executionID)
	if errors.Is(err, sql.ErrNoRows) {
		executionID, err = newID()
		if err != nil {
			return unavailable(err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO executions(id, session_id, assigned_worker_id, required_runtime, state,
			                       cancellation_requested, created_at, updated_at, retry_count)
			VALUES (?, ?, ?, ?, ?, 0, ?, ?, 0)
		`, executionID, workID, assignedWorkerID, runtime, "queued", now, now)
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE executions SET assigned_worker_id = COALESCE(NULLIF(?, ''), assigned_worker_id),
			       state = ?, cancellation_requested = 0, updated_at = ? WHERE id = ?
		`, assignedWorkerID, "queued", now, executionID)
	}
	if err != nil {
		return unavailable(err)
	}
	return nil
}

// resolveAnswerActor turns the optional actor on an answer request into the
// label stored with the answer. An absent or whitespace-only actor is the
// operator, so every answer names who gave it. The label is free-form within
// the same bounds as approval's actor, except that "agent" is reserved: an
// answer is trusted context, and nothing trusted may claim to come from the
// agent it is addressed to.
func resolveAnswerActor(raw string) (string, error) {
	actor := strings.TrimSpace(raw)
	if actor == "" {
		return string(protocol.WorkUpdateActorOperator), nil
	}
	if len(actor) > 255 || !utf8.ValidString(actor) {
		return "", invalid("invalid_actor", "actor is required and limited to 255 bytes")
	}
	if strings.EqualFold(actor, string(protocol.WorkUpdateActorAgent)) {
		return "", invalid("invalid_actor", "actor may not be the agent")
	}
	return actor, nil
}

func storedWorkAnswer(
	ctx context.Context,
	queryer sqlQueryer,
	workID, requestID string,
) (protocol.WorkAnswer, bool, error) {
	var answer protocol.WorkAnswer
	var acceptedAt int64
	err := queryer.QueryRowContext(ctx, `
		SELECT id, work_id, question_update_id, request_id, message, actor, accepted_at
		FROM work_answers WHERE work_id = ? AND request_id = ?
	`, workID, requestID).Scan(
		&answer.ID, &answer.WorkID, &answer.QuestionUpdateID, &answer.RequestID,
		&answer.Message, &answer.Actor, &acceptedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return answer, false, nil
	}
	if err != nil {
		return answer, false, unavailable(err)
	}
	answer.AcceptedAt = fromMillis(acceptedAt)
	return answer, true, nil
}

func (s *Store) workAnswer(ctx context.Context, answerID string) (protocol.WorkAnswer, error) {
	var answer protocol.WorkAnswer
	var acceptedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, work_id, question_update_id, request_id, message, actor, accepted_at
		FROM work_answers WHERE id = ?
	`, answerID).Scan(
		&answer.ID, &answer.WorkID, &answer.QuestionUpdateID, &answer.RequestID,
		&answer.Message, &answer.Actor, &acceptedAt,
	)
	if err != nil {
		return answer, unavailable(err)
	}
	answer.AcceptedAt = fromMillis(acceptedAt)
	return answer, nil
}
