package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/protocol"
)

func TestCodeStageSupervisorHelper(t *testing.T) {
	if os.Getenv("FACTORY_TEST_SUPERVISOR_HELPER") != "1" {
		return
	}
	control := os.NewFile(3, "factory-worker-control")
	if err := RunSupervisor(control, os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// writeCountingFakeCodex records every real agent invocation, and only a real
// one. Capability probing calls --version constantly, so those are excluded;
// what lands in the log is exactly "a model runtime was spawned to do work".
// This file is what makes INV-7 a measurement rather than an assertion.
func writeCountingFakeCodex(t *testing.T, path, logPath string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo codex-test
  exit 0
fi
if [ "${1:-}" = "login" ]; then
  echo 'Logged in'
  exit 0
fi
result_path=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then result_path="$argument"; fi
  previous="$argument"
done
cat > /dev/null
printf 'runtime invoked\n' >> "` + logPath + `"
if [ -n "$result_path" ]; then printf 'fake agent completed\n' > "$result_path"; fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runtimeInvocations(t *testing.T, logPath string) int {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(strings.TrimSpace(string(contents)))) / 2
}

type codeStageHarness struct {
	store        *controlplane.Store
	repositoryID string
	runtimeLog   string
}

func startCodeStageHarness(t *testing.T) *codeStageHarness {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "factory-code-stage-")
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

	serverRoot, err := os.MkdirTemp("/tmp", "factory-code-stage-server-")
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

	runtimeLog := filepath.Join(root, "runtime-invocations.log")
	fakeCodex := filepath.Join(root, "codex")
	writeCountingFakeCodex(t, fakeCodex, runtimeLog)
	t.Setenv("FACTORY_TEST_SUPERVISOR_HELPER", "1")
	options := testOptions(fakeCodex)
	options.HTTPClient = server.Client()
	options.SupervisorCommand = []string{os.Args[0], "-test.run=TestCodeStageSupervisorHelper"}
	options.PollInterval = 10 * time.Millisecond
	options.RegistrationInterval = 20 * time.Millisecond
	manager, err := New(Config{
		Server: server.URL, Name: "code-stage", Runtime: protocol.RuntimeCodex,
		MaxConcurrent: 1, DataDirectory: filepath.Join(root, "worker"),
		Repositories: map[string]RepositoryConfig{"factory": {Path: repositoryPath, BaseBranch: "main"}},
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	return &codeStageHarness{store: store, repositoryID: registered.Repositories[0].ID, runtimeLog: runtimeLog}
}

func (harness *codeStageHarness) run(
	t *testing.T,
	name string,
	stages []protocol.PipelineStage,
	requestKey string,
) protocol.Work {
	t.Helper()
	pipeline, err := harness.store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: name, Stages: stages,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := harness.store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: name, Prompt: "Run the pipeline.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{harness.repositoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := harness.store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	var work protocol.Work
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		work, err = harness.store.Work(context.Background(), run.Sessions[0].ID)
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

// TestFailingCodeStageFailsTheRunWithNoRuntimeInvoked is AC-6 and INV-7. A
// pipeline whose only stage is a code stage must fail the run on a non zero
// exit, must carry the command output, and must never spawn a model runtime.
func TestFailingCodeStageFailsTheRunWithNoRuntimeInvoked(t *testing.T) {
	harness := startCodeStageHarness(t)
	work := harness.run(t, "Failing gate", []protocol.PipelineStage{
		{Name: "Type check", Kind: protocol.StageKindCode, Command: "echo 'type error in greet.js'; exit 3"},
	}, "code-stage-failing")

	if work.Attempts[0].State != "failed" {
		t.Fatalf("Attempt state = %q, want failed: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 1 || work.Stages[0].State != protocol.StageFailed {
		t.Fatalf("Pipeline stages = %#v, want one failed stage", work.Stages)
	}
	if !strings.Contains(work.Stages[0].Error, "type error in greet.js") {
		t.Fatalf("stage error = %q, want the command output", work.Stages[0].Error)
	}
	if invocations := runtimeInvocations(t, harness.runtimeLog); invocations != 0 {
		t.Fatalf("runtime invocations = %d, want 0: INV-7 requires a code stage to invoke no model", invocations)
	}
}

// TestSucceedingCodeStageRecordsOutputWithNoRuntimeInvoked is the other half of
// INV-7: the zero token path has to work, not merely fail cheaply.
func TestSucceedingCodeStageRecordsOutputWithNoRuntimeInvoked(t *testing.T) {
	harness := startCodeStageHarness(t)
	work := harness.run(t, "Passing gate", []protocol.PipelineStage{
		{Name: "Tests", Kind: protocol.StageKindCode, Command: "echo 'pass 2 fail 0'"},
	}, "code-stage-passing")

	if work.Attempts[0].State != "succeeded" {
		t.Fatalf("Attempt state = %q, want succeeded: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 1 || work.Stages[0].State != protocol.StageSucceeded {
		t.Fatalf("Pipeline stages = %#v, want one succeeded stage", work.Stages)
	}
	if !strings.Contains(work.Stages[0].Result, "pass 2 fail 0") {
		t.Fatalf("stage result = %q, want the command output", work.Stages[0].Result)
	}
	if invocations := runtimeInvocations(t, harness.runtimeLog); invocations != 0 {
		t.Fatalf("runtime invocations = %d, want 0", invocations)
	}
}

// TestCodeStageAfterAgentStageInvokesTheRuntimeExactlyOnce is the shape the
// design actually describes: an agent stage writes, a code stage gates. Exactly
// one runtime invocation for two stages is the per stage form of INV-7.
func TestCodeStageAfterAgentStageInvokesTheRuntimeExactlyOnce(t *testing.T) {
	harness := startCodeStageHarness(t)
	work := harness.run(t, "Build then gate", []protocol.PipelineStage{
		{Name: "Build", Prompt: "Make the requested change."},
		{Name: "Tests", Kind: protocol.StageKindCode, Command: "echo 'suite failed'; exit 1"},
	}, "code-stage-after-agent")

	if work.Attempts[0].State != "failed" {
		t.Fatalf("Attempt state = %q, want failed: %#v", work.Attempts[0].State, work.Attempts)
	}
	if len(work.Stages) != 2 || work.Stages[0].State != protocol.StageSucceeded ||
		work.Stages[1].State != protocol.StageFailed {
		t.Fatalf("Pipeline stages = %#v, want agent succeeded then code failed", work.Stages)
	}
	if !strings.Contains(work.Stages[1].Error, "suite failed") {
		t.Fatalf("code stage error = %q, want the command output", work.Stages[1].Error)
	}
	if invocations := runtimeInvocations(t, harness.runtimeLog); invocations != 1 {
		t.Fatalf("runtime invocations = %d, want exactly 1: the code stage must not spawn one", invocations)
	}
}
