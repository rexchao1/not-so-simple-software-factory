package worker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

// codeStageResult is the whole outcome of a stage that ran a command instead of
// a model. It deliberately mirrors the fields the supervisor path reports, so
// the stage bookkeeping either path performs is identical.
type codeStageResult struct {
	state  protocol.StageRunState
	result string
	failed string
}

// runCodeStage executes one kind: code stage. It never starts a supervisor and
// never touches a runtime, which is what INV-7 asserts: a code stage invokes no
// model and consumes no tokens. The command runs through sh -c so a stage can
// declare "npm test" or "go vet ./..." the way a person would write it.
//
// Output is streamed to the attempt event log line by line, exactly as agent
// output is, and the tail is kept for the stage record so a failure carries the
// reason with it.
func runCodeStage(
	ctx context.Context,
	worktree string,
	stage protocol.StageRun,
	deadline time.Time,
	sender *eventSender,
) codeStageResult {
	command := strings.TrimSpace(stage.Command)
	if command == "" {
		return codeStageResult{state: protocol.StageFailed, failed: "the code stage declares no command"}
	}
	stageContext := ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		stageContext, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	process := exec.CommandContext(stageContext, "sh", "-c", command)
	process.Dir = worktree
	// A code stage is a mechanical gate, not an agent. It gets the worker's
	// environment as the supervisor path does today, with the agent update
	// credentials withheld: there is no agent to report an outcome, and a
	// declared command has no business holding the report token.
	process.Env = codeStageEnvironment()

	tail := &tailBuffer{limit: protocol.MaxResultBytes}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return codeStageResult{state: protocol.StageFailed, failed: err.Error()}
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return codeStageResult{state: protocol.StageFailed, failed: err.Error()}
	}
	if err := process.Start(); err != nil {
		return codeStageResult{state: protocol.StageFailed, failed: err.Error()}
	}

	readers := sync.WaitGroup{}
	readers.Add(2)
	go func() {
		defer readers.Done()
		streamCodeStageOutput(stdout, "stdout", tail, sender)
	}()
	go func() {
		defer readers.Done()
		streamCodeStageOutput(stderr, "stderr", tail, sender)
	}()
	readers.Wait()
	waitErr := process.Wait()

	output := strings.TrimRight(tail.String(), "\n")
	if waitErr == nil {
		return codeStageResult{state: protocol.StageSucceeded, result: output}
	}
	if errors.Is(stageContext.Err(), context.DeadlineExceeded) {
		return codeStageResult{
			state:  protocol.StageFailed,
			result: output,
			failed: boundedText(firstNonEmpty(output+"\n", "")+"code stage timed out: "+command, protocol.MaxErrorBytes),
		}
	}
	if errors.Is(stageContext.Err(), context.Canceled) {
		return codeStageResult{state: protocol.StageCancelled, result: output, failed: "code stage cancelled: " + command}
	}
	var exitErr *exec.ExitError
	reason := waitErr.Error()
	if errors.As(waitErr, &exitErr) {
		reason = "code stage command failed: " + command
	}
	failure := reason
	if output != "" {
		failure = output + "\n" + reason
	}
	return codeStageResult{
		state:  protocol.StageFailed,
		result: output,
		failed: boundedText(failure, protocol.MaxErrorBytes),
	}
}

func streamCodeStageOutput(reader io.Reader, stream string, tail *tailBuffer, sender *eventSender) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxResultBytes)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = tail.Write([]byte(line + "\n"))
		if sender != nil {
			sender.enqueue(stream, line, false)
		}
	}
	if err := scanner.Err(); err != nil && sender != nil {
		sender.enqueue(stream, "output truncated: "+err.Error(), true)
	}
}

// codeStageContext carries the attempt scoped values a code stage needs to
// report itself through the same control plane calls an agent stage uses.
type codeStageContext struct {
	claim              protocol.Claim
	token              string
	handle             *attemptHandle
	repository         Repository
	worktree           worktree
	sender             *eventSender
	stage              protocol.StageRun
	deadline           time.Time
	firstExecutedStage bool
}

// runCodeStageInAttempt drives one code stage through start, execution, and
// completion. It returns handled=false when it has already finished the attempt
// itself, in which case the caller must stop driving the pipeline.
//
// The attempt transition it performs on the first executed stage deliberately
// carries no supervisor PID and no process group, because there is no
// supervised process. It also never sends RuntimeStarted: no runtime started.
func (manager *Manager) runCodeStageInAttempt(input codeStageContext) (supervisorMessage, bool) {
	claim, token, handle := input.claim, input.token, input.handle
	if input.firstExecutedStage {
		started, err := manager.client.start(handle.context, claim.Attempt.ID, protocol.StartAttemptRequest{
			LeaseToken: token, StartedFromSHA: input.worktree.BaseCommit,
		})
		if err != nil {
			reason := handle.stopReason()
			var apiError *APIError
			if errors.As(err, &apiError) && apiError.Code == "lease_not_owner" {
				reason = "lease_lost"
			}
			if reason == "" {
				reason = "failed"
			}
			handle.stop(reason)
			input.sender.closeAndWait(5 * time.Second)
			errorText := err.Error()
			if reason == "timeout" {
				errorText = "Session timeout reached"
			}
			manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree,
				terminalForStop(handle), "", errorText)
			return supervisorMessage{}, false
		}
		if started.State != "running" {
			handle.stop("lease_lost")
			input.sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree, "failed", "",
				"control plane did not accept the running transition")
			return supervisorMessage{}, false
		}
		if err := manager.persistLifecycle(claim.Attempt.ID, manifestRunning, nil); err != nil {
			handle.stop("failed")
			manager.markUnhealthy("manifest_write", err)
			input.sender.closeAndWait(5 * time.Second)
			manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree, "failed", "", err.Error())
			return supervisorMessage{}, false
		}
		manager.logger.Info("attempt_started", "attempt_id", claim.Attempt.ID, "repository", input.repository.Key,
			"process", "code stage")
	}
	if _, err := manager.client.startStage(handle.context, claim.Attempt.ID, input.stage.Position,
		protocol.StartStageRequest{LeaseToken: token}); err != nil {
		reason := stageStartFailureReason(handle.stopReason(), err)
		handle.stop(reason)
		input.sender.closeAndWait(5 * time.Second)
		manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree,
			terminalForStop(handle), "", err.Error())
		return supervisorMessage{}, false
	}
	manager.logger.Info("code_stage_started", "attempt_id", claim.Attempt.ID,
		"stage", input.stage.Name, "position", input.stage.Position, "command", input.stage.Command)

	outcome := runCodeStage(handle.context, input.worktree.Path, input.stage, input.deadline, input.sender)

	_, completeErr := manager.client.completeStage(handle.context, claim.Attempt.ID, input.stage.Position,
		protocol.CompleteStageRequest{
			LeaseToken: token, State: outcome.state, Result: outcome.result, Error: outcome.failed,
		})
	if completeErr != nil || outcome.state != protocol.StageSucceeded {
		var apiError *APIError
		if errors.As(completeErr, &apiError) && apiError.Code == "lease_not_owner" {
			handle.stop("lease_lost")
		}
		input.sender.closeAndWait(5 * time.Second)
		attemptState := "failed"
		if outcome.state == protocol.StageCancelled {
			attemptState = "cancelled"
		}
		manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree,
			attemptState, outcome.result, firstNonEmpty(errorString(completeErr), outcome.failed))
		return supervisorMessage{}, false
	}
	manager.logger.Info("code_stage_completed", "attempt_id", claim.Attempt.ID,
		"stage", input.stage.Name, "position", input.stage.Position)
	return supervisorMessage{Type: "exit", Reason: "exited", ExitCode: 0, Result: outcome.result}, true
}

// codeStageEnvironment withholds the agent update channel from a command. Only
// an agent reports an outcome, so FACTORY_UPDATE_SOCKET and
// FACTORY_UPDATE_TOKEN never need to be readable by a declared command.
func codeStageEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "FACTORY_UPDATE_SOCKET" || key == "FACTORY_UPDATE_TOKEN" {
			continue
		}
		environment = append(environment, value)
	}
	return environment
}
