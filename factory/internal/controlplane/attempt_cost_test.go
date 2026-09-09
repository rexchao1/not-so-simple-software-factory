package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// attemptCostRow reads the six cost columns out of the attempt row itself, so
// these tests prove what was written rather than what a loader surfaced. Any
// nil pointer means the column is NULL: not measured.
type attemptCostRow struct {
	costUSD                                                                   *float64
	inputTokens, cacheCreationInputTokens, cacheReadInputTokens, outputTokens *int64
	models                                                                    *string
}

func storedAttemptCost(t *testing.T, store *Store, attemptID string) attemptCostRow {
	t.Helper()
	var row attemptCostRow
	err := store.db.QueryRowContext(context.Background(), `
		SELECT cost_usd, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, models
		FROM attempts WHERE id = ?
	`, attemptID).Scan(&row.costUSD, &row.inputTokens, &row.cacheCreationInputTokens,
		&row.cacheReadInputTokens, &row.outputTokens, &row.models)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (row attemptCostRow) allNull() bool {
	return row.costUSD == nil && row.inputTokens == nil && row.cacheCreationInputTokens == nil &&
		row.cacheReadInputTokens == nil && row.outputTokens == nil && row.models == nil
}

// measuredCost is a completion payload with every value present, at a
// precision that would expose any rounding on the way through storage.
func measuredCost() (float64, protocol.Usage, map[string]protocol.ModelUsage) {
	usage := protocol.Usage{
		InputTokens: 1200, CacheCreationInputTokens: 300, CacheReadInputTokens: 45000, OutputTokens: 850,
	}
	models := map[string]protocol.ModelUsage{
		"claude-fable-5-1": {
			InputTokens: 1000, CacheCreationInputTokens: 300, CacheReadInputTokens: 45000, OutputTokens: 800,
			CostUSD: 0.12,
		},
		"claude-haiku-4-5-20251001": {
			InputTokens: 200, OutputTokens: 50, CostUSD: 0.003456789012345,
		},
	}
	return 0.123456789012345, usage, models
}

// runningClaimedAttempt takes a Claude Code task through admission, a claim,
// and the start report, leaving a running Attempt that a completion can land
// on. It returns the run so the caller can look the Attempt up through the
// run detail as well.
func runningClaimedAttempt(t *testing.T, store *Store, requestKey string) (protocol.Worker, protocol.RunDetail, *protocol.Claim) {
	t.Helper()
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Measure " + requestKey, Prompt: "Do the task.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: requestKey, LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	return worker, run, claim
}

func expectMeasured(t *testing.T, attempt protocol.Attempt, cost float64, usage protocol.Usage, models map[string]protocol.ModelUsage) {
	t.Helper()
	if attempt.CostUSD == nil || *attempt.CostUSD != cost {
		t.Fatalf("attempt cost = %v, want %v", attempt.CostUSD, cost)
	}
	if attempt.Usage == nil || *attempt.Usage != usage {
		t.Fatalf("attempt usage = %#v, want %#v", attempt.Usage, usage)
	}
	if len(attempt.Models) != len(models) {
		t.Fatalf("attempt models = %#v, want %#v", attempt.Models, models)
	}
	for name, want := range models {
		if got, ok := attempt.Models[name]; !ok || got != want {
			t.Fatalf("attempt models[%q] = %#v, want %#v", name, got, want)
		}
	}
}

func expectUnmeasured(t *testing.T, attempt protocol.Attempt) {
	t.Helper()
	if attempt.CostUSD != nil || attempt.Usage != nil || attempt.Models != nil {
		t.Fatalf("attempt carries a cost it was never given: %#v", attempt)
	}
}

// TestCompleteAttemptStoresCost proves the three values land on the row for
// every terminal state, at the precision reported, and that a replayed
// completion returns what was stored without overwriting it.
func TestCompleteAttemptStoresCost(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			_, _, claim := runningClaimedAttempt(t, store, "stores-cost-"+state)
			cost, usage, models := measuredCost()
			attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
				LeaseToken: tokenA, State: state, Result: "done", Error: "",
				CostUSD: &cost, Usage: &usage, Models: models,
			})
			if err != nil {
				t.Fatal(err)
			}
			if attempt.State != state {
				t.Fatalf("attempt state = %q, want %q", attempt.State, state)
			}
			expectMeasured(t, attempt, cost, usage, models)

			row := storedAttemptCost(t, store, attempt.ID)
			if row.costUSD == nil || *row.costUSD != cost ||
				row.inputTokens == nil || *row.inputTokens != usage.InputTokens ||
				row.cacheCreationInputTokens == nil || *row.cacheCreationInputTokens != usage.CacheCreationInputTokens ||
				row.cacheReadInputTokens == nil || *row.cacheReadInputTokens != usage.CacheReadInputTokens ||
				row.outputTokens == nil || *row.outputTokens != usage.OutputTokens || row.models == nil {
				t.Fatalf("stored cost row = %#v", row)
			}
			var storedModels map[string]protocol.ModelUsage
			if err := json.Unmarshal([]byte(*row.models), &storedModels); err != nil {
				t.Fatal(err)
			}
			if len(storedModels) != len(models) || storedModels["claude-fable-5-1"] != models["claude-fable-5-1"] {
				t.Fatalf("stored models = %s", *row.models)
			}

			// A replay with a different cost is answered from the row, not
			// the request.
			other := cost * 10
			replayed, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
				LeaseToken: tokenA, State: state, Result: "done", CostUSD: &other,
			})
			if err != nil {
				t.Fatal(err)
			}
			expectMeasured(t, replayed, cost, usage, models)
			if again := storedAttemptCost(t, store, attempt.ID); again.costUSD == nil || *again.costUSD != cost {
				t.Fatalf("replay overwrote the stored cost: %#v", again)
			}
		})
	}
}

// TestCompleteAttemptWithoutCostLeavesNull pins NULL, not zero, as the answer
// for a completion that carries none of the values: a Codex or Pi attempt.
func TestCompleteAttemptWithoutCostLeavesNull(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _, claim := runningClaimedAttempt(t, store, "without-cost")
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	expectUnmeasured(t, attempt)
	if row := storedAttemptCost(t, store, attempt.ID); !row.allNull() {
		t.Fatalf("completion without cost wrote cost columns: %#v", row)
	}
	fetched, err := store.Attempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectUnmeasured(t, fetched)
	encoded, err := json.Marshal(fetched)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"cost_usd"`, `"usage"`, `"models"`} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("unmeasured attempt serialized %s: %s", key, encoded)
		}
	}
}

// TestCompleteAttemptStoresUsageWithoutCost proves usage alone writes exactly
// the four counts and leaves cost and models NULL.
func TestCompleteAttemptStoresUsageWithoutCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _, claim := runningClaimedAttempt(t, store, "usage-only")
	usage := protocol.Usage{InputTokens: 10, CacheCreationInputTokens: 0, CacheReadInputTokens: 0, OutputTokens: 7}
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "done", Usage: &usage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.CostUSD != nil || attempt.Models != nil || attempt.Usage == nil || *attempt.Usage != usage {
		t.Fatalf("usage-only attempt = %#v", attempt)
	}
	row := storedAttemptCost(t, store, attempt.ID)
	if row.costUSD != nil || row.models != nil ||
		row.inputTokens == nil || *row.inputTokens != 10 ||
		row.cacheCreationInputTokens == nil || *row.cacheCreationInputTokens != 0 ||
		row.cacheReadInputTokens == nil || *row.cacheReadInputTokens != 0 ||
		row.outputTokens == nil || *row.outputTokens != 7 {
		t.Fatalf("usage-only row = %#v", row)
	}
}

// TestCompleteAttemptRejectsNegativeCost covers every shape invalid_cost
// rejects, and proves a rejected completion stores nothing and leaves the
// Attempt running for the next one.
func TestCompleteAttemptRejectsNegativeCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _, claim := runningClaimedAttempt(t, store, "rejects-cost")
	negative := -0.01
	zero := 0.0
	// Each entry serializes to well over 100 bytes, so 100 of them pass the
	// 8192-byte bound by a wide margin.
	oversized := map[string]protocol.ModelUsage{}
	for i := 0; i < 100; i++ {
		oversized[fmt.Sprintf("claude-model-%03d", i)] = protocol.ModelUsage{}
	}
	if encoded, _ := json.Marshal(oversized); len(encoded) <= maxModelsBytes {
		t.Fatalf("oversized fixture is only %d bytes", len(encoded))
	}
	cases := []struct {
		name  string
		input protocol.CompleteAttemptRequest
	}{
		{"negative cost", protocol.CompleteAttemptRequest{CostUSD: &negative}},
		{"negative input tokens", protocol.CompleteAttemptRequest{Usage: &protocol.Usage{InputTokens: -1}}},
		{"negative cache creation tokens", protocol.CompleteAttemptRequest{Usage: &protocol.Usage{CacheCreationInputTokens: -1}}},
		{"negative cache read tokens", protocol.CompleteAttemptRequest{Usage: &protocol.Usage{CacheReadInputTokens: -1}}},
		{"negative output tokens", protocol.CompleteAttemptRequest{Usage: &protocol.Usage{OutputTokens: -1}}},
		{"empty models", protocol.CompleteAttemptRequest{CostUSD: &zero, Models: map[string]protocol.ModelUsage{}}},
		{"empty model key", protocol.CompleteAttemptRequest{Models: map[string]protocol.ModelUsage{"": {}}}},
		{"negative model cost", protocol.CompleteAttemptRequest{Models: map[string]protocol.ModelUsage{"claude-fable-5-1": {CostUSD: -1}}}},
		{"negative model count", protocol.CompleteAttemptRequest{Models: map[string]protocol.ModelUsage{"claude-fable-5-1": {OutputTokens: -1}}}},
		{"oversized models", protocol.CompleteAttemptRequest{Models: oversized}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			input.LeaseToken, input.State, input.Result = tokenA, "succeeded", "done"
			_, err := store.CompleteAttempt(ctx, claim.Attempt.ID, input)
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != "invalid_cost" || service.Status != http.StatusBadRequest {
				t.Fatalf("CompleteAttempt error = %v, want 400 invalid_cost", err)
			}
			attempt, err := store.Attempt(ctx, claim.Attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.State != "running" {
				t.Fatalf("rejected completion moved the attempt to %q", attempt.State)
			}
			expectUnmeasured(t, attempt)
			if row := storedAttemptCost(t, store, attempt.ID); !row.allNull() {
				t.Fatalf("rejected completion stored something: %#v", row)
			}
		})
	}
	// The Attempt is still open to a valid completion.
	cost, usage, models := measuredCost()
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "done", CostUSD: &cost, Usage: &usage, Models: models,
	})
	if err != nil || attempt.State != "succeeded" {
		t.Fatalf("completion after rejections = %#v, error %v", attempt, err)
	}
	expectMeasured(t, attempt, cost, usage, models)
}

// TestCompleteAttemptKeepsCostWhenCancelled takes the operator-cancel path,
// which clears the result and error of a late completion. The cost is not
// cleared: the tokens were spent whether or not anyone wants the answer.
func TestCompleteAttemptKeepsCostWhenCancelled(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningClaimedAttempt(t, store, "cancelled-cost")
	if _, err := store.CancelSession(ctx, run.Run.ID, run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	cost, usage, models := measuredCost()
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "late success", Error: "",
		CostUSD: &cost, Usage: &usage, Models: models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != "cancelled" || attempt.Result != "" || attempt.Error != "" {
		t.Fatalf("late completion after cancellation = %#v", attempt)
	}
	expectMeasured(t, attempt, cost, usage, models)
	if row := storedAttemptCost(t, store, attempt.ID); row.costUSD == nil || *row.costUSD != cost || row.models == nil {
		t.Fatalf("cancellation cleared the cost: %#v", row)
	}
}

// TestRunDetailCarriesAttemptCost proves the run detail's attempt select
// reads the same six columns.
func TestRunDetailCarriesAttemptCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningClaimedAttempt(t, store, "run-detail-cost")
	cost, usage, models := measuredCost()
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "done", CostUSD: &cost, Usage: &usage, Models: models,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.Run(ctx, run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sessions) != 1 || len(detail.Sessions[0].Attempts) != 1 {
		t.Fatalf("run detail = %#v", detail)
	}
	expectMeasured(t, detail.Sessions[0].Attempts[0], cost, usage, models)
}

// TestCompleteAttemptHTTPCarriesCost pins the wire shape: snake-case counts
// in, the same on the stored Attempt out, and invalid_cost as a 400.
func TestCompleteAttemptHTTPCarriesCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, _, claim := runningClaimedAttempt(t, store, "http-cost")
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	complete := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(ctx, http.MethodPost,
			"http://localhost/api/v1/attempts/"+claim.Attempt.ID+"/complete", strings.NewReader(body))
		request.Host = "localhost"
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	rejected := complete(`{"lease_token":"` + tokenA + `","state":"succeeded","result":"done","cost_usd":-1}`)
	var failure protocol.ErrorBody
	if rejected.Code != http.StatusBadRequest || json.Unmarshal(rejected.Body.Bytes(), &failure) != nil ||
		failure.Error.Code != "invalid_cost" {
		t.Fatalf("negative cost status %d: %s", rejected.Code, rejected.Body.String())
	}

	response := complete(`{
		"lease_token": "` + tokenA + `", "state": "succeeded", "result": "done",
		"cost_usd": 0.0421,
		"usage": {"input_tokens": 12, "cache_creation_input_tokens": 3, "cache_read_input_tokens": 400, "output_tokens": 9},
		"models": {"claude-fable-5-1": {"input_tokens": 12, "cache_creation_input_tokens": 3,
		                                 "cache_read_input_tokens": 400, "output_tokens": 9, "cost_usd": 0.0421}}
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("complete status %d: %s", response.Code, response.Body.String())
	}
	var attempt protocol.Attempt
	if err := json.Unmarshal(response.Body.Bytes(), &attempt); err != nil {
		t.Fatal(err)
	}
	expectMeasured(t, attempt, 0.0421,
		protocol.Usage{InputTokens: 12, CacheCreationInputTokens: 3, CacheReadInputTokens: 400, OutputTokens: 9},
		map[string]protocol.ModelUsage{"claude-fable-5-1": {
			InputTokens: 12, CacheCreationInputTokens: 3, CacheReadInputTokens: 400, OutputTokens: 9, CostUSD: 0.0421,
		}})
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["cost_usd"]) != "0.0421" ||
		!bytes.Contains(wire["usage"], []byte(`"cache_creation_input_tokens":3`)) ||
		!bytes.Contains(wire["models"], []byte(`"cache_read_input_tokens":400`)) {
		t.Fatalf("completion response wire shape: %s", response.Body.String())
	}

	fetch := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"http://localhost/api/v1/attempts/"+claim.Attempt.ID, nil)
	fetch.Host = "localhost"
	fetched := httptest.NewRecorder()
	handler.ServeHTTP(fetched, fetch)
	var again protocol.Attempt
	if fetched.Code != http.StatusOK || json.Unmarshal(fetched.Body.Bytes(), &again) != nil {
		t.Fatalf("attempt status %d: %s", fetched.Code, fetched.Body.String())
	}
	if again.CostUSD == nil || *again.CostUSD != 0.0421 || again.Usage == nil || len(again.Models) != 1 {
		t.Fatalf("single attempt route lost the cost: %#v", again)
	}
}

// TestClaimAfterMigration041 exists because the claim select feeds
// scanAttempt: a missed column there is a scan arity error on every claim,
// and no worker could claim at all.
func TestClaimAfterMigration041(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var applied int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 41`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration 041 applied %d times", applied)
	}
	_, _, claim := runningClaimedAttempt(t, store, "claim-after-041")
	if claim.Attempt.State != "preparing" {
		t.Fatalf("claimed attempt = %#v", claim.Attempt)
	}
	expectUnmeasured(t, claim.Attempt)
}

// TestMigration041LeavesExistingAttemptsUnmeasured is the migration test,
// written against behaviour: an Attempt row written the way every pre-041
// INSERT was carries NULL in all six columns and surfaces no cost, on the
// single attempt route and in the run detail. The CHECK constraints hold the
// line below the API as well.
func TestMigration041LeavesExistingAttemptsUnmeasured(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Legacy attempt", Prompt: "Do the task.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "legacy-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	var executionID string
	if err := store.db.QueryRowContext(ctx,
		`SELECT id FROM executions WHERE session_id = ?`, run.Sessions[0].ID).Scan(&executionID); err != nil {
		t.Fatal(err)
	}
	// The column list deliberately omits the six cost columns, the way every
	// pre-041 INSERT did.
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO attempts(id, execution_id, worker_id, attempt_number, state,
		                     lease_digest, lease_expires_at, result, completed_at, created_at)
		VALUES ('attempt-pre-041', ?, ?, 1, 'succeeded', X'01', 0, 'done', 2, 1)
	`, executionID, worker.ID); err != nil {
		t.Fatal(err)
	}
	if row := storedAttemptCost(t, store, "attempt-pre-041"); !row.allNull() {
		t.Fatalf("pre-041 attempt row = %#v, want all NULL", row)
	}
	attempt, err := store.Attempt(ctx, "attempt-pre-041")
	if err != nil {
		t.Fatal(err)
	}
	expectUnmeasured(t, attempt)
	detail, err := store.Run(ctx, run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sessions[0].Attempts) != 1 {
		t.Fatalf("run detail attempts = %#v", detail.Sessions[0].Attempts)
	}
	expectUnmeasured(t, detail.Sessions[0].Attempts[0])

	// A stage row written the way every pre-041 INSERT was: the column list
	// omits the six cost columns, so the stage surfaces no cost on the Work's
	// stage list and none of the three keys in its JSON.
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO session_stages(session_id, position, name, kind, prompt, state, result, started_at, completed_at)
		VALUES (?, 1, 'Legacy stage', 'agent', 'Review it.', 'succeeded', 'done', 1, 2)
	`, run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if row := storedStageCost(t, store, run.Sessions[0].ID, 1); !row.allNull() {
		t.Fatalf("pre-041 stage row = %#v, want all NULL", row)
	}
	work, err := store.Work(ctx, run.Sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(work.Stages) != 2 || work.Stages[1].Name != "Legacy stage" {
		t.Fatalf("Work stages = %#v", work.Stages)
	}
	expectStageUnmeasured(t, work.Stages[1])
	encodedStage, err := json.Marshal(work.Stages[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"cost_usd"`, `"usage"`, `"models"`} {
		if bytes.Contains(encodedStage, []byte(key)) {
			t.Fatalf("pre-041 stage serialized %s: %s", key, encodedStage)
		}
	}

	for _, table := range []string{"attempts", "session_stages"} {
		var columns int
		if err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pragma_table_info(?) WHERE name IN
			('cost_usd', 'input_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens', 'output_tokens', 'models')
		`, table).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if columns != 6 {
			t.Fatalf("%s carries %d of the six cost columns", table, columns)
		}
	}
	for _, statement := range []string{
		`UPDATE attempts SET cost_usd = -0.5 WHERE id = 'attempt-pre-041'`,
		`UPDATE attempts SET output_tokens = -1 WHERE id = 'attempt-pre-041'`,
		`UPDATE attempts SET models = 'not json' WHERE id = 'attempt-pre-041'`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("%s: accepted by the CHECK constraint", statement)
		}
	}
}
