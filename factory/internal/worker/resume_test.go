package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestAgentNeedsInputForwardsWorkerVerifiedCheckpoint(t *testing.T) {
	_, checkout, repository, base := resumeTestRepository(t)
	var accepted protocol.AttemptUpdateRequest
	controlPlane := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input protocol.AttemptUpdateRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode update: %v", err)
		}
		if input.ReplayOnly {
			writer.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(writer).Encode(protocol.ErrorBody{Error: protocol.APIError{
				Code: "update_request_not_found", Message: "not stored",
			}})
			return
		}
		accepted = input
		_ = json.NewEncoder(writer).Encode(protocol.WorkUpdate{
			ID: "update-needs-input", WorkID: testWorkID, AttemptID: testAttemptID,
			RequestID: input.RequestID, Status: input.Status, Message: input.Message,
			CheckpointSHA: input.CheckpointSHA, CheckpointPublished: input.CheckpointPublished,
		})
	}))
	t.Cleanup(controlPlane.Close)
	manager := &Manager{
		options: Options{GitExecutable: "git"},
		client:  newClient(controlPlane.URL, controlPlane.Client()),
	}
	claim := protocol.Claim{
		Attempt: protocol.Attempt{ID: testAttemptID},
		Session: protocol.ClaimedSession{
			ID: testWorkID, Target: protocol.WorkTarget{PublishBranch: "factory/work-1111111111111111"},
		},
	}
	input := protocol.AgentUpdateRequest{
		WorkID: testWorkID, AttemptID: testAttemptID, UpdateToken: "token",
		RequestID: "65000000-0000-4000-8000-000000000001",
		Status:    protocol.WorkUpdateNeedsInput, Message: "Which behavior?",
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://factory.local/update", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &attemptHandle{context: ctx, cancel: cancel}
	manager.handleAgentUpdate(
		recorder, request, claim, "lease", sha256.Sum256([]byte("token")), handle,
		repository, worktree{Path: checkout, BaseCommit: base},
	)
	if recorder.Code != http.StatusOK || accepted.CheckpointSHA != base || accepted.CheckpointPublished ||
		accepted.Status != protocol.WorkUpdateNeedsInput {
		t.Fatalf("needs-input response = %d, forwarded %#v, body %s", recorder.Code, accepted, recorder.Body.String())
	}
}

func TestNeedsInputCheckpointRequiresCleanDurableExactHead(t *testing.T) {
	root, checkout, repository, base := resumeTestRepository(t)
	manager := &Manager{options: Options{GitExecutable: "git"}}
	const publishBranch = "factory/work-1111111111111111"
	claim := protocol.Claim{
		Session: protocol.ClaimedSession{
			ID: testWorkID, Target: protocol.WorkTarget{PublishBranch: publishBranch},
		},
	}
	value := worktree{Path: checkout, BaseCommit: base}
	evidence, validationErr := manager.validateNeedsInputCheckpoint(
		context.Background(), claim, repository, value,
	)
	if validationErr != nil || evidence.SHA != base || evidence.Published {
		t.Fatalf("unchanged checkpoint = %#v, error %#v", evidence, validationErr)
	}
	if err := os.WriteFile(filepath.Join(checkout, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, validationErr := manager.validateNeedsInputCheckpoint(
		context.Background(), claim, repository, value,
	); validationErr == nil || validationErr.code != "checkpoint_worktree_dirty" {
		t.Fatalf("dirty checkpoint error = %#v", validationErr)
	}
	runTestCommand(t, checkout, "git", "add", "dirty.txt")
	runTestCommand(t, checkout, "git", "commit", "-m", "test: checkpoint")
	checkpoint := strings.TrimSpace(runTestCommand(t, checkout, "git", "rev-parse", "HEAD"))
	if _, validationErr := manager.validateNeedsInputCheckpoint(
		context.Background(), claim, repository, value,
	); validationErr == nil || validationErr.code != "checkpoint_publish_required" {
		t.Fatalf("unpushed checkpoint error = %#v", validationErr)
	}
	runTestCommand(t, checkout, "git", "push", "origin", "HEAD:refs/heads/"+publishBranch)
	evidence, validationErr = manager.validateNeedsInputCheckpoint(
		context.Background(), claim, repository, value,
	)
	if validationErr != nil || evidence.SHA != checkpoint || !evidence.Published {
		t.Fatalf("published checkpoint = %#v, error %#v", evidence, validationErr)
	}

	other := filepath.Join(root, "other")
	runTestCommand(t, root, "git", "clone", filepath.Join(root, "remote.git"), other)
	runTestCommand(t, other, "git", "config", "user.email", "factory@example.com")
	runTestCommand(t, other, "git", "config", "user.name", "Factory Test")
	runTestCommand(t, other, "git", "checkout", "-b", "move", checkpoint)
	if err := os.WriteFile(filepath.Join(other, "moved.txt"), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, other, "git", "add", "moved.txt")
	runTestCommand(t, other, "git", "commit", "-m", "test: move checkpoint ref")
	runTestCommand(t, other, "git", "push", "origin", "HEAD:refs/heads/"+publishBranch)
	if _, validationErr := manager.validateNeedsInputCheckpoint(
		context.Background(), claim, repository, value,
	); validationErr == nil || validationErr.code != "checkpoint_head_mismatch" {
		t.Fatalf("moved checkpoint error = %#v", validationErr)
	}
}

func TestRecoveryPreparationPrefersPendingSHAAndRequiresExactRestoredRef(t *testing.T) {
	_, checkout, repository, base := resumeTestRepository(t)
	const publishBranch = "factory/work-1111111111111111"
	if err := os.WriteFile(filepath.Join(checkout, "checkpoint.txt"), []byte("checkpoint\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, checkout, "git", "add", "checkpoint.txt")
	runTestCommand(t, checkout, "git", "commit", "-m", "test: checkpoint")
	checkpoint := strings.TrimSpace(runTestCommand(t, checkout, "git", "rev-parse", "HEAD"))
	runTestCommand(t, checkout, "git", "push", "origin", "HEAD:refs/heads/"+publishBranch)

	branch, commit, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		PendingResumeSHA: checkpoint, CheckpointPublished: true,
	})
	if err != nil || branch != "main" || commit != checkpoint {
		t.Fatalf("pending resume = %q %q, error %v", branch, commit, err)
	}
	value, err := prepareWorktree(
		context.Background(), "git", t.TempDir(), repository,
		testWorkID, testAttemptID, worktreeRecovery{
			WorkID: testWorkID, PublishBranch: publishBranch,
			PendingResumeSHA: checkpoint, CheckpointPublished: true,
		},
	)
	if err != nil || value.BaseBranch != "main" || value.BaseCommit != checkpoint {
		t.Fatalf("pending-resume worktree = %#v, error %v", value, err)
	}
	prompt := buildPrompt(protocol.Claim{
		Session: protocol.ClaimedSession{
			TaskName: "Resume work", Prompt: "Continue the implementation.",
			OutcomeContract: protocol.OutcomeAgentUpdate,
			Target:          protocol.WorkTarget{PublishBranch: publishBranch},
		},
		Repository: protocol.Repository{RemoteIdentity: repository.RemoteIdentity},
	}, value)
	if !strings.Contains(prompt, "Target base branch: main") ||
		!strings.Contains(prompt, "Factory publish branch: "+publishBranch) {
		t.Fatalf("pending-resume prompt lost distinct base and publish branches: %q", prompt)
	}
	missingBaseRepository := repository
	missingBaseRepository.BaseBranch = "deleted-base"
	if _, _, err := resolveRecoveryCommit(
		context.Background(), "git", missingBaseRepository, worktreeRecovery{
			WorkID: testWorkID, PublishBranch: publishBranch,
			PendingResumeSHA: checkpoint, CheckpointPublished: true,
		},
	); err == nil || !strings.Contains(err.Error(), "deleted-base") {
		t.Fatalf("missing configured recovery base error = %v", err)
	}
	runTestCommand(t, checkout, "git", "push", "origin", ":refs/heads/"+publishBranch)
	if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		PendingResumeSHA: checkpoint, CheckpointPublished: true,
	}); err == nil || !strings.Contains(err.Error(), publishBranch) || !strings.Contains(err.Error(), checkpoint) {
		t.Fatalf("missing pending ref error = %v", err)
	}
	if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		CheckpointSHA: checkpoint, CheckpointPublished: true,
	}); err == nil || !strings.Contains(err.Error(), "previously published checkpoint") ||
		!strings.Contains(err.Error(), publishBranch) || !strings.Contains(err.Error(), checkpoint) {
		t.Fatalf("missing historical checkpoint ref error = %v", err)
	}

	knownPR := "https://github.com/owainlewis/factory/pull/343"
	if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		PullRequestURL: knownPR, PullRequestHeadSHA: checkpoint,
	}); err == nil || !strings.Contains(err.Error(), knownPR) || !strings.Contains(err.Error(), checkpoint) {
		t.Fatalf("missing known-PR ref error = %v", err)
	}
	runTestCommand(t, checkout, "git", "push", "origin", base+":refs/heads/"+publishBranch)
	if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		PullRequestURL: knownPR, PullRequestHeadSHA: checkpoint,
	}); err == nil || !strings.Contains(err.Error(), "not trusted recovery SHA") {
		t.Fatalf("wrong restored ref error = %v", err)
	}
	runTestCommand(t, checkout, "git", "push", "origin", checkpoint+":refs/heads/"+publishBranch)
	branch, commit, err = resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch,
		PullRequestURL: knownPR, PullRequestHeadSHA: checkpoint,
	})
	if err != nil || branch != "main" || commit != checkpoint {
		t.Fatalf("restored known-PR ref = %q %q, error %v", branch, commit, err)
	}

	missing := strings.Repeat("f", 40)
	if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, worktreeRecovery{
		WorkID: testWorkID, PublishBranch: publishBranch, PendingResumeSHA: missing,
	}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing unpublished checkpoint error = %v", err)
	}
	if _, err := prepareWorktree(
		context.Background(), "git", t.TempDir(), repository,
		testWorkID, testAttemptID, worktreeRecovery{
			WorkID: testWorkID, PublishBranch: publishBranch, PendingResumeSHA: missing,
		},
	); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("worktree preparation did not preserve the pending-commit error: %v", err)
	}
}

func TestRecoveryCommitRejectsChangedRegisteredOrigin(t *testing.T) {
	root, checkout, repository, base := resumeTestRepository(t)
	otherRemote := filepath.Join(root, "other.git")
	runTestCommand(t, root, "git", "init", "--bare", otherRemote)
	runTestCommand(t, checkout, "git", "remote", "set-url", "origin", otherRemote)
	for name, recovery := range map[string]worktreeRecovery{
		"pending checkpoint": {
			WorkID: testWorkID, PublishBranch: "factory/work-1111111111111111", PendingResumeSHA: base,
		},
		"known pull request": {
			WorkID: testWorkID, PublishBranch: "factory/work-1111111111111111",
			PullRequestURL: "https://github.com/owainlewis/factory/pull/343", PullRequestHeadSHA: base,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolveRecoveryCommit(context.Background(), "git", repository, recovery); err == nil ||
				!strings.Contains(err.Error(), "repository origin changed") {
				t.Fatalf("changed origin recovery error = %v", err)
			}
		})
	}
}

func TestPublishCommitRejectsRefMovementDuringFetch(t *testing.T) {
	root, checkout, repository, base := resumeTestRepository(t)
	const publishBranch = "factory/work-1111111111111111"
	runTestCommand(t, checkout, "git", "push", "origin", base+":refs/heads/"+publishBranch)
	if err := os.WriteFile(filepath.Join(checkout, "moved.txt"), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, checkout, "git", "add", "moved.txt")
	runTestCommand(t, checkout, "git", "commit", "-m", "test: move publish ref")
	moved := strings.TrimSpace(runTestCommand(t, checkout, "git", "rev-parse", "HEAD"))
	runTestCommand(t, checkout, "git", "push", "origin", moved+":refs/heads/moved-source")
	remote := filepath.Join(root, "remote.git")
	gitWrapper := filepath.Join(root, "git-wrapper")
	script := "#!/bin/sh\n" +
		"git \"$@\"\n" +
		"status=$?\n" +
		"if [ \"$status\" -eq 0 ] && [ \"$1\" = fetch ]; then\n" +
		"  git --git-dir=\"" + remote + "\" update-ref refs/heads/" + publishBranch + " " + moved + "\n" +
		"fi\n" +
		"exit \"$status\"\n"
	if err := os.WriteFile(gitWrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remotePublishCommitOptional(
		context.Background(), gitWrapper, repository, publishBranch,
	); err == nil || !strings.Contains(err.Error(), "publish branch moved during validation") {
		t.Fatalf("moving publish ref error = %v", err)
	}
}

func resumeTestRepository(t *testing.T) (string, string, Repository, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	checkout := filepath.Join(root, "checkout")
	runTestCommand(t, root, "git", "init", "--bare", remote)
	runTestCommand(t, root, "git", "clone", remote, checkout)
	runTestCommand(t, checkout, "git", "config", "user.email", "factory@example.com")
	runTestCommand(t, checkout, "git", "config", "user.name", "Factory Test")
	if err := os.WriteFile(filepath.Join(checkout, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, checkout, "git", "add", "base.txt")
	runTestCommand(t, checkout, "git", "commit", "-m", "test: base")
	runTestCommand(t, checkout, "git", "branch", "-M", "main")
	runTestCommand(t, checkout, "git", "push", "origin", "main")
	runTestCommand(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	base := strings.TrimSpace(runTestCommand(t, checkout, "git", "rev-parse", "HEAD"))
	repository, err := resolveRepository("factory", checkout, "git")
	if err != nil {
		t.Fatal(err)
	}
	return root, checkout, repository, base
}
