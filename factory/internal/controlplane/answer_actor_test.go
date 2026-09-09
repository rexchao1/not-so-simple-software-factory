package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// storedAnswerActor reads work_answers.actor for one request out of the row
// itself, so these tests prove what was written rather than what a loader
// chose to surface. The second value reports whether the row exists at all.
func storedAnswerActor(t *testing.T, store *Store, workID, requestID string) (string, bool) {
	t.Helper()
	var actor string
	err := store.db.QueryRowContext(context.Background(),
		`SELECT actor FROM work_answers WHERE work_id = ? AND request_id = ?`,
		workID, requestID).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return actor, true
}

func assertInvalidActor(t *testing.T, err error) {
	t.Helper()
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 || serviceErr.Code != "invalid_actor" {
		t.Fatalf("err = %#v, want 400 invalid_actor", err)
	}
}

// assertAnswerRejectedWithoutTrace pins the "nothing is stored" half of a
// rejection: no work_answers row for the request, and the Work still waiting.
func assertAnswerRejectedWithoutTrace(t *testing.T, store *Store, workID, requestID string) {
	t.Helper()
	if actor, found := storedAnswerActor(t, store, workID, requestID); found {
		t.Fatalf("rejected answer left a work_answers row with actor %q", actor)
	}
	work, err := store.Work(context.Background(), workID)
	if err != nil || work.State != protocol.WorkNeedsInput || work.Answer != "" || work.AnsweredBy != "" {
		t.Fatalf("rejected answer changed Work = %#v, error %v", work, err)
	}
}

func TestAnswerWorkRecordsActor(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "63000000-0000-4000-8000-000000000001",
		Message:   "Keep the alias.", Actor: "overseer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Actor != "overseer" {
		t.Fatalf("answer actor = %q, want overseer", answer.Actor)
	}
	if actor, found := storedAnswerActor(t, store, work.ID, answer.RequestID); !found || actor != "overseer" {
		t.Fatalf("stored actor = %q (found %v), want overseer", actor, found)
	}
	answered, err := store.Work(context.Background(), work.ID)
	if err != nil || answered.Answer != answer.Message || answered.AnsweredBy != "overseer" {
		t.Fatalf("answered Work = %#v, error %v", answered, err)
	}
	// The stored answer reads back with its actor on a replay too.
	replayed, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: answer.RequestID, Message: answer.Message, Actor: "overseer",
	})
	if err != nil || replayed.ID != answer.ID || replayed.Actor != "overseer" {
		t.Fatalf("replay = %#v, error %v", replayed, err)
	}
}

func TestAnswerWorkDefaultsActorToOperator(t *testing.T) {
	for _, actor := range []string{"", "   ", "\t\n"} {
		t.Run("actor "+strings.ReplaceAll(actor, "\n", "\\n"), func(t *testing.T) {
			store, _, _, work := needsInputWork(t)
			answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: "63000000-0000-4000-8000-000000000002",
				Message:   "Keep the alias.", Actor: actor,
			})
			if err != nil {
				t.Fatal(err)
			}
			if answer.Actor != "operator" {
				t.Fatalf("answer actor = %q, want operator", answer.Actor)
			}
			if stored, found := storedAnswerActor(t, store, work.ID, answer.RequestID); !found || stored != "operator" {
				t.Fatalf("stored actor = %q (found %v), want operator", stored, found)
			}
			answered, err := store.Work(context.Background(), work.ID)
			if err != nil || answered.AnsweredBy != "operator" {
				t.Fatalf("answered Work = %#v, error %v", answered, err)
			}
		})
	}
}

// The actor must be bounded to 255 bytes, matching the CHECK on
// work_answers.actor and sessions.answered_by. Without an application-level
// bound an over-long actor fails the CHECK and surfaces as a generic 503
// instead of a 400.
func TestAnswerWorkRejectsActorOverByteLimit(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	const requestID = "63000000-0000-4000-8000-000000000003"
	_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: requestID, Message: "Keep the alias.", Actor: strings.Repeat("a", 256),
	})
	if err == nil {
		t.Fatal("answer with a 256-byte actor was accepted")
	}
	assertInvalidActor(t, err)
	assertAnswerRejectedWithoutTrace(t, store, work.ID, requestID)

	// Byte length, not rune length: a 255-byte actor is fine, and 255 runes
	// of multi-byte text are not. Invalid UTF-8 is rejected the same way.
	_, err = store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: requestID, Message: "Keep the alias.", Actor: strings.Repeat("é", 200),
	})
	assertInvalidActor(t, err)
	_, err = store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: requestID, Message: "Keep the alias.", Actor: "over\xffseer",
	})
	assertInvalidActor(t, err)
	assertAnswerRejectedWithoutTrace(t, store, work.ID, requestID)
}

func TestAnswerWorkAcceptsActorAtByteLimit(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	actor := strings.Repeat("a", 255)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "63000000-0000-4000-8000-000000000004",
		Message:   "Keep the alias.", Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Actor != actor {
		t.Fatalf("answer actor = %q, want the 255-byte actor", answer.Actor)
	}
	answered, err := store.Work(context.Background(), work.ID)
	if err != nil || answered.AnsweredBy != actor || answered.State != protocol.WorkQueued {
		t.Fatalf("answered Work = %#v, error %v", answered, err)
	}
}

// An answer is trusted context, so nothing trusted may claim to come from the
// agent it is addressed to. The reservation is case-insensitive so that the
// label cannot be smuggled past a byte comparison.
func TestAnswerWorkRejectsAgentActor(t *testing.T) {
	for _, actor := range []string{"agent", "Agent", "AGENT", "  agent  "} {
		t.Run(actor, func(t *testing.T) {
			store, _, _, work := needsInputWork(t)
			const requestID = "63000000-0000-4000-8000-000000000005"
			_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
				RequestID: requestID, Message: "Keep the alias.", Actor: actor,
			})
			if err == nil {
				t.Fatalf("answer with actor %q was accepted", actor)
			}
			assertInvalidActor(t, err)
			assertAnswerRejectedWithoutTrace(t, store, work.ID, requestID)
		})
	}
}

// A replay must match the whole stored answer, actor included. Two callers
// disagreeing over who gave the answer is the same conflict as two callers
// disagreeing over what it said.
func TestAnswerWorkReplayWithDifferentActorConflicts(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	answer, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
		RequestID: "63000000-0000-4000-8000-000000000006",
		Message:   "Keep the alias.", Actor: "overseer",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"", "operator", "reviewer"} {
		_, err := store.AnswerWork(context.Background(), work.ID, protocol.WorkAnswerRequest{
			RequestID: answer.RequestID, Message: answer.Message, Actor: actor,
		})
		if !serviceErrorCode(err, "answer_request_conflict") {
			t.Fatalf("replay with actor %q error = %v, want answer_request_conflict", actor, err)
		}
		var serviceErr *ServiceError
		if !errors.As(err, &serviceErr) || serviceErr.Status != 409 {
			t.Fatalf("replay with actor %q status = %#v, want 409", actor, err)
		}
	}
	if stored, found := storedAnswerActor(t, store, work.ID, answer.RequestID); !found || stored != "overseer" {
		t.Fatalf("stored actor after conflicting replays = %q (found %v), want overseer", stored, found)
	}
	// An operator answer replayed with an absent actor still matches, because
	// both resolve to the same label before comparison.
	second, _, _, secondWork := needsInputWork(t)
	first, err := second.AnswerWork(context.Background(), secondWork.ID, protocol.WorkAnswerRequest{
		RequestID: "63000000-0000-4000-8000-000000000007", Message: "Keep the alias.", Actor: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := second.AnswerWork(context.Background(), secondWork.ID, protocol.WorkAnswerRequest{
		RequestID: first.RequestID, Message: first.Message,
	})
	if err != nil || replayed.ID != first.ID || replayed.Actor != "operator" {
		t.Fatalf("operator replay without actor = %#v, error %v", replayed, err)
	}
}

// TestMigration040LabelsExistingAnswersOperator is the migration test,
// written against behaviour rather than DDL text. Every answer that existed
// when migration 040 ran was given by the operator, so the answer column
// defaults to that label for a row written without it, and the Work
// projection is backfilled wherever an answer is present. The NULL writes
// prove the NOT NULL half. The second part stages a genuine pre-040 database
// by dropping the two columns from a store holding a real answered Work and
// re-running the migration against it, which is the only way to exercise the
// backfill against rows the migration did not create.
func TestMigration040LabelsExistingAnswersOperator(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	ctx := context.Background()
	answer, err := store.AnswerWork(ctx, work.ID, protocol.WorkAnswerRequest{
		RequestID: "63000000-0000-4000-8000-000000000008", Message: "Keep the alias.", Actor: "overseer",
	})
	if err != nil {
		t.Fatal(err)
	}
	var actorDefault string
	if err := store.db.QueryRowContext(ctx, `
		SELECT dflt_value FROM pragma_table_info('work_answers') WHERE name = 'actor'
	`).Scan(&actorDefault); err != nil {
		t.Fatal(err)
	}
	if actorDefault != "'operator'" {
		t.Fatalf("work_answers.actor default = %s, want 'operator'", actorDefault)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE work_answers SET actor = NULL WHERE id = ?`, answer.ID); err == nil {
		t.Fatal("work_answers.actor accepted NULL; the column is not NOT NULL")
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE sessions SET answered_by = NULL WHERE id = ?`, work.ID); err == nil {
		t.Fatal("sessions.answered_by accepted NULL; the column is not NOT NULL")
	}

	// Stage the pre-040 state: an answered Work and an unanswered one exist,
	// and neither column does. Replaying the migration must recreate them,
	// default the answer's actor to operator, and backfill answered_by only
	// where an answer is.
	unanswered := admitDraftForTest(t, store).WorkIDs[0]
	for _, statement := range []string{
		`ALTER TABLE work_answers DROP COLUMN actor`,
		`ALTER TABLE sessions DROP COLUMN answered_by`,
		`DELETE FROM schema_migrations WHERE version = 40`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if actor, found := storedAnswerActor(t, store, work.ID, answer.RequestID); !found || actor != "operator" {
		t.Fatalf("pre-040 answer actor after migration = %q (found %v), want operator", actor, found)
	}
	answered, err := store.Work(ctx, work.ID)
	if err != nil || answered.Answer != answer.Message || answered.AnsweredBy != "operator" {
		t.Fatalf("pre-040 answered Work after migration = %#v, error %v", answered, err)
	}
	// The continuation history the next attempt sees reads the same label
	// back, so a Work answered before 040 gets an answer row that says
	// operator, and the row is still trusted.
	prompt, err := store.continuationPrompt(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if header := strings.Index(prompt, "Prior Work history"); header < 0 ||
		!strings.Contains(prompt[header:], `"kind":"answer","actor":"operator"`) ||
		!strings.Contains(prompt[header:], `"trusted":true`) {
		t.Fatalf("pre-040 answer row in continuation history = %q", prompt)
	}
	untouched, err := store.Work(ctx, unanswered)
	if err != nil || untouched.Answer != "" || untouched.AnsweredBy != "" {
		t.Fatalf("Work without an answer after migration = %#v, error %v", untouched, err)
	}
}
