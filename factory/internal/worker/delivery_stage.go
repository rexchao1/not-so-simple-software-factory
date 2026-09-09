package worker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

var admissionTitleSuffix = regexp.MustCompile(` \([0-9a-f]{8}\)$`)

// runDeliveryStageInAttempt performs the fixed delivery operations that used
// to require a third model context. It accepts no prompt or command from the
// Pipeline: the Worker owns every argument.
func (manager *Manager) runDeliveryStageInAttempt(input codeStageContext, priorSummary string) (supervisorMessage, bool) {
	claim, token, handle := input.claim, input.token, input.handle
	if input.firstExecutedStage {
		started, err := manager.client.start(handle.context, claim.Attempt.ID, protocol.StartAttemptRequest{
			LeaseToken: token, StartedFromSHA: input.worktree.BaseCommit,
		})
		if err != nil || started.State != "running" {
			handle.stop("failed")
			input.sender.closeAndWait(5 * time.Second)
			message := "control plane did not accept the running transition"
			if err != nil {
				message = err.Error()
			}
			manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree, "failed", "", message)
			return supervisorMessage{}, false
		}
	}
	if _, err := manager.client.startStage(handle.context, claim.Attempt.ID, input.stage.Position,
		protocol.StartStageRequest{LeaseToken: token}); err != nil {
		handle.stop(stageStartFailureReason(handle.stopReason(), err))
		input.sender.closeAndWait(5 * time.Second)
		manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree,
			terminalForStop(handle), "", err.Error())
		return supervisorMessage{}, false
	}
	if err := manager.persistLifecycle(claim.Attempt.ID, manifestRunning, nil); err != nil {
		handle.stop("failed")
		manager.markUnhealthy("manifest_write", err)
		input.sender.closeAndWait(5 * time.Second)
		manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree, "failed", "", err.Error())
		return supervisorMessage{}, false
	}

	result, err := manager.runDeliveryStage(handle.context, claim, token, handle,
		input.repository, input.worktree, priorSummary)
	state := protocol.StageSucceeded
	errorText := ""
	if err != nil {
		state, errorText = protocol.StageFailed, err.Error()
	}
	_, completeErr := manager.client.completeStage(handle.context, claim.Attempt.ID, input.stage.Position,
		protocol.CompleteStageRequest{LeaseToken: token, State: state, Result: result, Error: errorText})
	if completeErr != nil || err != nil {
		input.sender.closeAndWait(5 * time.Second)
		manager.finishWithWorktree(claim, token, handle, input.repository, input.worktree,
			"failed", result, firstNonEmpty(errorString(completeErr), errorText))
		return supervisorMessage{}, false
	}
	return supervisorMessage{Type: "exit", Reason: "exited", ExitCode: 0, Result: result}, true
}

func (manager *Manager) runDeliveryStage(
	ctx context.Context,
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	repository Repository,
	value worktree,
	priorSummary string,
) (string, error) {
	stdout, stderr, err := runGitCommand(ctx, manager.options.GitExecutable, value.Path, 64<<10,
		"rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", commandFailure("read delivery HEAD", stdout, stderr, err)
	}
	if strings.TrimSpace(string(stdout)) == value.BaseCommit {
		return manager.recordFactoryOutcome(ctx, claim, token, handle, repository, value,
			protocol.WorkUpdateNoChange, "No changes to deliver.", "")
	}

	stdout, stderr, err = runGitCommand(ctx, manager.options.GitExecutable, value.Path, 64<<10,
		"push", "origin", "HEAD:refs/heads/"+claim.Session.Target.PublishBranch)
	if err != nil {
		return "", commandFailure("push Factory publish branch", stdout, stderr, err)
	}

	repositorySlug := strings.TrimPrefix(claim.Repository.RemoteIdentity, "github.com/")
	if repositorySlug == claim.Repository.RemoteIdentity || strings.Count(repositorySlug, "/") != 1 {
		return "", errors.New("delivery requires a github.com/owner/repository identity")
	}
	stdout, _, _ = runCommand(ctx, manager.options.GitHubExecutable, value.Path, 64<<10,
		"pr", "list", "--repo", repositorySlug, "--head", claim.Session.Target.PublishBranch,
		"--state", "open", "--json", "url", "--jq", ".[0].url")
	pullRequestURL := strings.TrimSpace(string(stdout))
	if pullRequestURL == "" {
		title := admissionTitleSuffix.ReplaceAllString(claim.Session.TaskName, "")
		stdout, stderr, err = runCommand(ctx, manager.options.GitHubExecutable, value.Path, 64<<10,
			"pr", "create", "--repo", repositorySlug, "--head", claim.Session.Target.PublishBranch,
			"--base", value.BaseBranch, "--title", title, "--body", deliveryBody(title, priorSummary))
		if err != nil {
			return "", commandFailure("create pull request", stdout, stderr, err)
		}
		pullRequestURL = strings.TrimSpace(string(stdout))
	}
	if pullRequestURL == "" {
		return "", errors.New("GitHub did not return a pull request URL")
	}
	_, err = manager.recordFactoryOutcome(ctx, claim, token, handle, repository, value,
		protocol.WorkUpdateReady, "Factory delivered the verified work.", pullRequestURL)
	if err != nil {
		return "", err
	}
	return pullRequestURL, nil
}

// maxDeliveryEvidenceBytes keeps the pull-request body a summary. Raw stage
// output stays in Factory evidence, which is where an operator who wants the
// transcript should be reading it.
const maxDeliveryEvidenceBytes = 2 << 10

func deliveryBody(title, summary string) string {
	// The preceding stage's own result text, not the JSON handoff envelope:
	// this body is read by a human in a pull request, so a truncated object
	// literal would be worse than no summary at all.
	summary = strings.TrimSpace(boundedText(summary, maxDeliveryEvidenceBytes))
	if summary == "" {
		summary = "No concise verification summary was recorded."
	}
	return fmt.Sprintf("## Summary\n\n%s\n\n## Verification and review\n\n%s", title, summary)
}

func (manager *Manager) recordFactoryOutcome(
	ctx context.Context,
	claim protocol.Claim,
	token string,
	handle *attemptHandle,
	repository Repository,
	value worktree,
	status protocol.WorkUpdateStatus,
	message string,
	pullRequestURL string,
) (string, error) {
	requestID, err := manager.randomUUID()
	if err != nil {
		return "", err
	}
	forward := protocol.AttemptUpdateRequest{
		LeaseToken: token, RequestID: requestID, Status: status,
		Message: message, PullRequestURL: pullRequestURL,
	}
	if status == protocol.WorkUpdateReady {
		evidence, validationErr := manager.validateReadyDelivery(ctx, claim, repository, value, pullRequestURL)
		if validationErr != nil {
			return "", errors.New(validationErr.message)
		}
		forward.PullRequestHeadBranch = evidence.HeadBranch
		forward.PullRequestHeadSHA = evidence.HeadSHA
	}
	update, err := manager.client.update(ctx, claim.Attempt.ID, forward)
	if err != nil {
		return "", err
	}
	handle.recordOutcome(update)
	return update.Message, nil
}
