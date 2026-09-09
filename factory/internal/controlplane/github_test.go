package controlplane

import (
	"os"
	"testing"
)

func TestParsePullRequestURL(t *testing.T) {
	// Ported from parseGitHubPullRequestURL, internal/worker/agent_update.go:423-440,
	// which already rejects userinfo, query, and fragment. Keep the two in step:
	// the worker rejecting a URL the server accepts, or the reverse, is a bug.
	for _, testCase := range []struct {
		name   string
		url    string
		owner  string
		repo   string
		number int
		bad    bool
	}{
		{name: "plain", url: "https://github.com/octocat/factory-scratch/pull/2", owner: "octocat", repo: "factory-scratch", number: 2},
		{name: "trailing slash", url: "https://github.com/octocat/factory-scratch/pull/2/", owner: "octocat", repo: "factory-scratch", number: 2},
		// The worker accepts a mixed-case host, so the server must too. A URL
		// the worker already validated must not be refused here.
		{name: "uppercase host", url: "https://GitHub.com/octocat/factory-scratch/pull/2", owner: "octocat", repo: "factory-scratch", number: 2},
		{name: "userinfo", url: "https://evil@github.com/octocat/factory-scratch/pull/2", bad: true},
		{name: "query", url: "https://github.com/octocat/factory-scratch/pull/2?x=1", bad: true},
		{name: "fragment", url: "https://github.com/octocat/factory-scratch/pull/2#c", bad: true},
		{name: "wrong host", url: "https://gitlab.com/octocat/factory-scratch/pull/2", bad: true},
		{name: "not a pull", url: "https://github.com/octocat/factory-scratch/issues/2", bad: true},
		{name: "http", url: "http://github.com/octocat/factory-scratch/pull/2", bad: true},
		{name: "zero", url: "https://github.com/octocat/factory-scratch/pull/0", bad: true},
		{name: "negative", url: "https://github.com/octocat/factory-scratch/pull/-1", bad: true},
		{name: "not a number", url: "https://github.com/octocat/factory-scratch/pull/two", bad: true},
		{name: "escaped separator in owner", url: "https://github.com/rex%2Fchao1/factory-scratch/pull/2", bad: true},
		{name: "too many segments", url: "https://github.com/octocat/factory-scratch/pull/2/files", bad: true},
		{name: "empty", url: "", bad: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			owner, repo, number, err := parsePullRequestURL(testCase.url)
			if testCase.bad {
				if err == nil {
					t.Fatalf("accepted %q as %s/%s#%d", testCase.url, owner, repo, number)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected %q: %v", testCase.url, err)
			}
			if owner != testCase.owner || repo != testCase.repo || number != testCase.number {
				t.Fatalf("got %s/%s#%d, want %s/%s#%d",
					owner, repo, number, testCase.owner, testCase.repo, testCase.number)
			}
		})
	}
}

func TestFakeGitHubRecordsEveryCall(t *testing.T) {
	// The fake's whole purpose is to let a test assert that a merge did NOT
	// happen. If it does not record, the negative cases in the INV-8 table
	// test prove nothing.
	fake := newFakeGitHub()
	fake.pullRequests["octocat/factory-scratch#2"] = fakePullRequest{
		HeadRepo: "octocat/factory-scratch", HeadRef: "factory/work-abc", HeadSHA: "a1b2c3",
	}
	if _, err := fake.PullRequest(t.Context(), "octocat", "factory-scratch", 2); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.calls); got != 1 {
		t.Fatalf("calls recorded = %d, want 1", got)
	}
	if fake.merges != 0 {
		t.Fatalf("a read recorded %d merges", fake.merges)
	}
	if fake.mergeAttempts() != 0 {
		t.Fatalf("a read recorded %d merge attempts", fake.mergeAttempts())
	}
}

func TestLoadGitHubTokenRefusesAReadableFile(t *testing.T) {
	// The server's credential can merge code. A token file the rest of the
	// machine can read is refused rather than warned about.
	dir := t.TempDir()
	loose := dir + "/loose-token"
	if err := os.WriteFile(loose, []byte("ghp_example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGitHubToken(loose); err == nil {
		t.Fatal("a group and world readable token file was accepted")
	}

	tight := dir + "/tight-token"
	if err := os.WriteFile(tight, []byte("  ghp_example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := loadGitHubToken(tight)
	if err != nil {
		t.Fatal(err)
	}
	if token != "ghp_example" {
		t.Fatalf("token = %q, want it trimmed to ghp_example", token)
	}

	// No token file configured is not an error. It leaves the server without a
	// GitHub credential, and ready verification is what refuses to proceed.
	if token, err := loadGitHubToken(""); err != nil || token != "" {
		t.Fatalf("an unconfigured token file returned (%q, %v)", token, err)
	}

	empty := dir + "/empty-token"
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGitHubToken(empty); err == nil {
		t.Fatal("an empty token file was accepted as a credential")
	}
}
