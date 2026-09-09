package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/protocol"
)

// claudeCostHarness runs a real worker against a real control plane with a
// fake Claude Code in place of the model. The fake answers with a canned
// result event per invocation, which is what lets a Pipeline give each of its
// stages a different reported cost.
type claudeCostHarness struct {
	store        *controlplane.Store
	repositoryID string
	responses    string
}

// writeFakeClaudeCode answers the two probes the health check makes and then
// replays one canned stream-json response per real invocation, so stage N of a
// Pipeline gets the file named N. An invocation whose response has not been
// queued waits for it, which is what holds a stage open long enough for a test
// to cancel the attempt out from under it.
func writeFakeClaudeCode(t *testing.T, path, responses string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo claude-test
  exit 0
fi
if [ "${1:-}" = "auth" ]; then
  echo '{"loggedIn":true}'
  exit 0
fi
cat > /dev/null
counter="` + responses + `/count"
invocation=1
if [ -f "$counter" ]; then invocation=$(($(cat "$counter") + 1)); fi
printf '%s' "$invocation" > "$counter"
echo '{"type":"system","subtype":"init"}'
waited=0
while [ ! -f "` + responses + `/$invocation" ] && [ "$waited" -lt 300 ]; do
  sleep 0.1
  waited=$((waited + 1))
done
cat "` + responses + `/$invocation"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func startClaudeCostHarness(t *testing.T, logger *slog.Logger) *claudeCostHarness {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "factory-claude-cost-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	remote := filepath.Join(root, "remote.git")
	repositoryPath := filepath.Join(root, "repository")
	runTestCommand(t, root, "git", "init", "--bare", remote)
	runTestCommand(t, root, "git", "clone", remote, repositoryPath)
	runTestCommand(t, repositoryPath, "git", "config", "user.email", "factory@example.com")
	runTestCommand(t, repositoryPath, "git", "config", "user.name", "Factory Test")
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, repositoryPath, "git", "add", "README.md")
	runTestCommand(t, repositoryPath, "git", "commit", "-m", "test: seed repository")
	runTestCommand(t, repositoryPath, "git", "branch", "-M", "main")
	runTestCommand(t, repositoryPath, "git", "push", "-u", "origin", "main")
	runTestCommand(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/main")

	serverRoot, err := os.MkdirTemp("/tmp", "factory-claude-cost-server-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(serverRoot) })
	store, err := controlplane.Open(context.Background(), filepath.Join(serverRoot, "factory.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := httptest.NewServer(controlplane.NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	responses := filepath.Join(root, "responses")
	if err := os.MkdirAll(responses, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(root, "claude")
	writeFakeClaudeCode(t, fakeClaude, responses)
	t.Setenv("FACTORY_TEST_SUPERVISOR_HELPER", "1")
	options := testOptions(fakeClaude)
	options.HTTPClient = server.Client()
	options.SupervisorCommand = []string{os.Args[0], "-test.run=TestCodeStageSupervisorHelper"}
	options.PollInterval = 10 * time.Millisecond
	options.RegistrationInterval = 20 * time.Millisecond
	manager, err := New(Config{
		Server: server.URL, Name: "claude-cost", Runtime: protocol.RuntimeClaudeCode,
		MaxConcurrent: 1, DataDirectory: filepath.Join(root, "worker"),
		Repositories: map[string]RepositoryConfig{"factory": {Path: repositoryPath, BaseBranch: "main"}},
	}, options, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Manager shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Manager did not stop")
		}
	})

	var registered protocol.Worker
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		registered, err = store.Worker(context.Background(), manager.ID())
		if err == nil && len(registered.Repositories) == 1 && registered.Health == "healthy" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(registered.Repositories) != 1 || registered.Health != "healthy" {
		t.Fatalf("Worker registration = %#v, err %v", registered, err)
	}
	return &claudeCostHarness{store: store, repositoryID: registered.Repositories[0].ID, responses: responses}
}

// reply queues the result event the fake Claude Code prints on its invocation
// number, counting only the agent stages of the Pipeline.
func (harness *claudeCostHarness) reply(t *testing.T, invocation int, event string) {
	t.Helper()
	path := filepath.Join(harness.responses, strconv.Itoa(invocation))
	if err := os.WriteFile(path, []byte(event+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start queues the Pipeline and hands the run back without waiting for it, so
// a test can act on the attempt while it is still going.
func (harness *claudeCostHarness) start(
	t *testing.T,
	name string,
	stages []protocol.PipelineStage,
	requestKey string,
) protocol.RunDetail {
	t.Helper()
	pipeline, err := harness.store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: name, Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := harness.store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: name, Prompt: "Run the pipeline.", Runtime: protocol.RuntimeClaudeCode,
		PipelineID: pipeline.ID, RepositoryIDs: []string{harness.repositoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := harness.store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// await polls until the attempt reaches a terminal state, which is when the
// completion the test is about has been recorded.
func (harness *claudeCostHarness) await(t *testing.T, sessionID string) protocol.Work {
	t.Helper()
	var work protocol.Work
	var err error
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		work, err = harness.store.Work(context.Background(), sessionID)
		if err == nil && len(work.Attempts) == 1 && work.Attempts[0].State != "preparing" &&
			work.Attempts[0].State != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Work read failed: %v", err)
	}
	return work
}

// awaitStage polls until the stage at the given position reaches a state, so a
// test can act at a known point in a Pipeline that is still running.
func (harness *claudeCostHarness) awaitStage(t *testing.T, sessionID string, position int, state protocol.StageRunState) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		work, err := harness.store.Work(context.Background(), sessionID)
		if err == nil && len(work.Stages) > position && work.Stages[position].State == state {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("stage %d never reached %q", position, state)
}

func (harness *claudeCostHarness) run(
	t *testing.T,
	name string,
	stages []protocol.PipelineStage,
	requestKey string,
) protocol.Work {
	t.Helper()
	run := harness.start(t, name, stages, requestKey)
	return harness.await(t, run.Sessions[0].ID)
}

// resultEvent builds the terminal event Claude Code prints, with the cost
// fields spelled the way Claude spells them rather than the way the control
// plane stores them.
func resultEvent(isError bool, cost float64, usage protocol.Usage, models map[string]protocol.ModelUsage) string {
	event := `{"type":"result","result":"stage done","is_error":` + strconv.FormatBool(isError) +
		`,"total_cost_usd":` + formatCost(&cost) +
		`,"usage":{"input_tokens":` + strconv.FormatInt(usage.InputTokens, 10) +
		`,"cache_creation_input_tokens":` + strconv.FormatInt(usage.CacheCreationInputTokens, 10) +
		`,"cache_read_input_tokens":` + strconv.FormatInt(usage.CacheReadInputTokens, 10) +
		`,"output_tokens":` + strconv.FormatInt(usage.OutputTokens, 10) + `}`
	if len(models) == 0 {
		return event + "}"
	}
	entries := make([]string, 0, len(models))
	for name, model := range models {
		entries = append(entries, `"`+name+`":{"inputTokens":`+strconv.FormatInt(model.InputTokens, 10)+
			`,"cacheCreationInputTokens":`+strconv.FormatInt(model.CacheCreationInputTokens, 10)+
			`,"cacheReadInputTokens":`+strconv.FormatInt(model.CacheReadInputTokens, 10)+
			`,"outputTokens":`+strconv.FormatInt(model.OutputTokens, 10)+
			`,"costUSD":`+formatCost(&model.CostUSD)+`,"contextWindow":200000,"costBasis":"api"}`)
	}
	return event + `,"modelUsage":{` + strings.Join(entries, ",") + `}}`
}

func expectMeasured(
	t *testing.T,
	what string,
	cost *float64,
	usage *protocol.Usage,
	models map[string]protocol.ModelUsage,
	wantCost float64,
	wantUsage protocol.Usage,
	wantModels map[string]protocol.ModelUsage,
) {
	t.Helper()
	if cost == nil || *cost != wantCost {
		t.Fatalf("%s cost = %s, want %s", what, formatCost(cost), formatCost(&wantCost))
	}
	if usage == nil || *usage != wantUsage {
		t.Fatalf("%s usage = %+v, want %+v", what, usage, wantUsage)
	}
	if len(models) != len(wantModels) {
		t.Fatalf("%s models = %+v, want %+v", what, models, wantModels)
	}
	for name, want := range wantModels {
		if got, ok := models[name]; !ok || got != want {
			t.Fatalf("%s models[%q] = %+v, want %+v", what, name, got, want)
		}
	}
}

func expectUnmeasured(
	t *testing.T,
	what string,
	cost *float64,
	usage *protocol.Usage,
	models map[string]protocol.ModelUsage,
) {
	t.Helper()
	if cost != nil || usage != nil || models != nil {
		t.Fatalf("%s carries a cost it was never given: %s, %+v, %+v", what, formatCost(cost), usage, models)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// lockedBuffer is a log sink a test can read while the Manager is still
// writing to it. A test waits for the completion it is about, but the Manager
// goes on logging after that, so the read needs the same lock as the writes
// rather than a guess about when the last line lands.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(fragment []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(fragment)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

// TestRunAttemptReportsStageCost is the whole path in one run: Claude reports
// a cost, the worker puts it on the stage that spent it, and the attempt
// completion carries the same number because that stage is the only one.
func TestRunAttemptReportsStageCost(t *testing.T) {
	harness := startClaudeCostHarness(t, discardLogger())
	usage := protocol.Usage{
		InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44,
	}
	models := map[string]protocol.ModelUsage{"claude-opus-4": {
		InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44, CostUSD: 0.25,
	}}
	harness.reply(t, 1, resultEvent(false, 0.25, usage, models))

	work := harness.run(t, "One measured stage", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
	}, "claude-cost-single")

	if work.Attempts[0].State != "succeeded" {
		t.Fatalf("Attempt state = %q, want succeeded: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 1 {
		t.Fatalf("Pipeline stages = %#v, want one", work.Stages)
	}
	expectMeasured(t, "stage 0", work.Stages[0].CostUSD, work.Stages[0].Usage, work.Stages[0].Models,
		0.25, usage, models)
	expectMeasured(t, "attempt", work.Attempts[0].CostUSD, work.Attempts[0].Usage, work.Attempts[0].Models,
		0.25, usage, models)
}

// TestRunAttemptSumsStageCosts is the reason the sum lives on the handle: each
// stage keeps its own number, the attempt carries their total, and a model
// both stages used is one entry rather than two. The code stage in the middle
// spends nothing and must not disturb either.
func TestRunAttemptSumsStageCosts(t *testing.T) {
	harness := startClaudeCostHarness(t, discardLogger())
	build := protocol.Usage{
		InputTokens: 10, CacheCreationInputTokens: 20, CacheReadInputTokens: 30, OutputTokens: 40,
	}
	buildModels := map[string]protocol.ModelUsage{"claude-opus-4": {
		InputTokens: 10, CacheCreationInputTokens: 20, CacheReadInputTokens: 30, OutputTokens: 40, CostUSD: 0.25,
	}}
	review := protocol.Usage{
		InputTokens: 1, CacheCreationInputTokens: 2, CacheReadInputTokens: 3, OutputTokens: 4,
	}
	reviewModels := map[string]protocol.ModelUsage{
		"claude-opus-4": {
			InputTokens: 1, CacheCreationInputTokens: 2, CacheReadInputTokens: 3, OutputTokens: 4, CostUSD: 0.1,
		},
		"claude-haiku-4": {
			InputTokens: 5, CacheCreationInputTokens: 6, CacheReadInputTokens: 7, OutputTokens: 8, CostUSD: 0.15,
		},
	}
	harness.reply(t, 1, resultEvent(false, 0.25, build, buildModels))
	harness.reply(t, 2, resultEvent(false, 0.25, review, reviewModels))

	work := harness.run(t, "Two measured stages", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
		{Name: "Gate", Kind: protocol.StageKindCode, Command: "echo 'suite passed'"},
		{Name: "Review", Prompt: "Review the implementation."},
	}, "claude-cost-sum")

	if work.Attempts[0].State != "succeeded" {
		t.Fatalf("Attempt state = %q, want succeeded: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 3 {
		t.Fatalf("Pipeline stages = %#v, want three", work.Stages)
	}
	expectMeasured(t, "stage 0", work.Stages[0].CostUSD, work.Stages[0].Usage, work.Stages[0].Models,
		0.25, build, buildModels)
	expectUnmeasured(t, "code stage 1", work.Stages[1].CostUSD, work.Stages[1].Usage, work.Stages[1].Models)
	expectMeasured(t, "stage 2", work.Stages[2].CostUSD, work.Stages[2].Usage, work.Stages[2].Models,
		0.25, review, reviewModels)
	expectMeasured(t, "attempt", work.Attempts[0].CostUSD, work.Attempts[0].Usage, work.Attempts[0].Models,
		0.5,
		protocol.Usage{InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44},
		map[string]protocol.ModelUsage{
			"claude-opus-4": {
				InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44,
				CostUSD: 0.35,
			},
			"claude-haiku-4": {
				InputTokens: 5, CacheCreationInputTokens: 6, CacheReadInputTokens: 7, OutputTokens: 8, CostUSD: 0.15,
			},
		})
}

// TestRunAttemptKeepsCostWhenStageFails is the failure path the money still
// went out on. A stage that reports a terminal error ends the attempt from
// inside the stage loop, and the spend it already made has to survive that
// early return.
func TestRunAttemptKeepsCostWhenStageFails(t *testing.T) {
	harness := startClaudeCostHarness(t, discardLogger())
	usage := protocol.Usage{
		InputTokens: 7, CacheCreationInputTokens: 8, CacheReadInputTokens: 9, OutputTokens: 10,
	}
	models := map[string]protocol.ModelUsage{"claude-opus-4": {
		InputTokens: 7, CacheCreationInputTokens: 8, CacheReadInputTokens: 9, OutputTokens: 10, CostUSD: 0.4,
	}}
	harness.reply(t, 1, resultEvent(true, 0.4, usage, models))

	work := harness.run(t, "Failing measured stage", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
		{Name: "Review", Prompt: "Review the implementation."},
	}, "claude-cost-failure")

	if work.Attempts[0].State != "failed" {
		t.Fatalf("Attempt state = %q, want failed: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 2 || work.Stages[0].State != protocol.StageFailed {
		t.Fatalf("Pipeline stages = %#v, want a failed first stage", work.Stages)
	}
	expectMeasured(t, "stage 0", work.Stages[0].CostUSD, work.Stages[0].Usage, work.Stages[0].Models,
		0.4, usage, models)
	expectUnmeasured(t, "unreached stage 1", work.Stages[1].CostUSD, work.Stages[1].Usage, work.Stages[1].Models)
	expectMeasured(t, "attempt", work.Attempts[0].CostUSD, work.Attempts[0].Usage, work.Attempts[0].Models,
		0.4, usage, models)
}

// TestRunAttemptKeepsStageCostWhenCancelled is the other early return out of
// the stage loop. An operator cancels while a later stage is still running, so
// the attempt ends on somebody else's decision rather than its own, and the
// spend the earlier stage already made still has to reach the completion. The
// second stage holds itself open by waiting for a response that never comes.
func TestRunAttemptKeepsStageCostWhenCancelled(t *testing.T) {
	harness := startClaudeCostHarness(t, discardLogger())
	usage := protocol.Usage{
		InputTokens: 3, CacheCreationInputTokens: 5, CacheReadInputTokens: 7, OutputTokens: 9,
	}
	models := map[string]protocol.ModelUsage{"claude-opus-4": {
		InputTokens: 3, CacheCreationInputTokens: 5, CacheReadInputTokens: 7, OutputTokens: 9, CostUSD: 0.6,
	}}
	harness.reply(t, 1, resultEvent(false, 0.6, usage, models))

	run := harness.start(t, "Cancelled measured stage", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
		{Name: "Review", Prompt: "Review the implementation."},
	}, "claude-cost-cancelled")
	session := run.Sessions[0].ID
	harness.awaitStage(t, session, 0, protocol.StageSucceeded)
	harness.awaitStage(t, session, 1, protocol.StageRunning)
	if _, err := harness.store.CancelRun(context.Background(), run.Run.ID); err != nil {
		t.Fatal(err)
	}
	work := harness.await(t, session)

	if work.Attempts[0].State != "cancelled" {
		t.Fatalf("Attempt state = %q, want cancelled: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 2 {
		t.Fatalf("Pipeline stages = %#v, want two", work.Stages)
	}
	expectMeasured(t, "stage 0", work.Stages[0].CostUSD, work.Stages[0].Usage, work.Stages[0].Models,
		0.6, usage, models)
	expectUnmeasured(t, "cancelled stage 1", work.Stages[1].CostUSD, work.Stages[1].Usage, work.Stages[1].Models)
	expectMeasured(t, "attempt", work.Attempts[0].CostUSD, work.Attempts[0].Usage, work.Attempts[0].Models,
		0.6, usage, models)
}

// TestRunAttemptWarnsWhenClaudeCostMissing is the alarm. Claude's result event
// is the only source of the number, so a Claude stage that returns without one
// has to say so in the worker log; the code stage beside it has no such source
// and must stay quiet.
func TestRunAttemptWarnsWhenClaudeCostMissing(t *testing.T) {
	log := &lockedBuffer{}
	harness := startClaudeCostHarness(t, slog.New(slog.NewTextHandler(log, nil)))
	harness.reply(t, 1, `{"type":"result","result":"stage done","is_error":false}`)

	work := harness.run(t, "Unmeasured stage", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
		{Name: "Gate", Kind: protocol.StageKindCode, Command: "echo 'suite passed'"},
	}, "claude-cost-missing")

	if work.Attempts[0].State != "succeeded" {
		t.Fatalf("Attempt state = %q, want succeeded: %#v", work.Attempts[0].State, work.Attempts)
	}
	expectUnmeasured(t, "stage 0", work.Stages[0].CostUSD, work.Stages[0].Usage, work.Stages[0].Models)
	expectUnmeasured(t, "attempt", work.Attempts[0].CostUSD, work.Attempts[0].Usage, work.Attempts[0].Models)

	var warnings []string
	for _, line := range strings.Split(log.String(), "\n") {
		if strings.Contains(line, "claude_cost_missing") {
			warnings = append(warnings, line)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("claude_cost_missing warnings = %d, want 1: %v", len(warnings), warnings)
	}
	for _, expected := range []string{
		`level=WARN`, `msg=claude_cost_missing`,
		`attempt_id=` + work.Attempts[0].ID, `position=0`, `which=cost`,
	} {
		if !strings.Contains(warnings[0], expected) {
			t.Fatalf("warning %q is missing %q", warnings[0], expected)
		}
	}
}

// TestClaudeCostMissingNamesTheAbsentValue covers the cases the end to end run
// above cannot reach cheaply: which of the three is named first, the run that
// reports a spend of nothing, and every reason there is nothing to warn about.
// A code stage is missing from this table on purpose: its exit message looks
// exactly like a Claude stage that reported nothing, and what keeps it quiet
// is that the stage loop never offers it here at all.
func TestClaudeCostMissingNamesTheAbsentValue(t *testing.T) {
	full := supervisorMessage{
		Reason: "exited", CostUSD: costPointer(0.25),
		Usage:  &protocol.Usage{InputTokens: 11, OutputTokens: 44},
		Models: map[string]protocol.ModelUsage{"claude-opus-4": {CostUSD: 0.25}},
	}
	zeroed := full
	zeroed.CostUSD = costPointer(0)
	zeroed.Usage = &protocol.Usage{}
	noCost := full
	noCost.CostUSD = nil
	noUsage := full
	noUsage.Usage = nil
	noModels := full
	noModels.Models = nil
	cancelled := noCost
	cancelled.Reason = "cancelled"
	reported := noCost
	reported.Reason = "outcome_reported"

	for _, testCase := range []struct {
		name    string
		runtime string
		message supervisorMessage
		want    string
	}{
		{name: "everything reported", runtime: protocol.RuntimeClaudeCode, message: full},
		{name: "no cost", runtime: protocol.RuntimeClaudeCode, message: noCost, want: "cost"},
		{name: "no usage", runtime: protocol.RuntimeClaudeCode, message: noUsage, want: "usage"},
		{name: "no models", runtime: protocol.RuntimeClaudeCode, message: noModels, want: "models"},
		{name: "a run that spent nothing", runtime: protocol.RuntimeClaudeCode, message: zeroed, want: "zeroed"},
		{name: "a cancelled stage", runtime: protocol.RuntimeClaudeCode, message: cancelled},
		{name: "an outcome the agent reported", runtime: protocol.RuntimeClaudeCode, message: reported},
		{name: "Codex", runtime: protocol.RuntimeCodex, message: noCost},
		{name: "Pi", runtime: protocol.RuntimePi, message: noCost},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := missingClaudeCost(testCase.runtime, testCase.message); got != testCase.want {
				t.Fatalf("missingClaudeCost() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestAttemptHandleStageCostSumStaysNilUntilReported is the difference between
// "nothing was spent" and "nothing was measured": a runtime that reports no
// cost must leave the attempt's columns empty rather than claim a zero, and a
// stage that reports only part of the three contributes only that part.
func TestAttemptHandleStageCostSumStaysNilUntilReported(t *testing.T) {
	handle := &attemptHandle{}
	handle.addCost(supervisorMessage{Reason: "exited", Result: "no cost here"})
	cost, usage, models := handle.attemptCost()
	expectUnmeasured(t, "handle", cost, usage, models)

	handle.addCost(supervisorMessage{Usage: &protocol.Usage{InputTokens: 11}})
	cost, usage, models = handle.attemptCost()
	if cost != nil || models != nil {
		t.Fatalf("handle invented a cost of %s and models %+v", formatCost(cost), models)
	}
	if usage == nil || *usage != (protocol.Usage{InputTokens: 11}) {
		t.Fatalf("handle usage = %+v", usage)
	}
}
