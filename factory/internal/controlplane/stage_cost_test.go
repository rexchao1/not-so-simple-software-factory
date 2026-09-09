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

// storedStageCost reads the six cost columns out of the stage row itself, in
// the shape storedAttemptCost reads the attempt row, so these tests prove what
// was written rather than what a loader surfaced.
func storedStageCost(t *testing.T, store *Store, workID string, position int) attemptCostRow {
	t.Helper()
	var row attemptCostRow
	err := store.db.QueryRowContext(context.Background(), `
		SELECT cost_usd, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, models
		FROM session_stages WHERE session_id = ? AND position = ?
	`, workID, position).Scan(&row.costUSD, &row.inputTokens, &row.cacheCreationInputTokens,
		&row.cacheReadInputTokens, &row.outputTokens, &row.models)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// runningPipelineAttempt takes a two-stage Pipeline task through admission, a
// claim, and the start report, leaving a running Attempt whose stages can be
// started and completed.
func runningPipelineAttempt(t *testing.T, store *Store, requestKey string, contract protocol.OutcomeContract) (protocol.Worker, protocol.RunDetail, *protocol.Claim) {
	t.Helper()
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{
		Name: "Build then review " + requestKey,
		Stages: []protocol.PipelineStage{
			{Name: "Build", Prompt: "Implement {{ task.prompt }}"},
			{Name: "Review", Prompt: "Review the implementation."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Measure " + requestKey, Prompt: "Do the task.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
		OutcomeContract: contract,
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

func expectStageMeasured(t *testing.T, stage protocol.StageRun, cost float64, usage protocol.Usage, models map[string]protocol.ModelUsage) {
	t.Helper()
	if stage.CostUSD == nil || *stage.CostUSD != cost {
		t.Fatalf("stage %d cost = %v, want %v", stage.Position, stage.CostUSD, cost)
	}
	if stage.Usage == nil || *stage.Usage != usage {
		t.Fatalf("stage %d usage = %#v, want %#v", stage.Position, stage.Usage, usage)
	}
	if len(stage.Models) != len(models) {
		t.Fatalf("stage %d models = %#v, want %#v", stage.Position, stage.Models, models)
	}
	for name, want := range models {
		if got, ok := stage.Models[name]; !ok || got != want {
			t.Fatalf("stage %d models[%q] = %#v, want %#v", stage.Position, name, got, want)
		}
	}
}

func expectStageUnmeasured(t *testing.T, stage protocol.StageRun) {
	t.Helper()
	if stage.CostUSD != nil || stage.Usage != nil || stage.Models != nil {
		t.Fatalf("stage %d carries a cost it was never given: %#v", stage.Position, stage)
	}
}

// workStages reads the Work's stage list, the run detail's copy of it, and
// the stored rows, and returns the Work's list once the two loaders agree.
func workStages(t *testing.T, store *Store, runID, workID string) []protocol.StageRun {
	t.Helper()
	ctx := context.Background()
	work, err := store.Work(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.Run(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var summaries []protocol.StageRun
	for _, session := range detail.Sessions {
		if session.ID == workID {
			summaries = session.Stages
		}
	}
	if len(summaries) != len(work.Stages) {
		t.Fatalf("run detail stages = %#v, Work stages = %#v", summaries, work.Stages)
	}
	for i := range work.Stages {
		full, summary := work.Stages[i], summaries[i]
		if (full.CostUSD == nil) != (summary.CostUSD == nil) || (full.Usage == nil) != (summary.Usage == nil) ||
			len(full.Models) != len(summary.Models) {
			t.Fatalf("run detail stage %d = %#v, Work stage = %#v", i, summary, full)
		}
		if full.CostUSD != nil && (*full.CostUSD != *summary.CostUSD || *full.Usage != *summary.Usage) {
			t.Fatalf("run detail stage %d = %#v, Work stage = %#v", i, summary, full)
		}
	}
	return work.Stages
}

// TestCompleteStageStoresCost proves the three values land on the stage row
// for every terminal state, at the precision reported, and that every stage
// loader reads them back.
func TestCompleteStageStoresCost(t *testing.T) {
	for _, state := range []protocol.StageRunState{protocol.StageSucceeded, protocol.StageFailed, protocol.StageCancelled} {
		t.Run(string(state), func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			_, run, claim := runningPipelineAttempt(t, store, "stage-stores-cost-"+string(state), "")
			workID := run.Sessions[0].ID
			if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
				t.Fatal(err)
			}
			cost, usage, models := measuredCost()
			stage, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
				LeaseToken: tokenA, State: state, Result: "done", CostUSD: &cost, Usage: &usage, Models: models,
			})
			if err != nil {
				t.Fatal(err)
			}
			if stage.State != state {
				t.Fatalf("stage state = %q, want %q", stage.State, state)
			}
			expectStageMeasured(t, stage, cost, usage, models)

			row := storedStageCost(t, store, workID, 0)
			if row.costUSD == nil || *row.costUSD != cost ||
				row.inputTokens == nil || *row.inputTokens != usage.InputTokens ||
				row.cacheCreationInputTokens == nil || *row.cacheCreationInputTokens != usage.CacheCreationInputTokens ||
				row.cacheReadInputTokens == nil || *row.cacheReadInputTokens != usage.CacheReadInputTokens ||
				row.outputTokens == nil || *row.outputTokens != usage.OutputTokens || row.models == nil {
				t.Fatalf("stored stage cost row = %#v", row)
			}
			var storedModels map[string]protocol.ModelUsage
			if err := json.Unmarshal([]byte(*row.models), &storedModels); err != nil {
				t.Fatal(err)
			}
			if len(storedModels) != len(models) || storedModels["claude-fable-5-1"] != models["claude-fable-5-1"] {
				t.Fatalf("stored models = %s", *row.models)
			}
			// The second stage never ran, so it stays unmeasured whichever way
			// the first one ended.
			if second := storedStageCost(t, store, workID, 1); !second.allNull() {
				t.Fatalf("untouched stage row = %#v", second)
			}

			stages := workStages(t, store, run.Run.ID, workID)
			if len(stages) != 2 {
				t.Fatalf("Work stages = %#v", stages)
			}
			expectStageMeasured(t, stages[0], cost, usage, models)
			expectStageUnmeasured(t, stages[1])
			single, err := store.stageRun(ctx, workID, 0)
			if err != nil {
				t.Fatal(err)
			}
			expectStageMeasured(t, single, cost, usage, models)
		})
	}
}

// TestCompleteStageWithoutCostLeavesNull pins NULL, not zero, as the answer
// for a stage completion that carries none of the values: a Codex, Pi, or
// code stage.
func TestCompleteStageWithoutCostLeavesNull(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningPipelineAttempt(t, store, "stage-without-cost", "")
	workID := run.Sessions[0].ID
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	stage, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded, Result: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	expectStageUnmeasured(t, stage)
	if row := storedStageCost(t, store, workID, 0); !row.allNull() {
		t.Fatalf("completion without cost wrote cost columns: %#v", row)
	}
	stages := workStages(t, store, run.Run.ID, workID)
	expectStageUnmeasured(t, stages[0])
	encoded, err := json.Marshal(stages[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"cost_usd"`, `"usage"`, `"models"`} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("unmeasured stage serialized %s: %s", key, encoded)
		}
	}
}

// TestCompleteStageRejectsNegativeCost covers every shape invalid_cost
// rejects on a stage, and proves a rejected completion stores nothing and
// leaves the stage running for the next one.
func TestCompleteStageRejectsNegativeCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningPipelineAttempt(t, store, "stage-rejects-cost", "")
	workID := run.Sessions[0].ID
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	negative := -0.01
	zero := 0.0
	oversized := map[string]protocol.ModelUsage{}
	for i := 0; i < 100; i++ {
		oversized[fmt.Sprintf("claude-model-%03d", i)] = protocol.ModelUsage{}
	}
	if encoded, _ := json.Marshal(oversized); len(encoded) <= maxModelsBytes {
		t.Fatalf("oversized fixture is only %d bytes", len(encoded))
	}
	cases := []struct {
		name  string
		input protocol.CompleteStageRequest
	}{
		{"negative cost", protocol.CompleteStageRequest{CostUSD: &negative}},
		{"negative input tokens", protocol.CompleteStageRequest{Usage: &protocol.Usage{InputTokens: -1}}},
		{"negative cache creation tokens", protocol.CompleteStageRequest{Usage: &protocol.Usage{CacheCreationInputTokens: -1}}},
		{"negative cache read tokens", protocol.CompleteStageRequest{Usage: &protocol.Usage{CacheReadInputTokens: -1}}},
		{"negative output tokens", protocol.CompleteStageRequest{Usage: &protocol.Usage{OutputTokens: -1}}},
		{"empty models", protocol.CompleteStageRequest{CostUSD: &zero, Models: map[string]protocol.ModelUsage{}}},
		{"empty model key", protocol.CompleteStageRequest{Models: map[string]protocol.ModelUsage{"": {}}}},
		{"negative model cost", protocol.CompleteStageRequest{Models: map[string]protocol.ModelUsage{"claude-fable-5-1": {CostUSD: -1}}}},
		{"negative model count", protocol.CompleteStageRequest{Models: map[string]protocol.ModelUsage{"claude-fable-5-1": {OutputTokens: -1}}}},
		{"oversized models", protocol.CompleteStageRequest{Models: oversized}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			input.LeaseToken, input.State, input.Result = tokenA, protocol.StageSucceeded, "done"
			_, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, input)
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != "invalid_cost" || service.Status != http.StatusBadRequest {
				t.Fatalf("CompleteStage error = %v, want 400 invalid_cost", err)
			}
			stage, err := store.stageRun(ctx, workID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if stage.State != protocol.StageRunning {
				t.Fatalf("rejected completion moved the stage to %q", stage.State)
			}
			expectStageUnmeasured(t, stage)
			if row := storedStageCost(t, store, workID, 0); !row.allNull() {
				t.Fatalf("rejected completion stored something: %#v", row)
			}
		})
	}
	// The stage is still open to a valid completion.
	cost, usage, models := measuredCost()
	stage, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded, Result: "done", CostUSD: &cost, Usage: &usage, Models: models,
	})
	if err != nil || stage.State != protocol.StageSucceeded {
		t.Fatalf("completion after rejections = %#v, error %v", stage, err)
	}
	expectStageMeasured(t, stage, cost, usage, models)
}

// TestCompleteStageReplayKeepsCost proves the idempotent replay compares the
// cost columns too: the same values return the stored stage, and a different
// cost, or none, is stage_not_running.
func TestCompleteStageReplayKeepsCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningPipelineAttempt(t, store, "stage-replay-cost", "")
	workID := run.Sessions[0].ID
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	cost, usage, models := measuredCost()
	complete := func(input protocol.CompleteStageRequest) (protocol.StageRun, error) {
		input.LeaseToken, input.State, input.Result = tokenA, protocol.StageSucceeded, "done"
		return store.CompleteStage(ctx, claim.Attempt.ID, 0, input)
	}
	if _, err := complete(protocol.CompleteStageRequest{CostUSD: &cost, Usage: &usage, Models: models}); err != nil {
		t.Fatal(err)
	}
	replayed, err := complete(protocol.CompleteStageRequest{CostUSD: &cost, Usage: &usage, Models: models})
	if err != nil {
		t.Fatalf("replay with the same values: %v", err)
	}
	expectStageMeasured(t, replayed, cost, usage, models)

	other := cost * 10
	otherUsage := usage
	otherUsage.OutputTokens++
	otherModels := map[string]protocol.ModelUsage{"claude-fable-5-1": models["claude-fable-5-1"]}
	for name, input := range map[string]protocol.CompleteStageRequest{
		"different cost":   {CostUSD: &other, Usage: &usage, Models: models},
		"different usage":  {CostUSD: &cost, Usage: &otherUsage, Models: models},
		"different models": {CostUSD: &cost, Usage: &usage, Models: otherModels},
		"no cost":          {},
	} {
		_, err := complete(input)
		var service *ServiceError
		if !errors.As(err, &service) || service.Code != "stage_not_running" || service.Status != http.StatusConflict {
			t.Fatalf("%s replay error = %v, want 409 stage_not_running", name, err)
		}
	}
	if row := storedStageCost(t, store, workID, 0); row.costUSD == nil || *row.costUSD != cost {
		t.Fatalf("replay overwrote the stored cost: %#v", row)
	}
	again, err := store.stageRun(ctx, workID, 0)
	if err != nil {
		t.Fatal(err)
	}
	expectStageMeasured(t, again, cost, usage, models)
}

// TestRetryClearsStageCost proves a retry sets the six columns back to NULL
// on every stage while the earlier attempt row keeps its sum: the stage row
// belongs to the Work and holds only its most recent run.
func TestRetryClearsStageCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningPipelineAttempt(t, store, "stage-retry-cost", "")
	workID := run.Sessions[0].ID
	cost, usage, models := measuredCost()
	for position, state := range []protocol.StageRunState{protocol.StageSucceeded, protocol.StageFailed} {
		if _, err := store.StartStage(ctx, claim.Attempt.ID, position, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteStage(ctx, claim.Attempt.ID, position, protocol.CompleteStageRequest{
			LeaseToken: tokenA, State: state, Result: "done", CostUSD: &cost, Usage: &usage, Models: models,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sum := cost * 2
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "review failed", CostUSD: &sum, Usage: &usage, Models: models,
	})
	if err != nil || attempt.State != "failed" {
		t.Fatalf("attempt = %#v, error %v", attempt, err)
	}
	for position := 0; position < 2; position++ {
		if row := storedStageCost(t, store, workID, position); row.costUSD == nil || *row.costUSD != cost {
			t.Fatalf("stage %d before retry = %#v", position, row)
		}
	}

	detail, err := store.RetrySession(ctx, run.Run.ID, workID)
	if err != nil {
		t.Fatal(err)
	}
	for position := 0; position < 2; position++ {
		if row := storedStageCost(t, store, workID, position); !row.allNull() {
			t.Fatalf("retry left stage %d measured: %#v", position, row)
		}
	}
	for _, stage := range detail.Sessions[0].Stages {
		if stage.State != protocol.StagePending {
			t.Fatalf("retried stage = %#v", stage)
		}
		expectStageUnmeasured(t, stage)
	}
	for _, stage := range workStages(t, store, run.Run.ID, workID) {
		expectStageUnmeasured(t, stage)
	}
	if row := storedAttemptCost(t, store, attempt.ID); row.costUSD == nil || *row.costUSD != sum || row.models == nil {
		t.Fatalf("retry touched the earlier attempt row: %#v", row)
	}
	earlier, err := store.Attempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectMeasured(t, earlier, sum, usage, models)
}

// TestAnswerClearsLastStageCost proves an answer resets the last stage only:
// its cost columns go back to NULL, the earlier stage keeps its cost, the
// attempt row keeps its sum, and the continuation claim reads the same.
func TestAnswerClearsLastStageCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker, run, claim := runningPipelineAttempt(t, store, "stage-answer-cost", protocol.OutcomeAgentUpdate)
	workID := run.Sessions[0].ID
	cost, usage, models := measuredCost()
	for position := 0; position < 2; position++ {
		if _, err := store.StartStage(ctx, claim.Attempt.ID, position, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
			t.Fatal(err)
		}
		if position == 1 {
			if _, err := store.AppendAgentUpdate(ctx, claim.Attempt.ID, protocol.AttemptUpdateRequest{
				LeaseToken: tokenA, RequestID: "66000000-0000-4000-8000-000000000001",
				Status: protocol.WorkUpdateNeedsInput, Message: "Which compatibility mode?",
				CheckpointSHA: testCheckpointSHA, CheckpointPublished: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.CompleteStage(ctx, claim.Attempt.ID, position, protocol.CompleteStageRequest{
			LeaseToken: tokenA, State: protocol.StageSucceeded, Result: "done",
			CostUSD: &cost, Usage: &usage, Models: models,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sum := cost * 2
	attempt, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", CostUSD: &sum, Usage: &usage, Models: models,
	})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.Work(ctx, workID)
	if err != nil || work.State != protocol.WorkNeedsInput {
		t.Fatalf("needs-input Work = %#v, error %v", work, err)
	}
	expectStageMeasured(t, work.Stages[0], cost, usage, models)
	expectStageMeasured(t, work.Stages[1], cost, usage, models)

	if _, err := store.AnswerWork(ctx, workID, protocol.WorkAnswerRequest{
		RequestID: "66000000-0000-4000-8000-000000000002", Message: "Preserve legacy mode.",
	}); err != nil {
		t.Fatal(err)
	}
	if row := storedStageCost(t, store, workID, 0); row.costUSD == nil || *row.costUSD != cost || row.models == nil {
		t.Fatalf("answer cleared the earlier stage: %#v", row)
	}
	if row := storedStageCost(t, store, workID, 1); !row.allNull() {
		t.Fatalf("answer left the last stage measured: %#v", row)
	}
	stages := workStages(t, store, run.Run.ID, workID)
	expectStageMeasured(t, stages[0], cost, usage, models)
	expectStageUnmeasured(t, stages[1])
	if row := storedAttemptCost(t, store, attempt.ID); row.costUSD == nil || *row.costUSD != sum || row.models == nil {
		t.Fatalf("answer touched the earlier attempt row: %#v", row)
	}

	resumed, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "stage-answer-cost-continuation", LeaseToken: resumeToken,
	})
	if err != nil || resumed == nil || len(resumed.Session.Stages) != 2 {
		t.Fatalf("continuation claim = %#v, error %v", resumed, err)
	}
	expectStageMeasured(t, resumed.Session.Stages[0], cost, usage, models)
	if resumed.Session.Stages[1].State != protocol.StagePending {
		t.Fatalf("continuation stages = %#v", resumed.Session.Stages)
	}
	expectStageUnmeasured(t, resumed.Session.Stages[1])
}

// TestCompleteStageHTTPCarriesCost pins the wire shape of the stage
// completion: snake-case counts in, the same on the stored stage out, and
// invalid_cost as a 400.
func TestCompleteStageHTTPCarriesCost(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, run, claim := runningPipelineAttempt(t, store, "stage-http-cost", "")
	workID := run.Sessions[0].ID
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	complete := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(ctx, http.MethodPost,
			"http://localhost/api/v1/attempts/"+claim.Attempt.ID+"/stages/0/complete", strings.NewReader(body))
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
	if stage, err := store.stageRun(ctx, workID, 0); err != nil || stage.State != protocol.StageRunning {
		t.Fatalf("rejected completion left the stage at %#v, error %v", stage, err)
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
	var stage protocol.StageRun
	if err := json.Unmarshal(response.Body.Bytes(), &stage); err != nil {
		t.Fatal(err)
	}
	wantUsage := protocol.Usage{InputTokens: 12, CacheCreationInputTokens: 3, CacheReadInputTokens: 400, OutputTokens: 9}
	wantModels := map[string]protocol.ModelUsage{"claude-fable-5-1": {
		InputTokens: 12, CacheCreationInputTokens: 3, CacheReadInputTokens: 400, OutputTokens: 9, CostUSD: 0.0421,
	}}
	expectStageMeasured(t, stage, 0.0421, wantUsage, wantModels)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["cost_usd"]) != "0.0421" ||
		!bytes.Contains(wire["usage"], []byte(`"cache_creation_input_tokens":3`)) ||
		!bytes.Contains(wire["models"], []byte(`"cache_read_input_tokens":400`)) {
		t.Fatalf("completion response wire shape: %s", response.Body.String())
	}

	fetch := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/runs/"+run.Run.ID, nil)
	fetch.Host = "localhost"
	fetched := httptest.NewRecorder()
	handler.ServeHTTP(fetched, fetch)
	var detail protocol.RunDetail
	if fetched.Code != http.StatusOK || json.Unmarshal(fetched.Body.Bytes(), &detail) != nil {
		t.Fatalf("run status %d: %s", fetched.Code, fetched.Body.String())
	}
	if len(detail.Sessions) != 1 || len(detail.Sessions[0].Stages) != 2 {
		t.Fatalf("run detail = %s", fetched.Body.String())
	}
	expectStageMeasured(t, detail.Sessions[0].Stages[0], 0.0421, wantUsage, wantModels)
	expectStageUnmeasured(t, detail.Sessions[0].Stages[1])
}
