package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

// verifyReadyDelivery discharges the four clauses of INV-3 that the server can
// observe. It cannot discharge the fifth.
//
// INV-3 requires the server to verify repository, publish branch, local HEAD,
// remote ref, and pull request head. Local HEAD is git rev-parse inside the
// attempt's worktree, a directory that exists only on the worker's filesystem
// and is disposed when the attempt ends. The fork supports off-host workers, so
// the server may never have had access to it at all, and it is recorded as
// unsatisfied, not quietly inherited from the worker.
//
// Every failure names the clause that failed, because "verification failed" on
// its own tells an operator nothing about which fact disagreed with which.
func verifyReadyDelivery(
	ctx context.Context,
	client githubClient,
	repositoryIdentity string,
	publishBranch string,
	update protocol.AttemptUpdateRequest,
) error {
	if client == nil {
		// Refusing here is the whole point. A server that cannot check the
		// delivery must not record one as verified, so an unconfigured server
		// fails closed and says why.
		return fmt.Errorf("the server has no GitHub credential, so a ready delivery cannot be verified: github is not configured")
	}
	if publishBranch == "" {
		return fmt.Errorf("the Work has no publish branch, so its publish branch cannot be verified")
	}

	owner, repository, number, err := parsePullRequestURL(update.PullRequestURL)
	if err != nil {
		return fmt.Errorf("the reported pull request URL %q is not a GitHub pull request URL", update.PullRequestURL)
	}

	pullRequest, err := client.PullRequest(ctx, owner, repository, number)
	if err != nil {
		return fmt.Errorf("read pull request %s/%s#%d: %w", owner, repository, number, err)
	}

	// Clause 1, repository. The managed identity carries a github.com/ prefix
	// and GitHub reports owner/name, so the prefix is stripped before the
	// comparison. Both sides are compared case insensitively, matching the
	// lower(remote_identity) lookups the repository store already uses.
	wantRepository := strings.TrimPrefix(repositoryIdentity, "github.com/")
	if !strings.EqualFold(pullRequest.HeadRepo, wantRepository) {
		return fmt.Errorf(
			"repository mismatch: the pull request head is on %q, the Work belongs to %q",
			pullRequest.HeadRepo, wantRepository)
	}

	// Clause 2, publish branch.
	if pullRequest.HeadRef != publishBranch {
		return fmt.Errorf(
			"publish branch mismatch: the pull request head branch is %q, the Work publishes to %q",
			pullRequest.HeadRef, publishBranch)
	}

	// Clause 3, pull request head. Checked before the remote ref so that a
	// wrong reported SHA is named as such rather than surfacing as a ref
	// disagreement, which points the operator at the wrong fact.
	if !strings.EqualFold(pullRequest.HeadSHA, update.PullRequestHeadSHA) {
		return fmt.Errorf(
			"pull request head mismatch: GitHub reports %q, the agent reported %q",
			pullRequest.HeadSHA, update.PullRequestHeadSHA)
	}

	// Clause 4, remote ref. The publish branch on the remote must point at the
	// same commit. This is the clause that catches a pull request opened from a
	// branch that has since moved.
	remoteSHA, err := client.BranchRef(ctx, owner, repository, publishBranch)
	if err != nil {
		return fmt.Errorf("read remote ref heads/%s: %w", publishBranch, err)
	}
	if !strings.EqualFold(remoteSHA, update.PullRequestHeadSHA) {
		return fmt.Errorf(
			"remote ref mismatch: heads/%s points at %q, the agent reported %q",
			publishBranch, remoteSHA, update.PullRequestHeadSHA)
	}

	return nil
}
