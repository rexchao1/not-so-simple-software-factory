package controlplane

import (
	"context"
	"fmt"
)

type fakePullRequest struct {
	HeadRepo string
	HeadRef  string
	HeadSHA  string
	Merged   bool
	State    string
}

// fakeGitHub follows the fake_cloud.go precedent: deterministic, in-process,
// and recording enough that a test can assert something did NOT happen. The
// merge counter is the important part. The negative cases in the INV-8 table
// test are only meaningful if a test can prove no merge was attempted.
type fakeGitHub struct {
	pullRequests map[string]fakePullRequest
	branchRefs   map[string]string
	failing      map[string][]string
	calls        []string
	merges       int
	mergeErr     error
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		pullRequests: map[string]fakePullRequest{},
		branchRefs:   map[string]string{},
		failing:      map[string][]string{},
	}
}

func (f *fakeGitHub) key(owner, repo string, number int) string {
	return fmt.Sprintf("%s/%s#%d", owner, repo, number)
}

func (f *fakeGitHub) PullRequest(_ context.Context, owner, repo string, number int) (pullRequestState, error) {
	key := f.key(owner, repo, number)
	f.calls = append(f.calls, "PullRequest "+key)
	found, ok := f.pullRequests[key]
	if !ok {
		return pullRequestState{}, fmt.Errorf("github: no pull request %s", key)
	}
	return pullRequestState(found), nil
}

func (f *fakeGitHub) BranchRef(_ context.Context, owner, repo, branch string) (string, error) {
	key := fmt.Sprintf("%s/%s@%s", owner, repo, branch)
	f.calls = append(f.calls, "BranchRef "+key)
	sha, ok := f.branchRefs[key]
	if !ok {
		return "", fmt.Errorf("github: no ref %s", key)
	}
	return sha, nil
}

func (f *fakeGitHub) FailingChecks(_ context.Context, owner, repo, sha string) ([]string, error) {
	key := fmt.Sprintf("%s/%s@%s", owner, repo, sha)
	f.calls = append(f.calls, "FailingChecks "+key)
	return f.failing[key], nil
}

func (f *fakeGitHub) MergePullRequest(_ context.Context, owner, repo string, number int, sha string) error {
	f.calls = append(f.calls, fmt.Sprintf("MergePullRequest %s sha=%s", f.key(owner, repo, number), sha))
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merges++
	return nil
}

// mergeAttempts counts how many times a merge was tried, successful or not.
// A refused merge must not be retried silently, and that is only assertable if
// attempts are counted separately from successes.
func (f *fakeGitHub) mergeAttempts() int {
	attempts := 0
	for _, call := range f.calls {
		if len(call) >= 16 && call[:16] == "MergePullRequest" {
			attempts++
		}
	}
	return attempts
}
