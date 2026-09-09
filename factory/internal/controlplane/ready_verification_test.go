package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	testReadyRepository   = "github.com/octocat/factory-scratch"
	testReadyBranch       = "factory/work-b22e40aab3484738"
	testReadyHeadSHA      = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	testReadyPullRequest  = "https://github.com/octocat/factory-scratch/pull/2"
	testReadyPullKey      = "octocat/factory-scratch#2"
	testReadyBranchRefKey = "octocat/factory-scratch@factory/work-b22e40aab3484738"
)

func readyUpdate() protocol.AttemptUpdateRequest {
	// protocol.AttemptUpdateRequest. Shape borrowed from the existing
	// ready-path callers at resume_test.go and agent_update_test.go.
	return protocol.AttemptUpdateRequest{
		Status:                protocol.WorkUpdateReady,
		PullRequestURL:        testReadyPullRequest,
		PullRequestHeadBranch: testReadyBranch,
		PullRequestHeadSHA:    testReadyHeadSHA,
	}
}

// verifyingFake is a GitHub whose recorded facts agree with readyUpdate, so
// verification passes. Tests that need a failure mutate exactly one fact.
func verifyingFake() *fakeGitHub {
	fake := newFakeGitHub()
	fake.pullRequests[testReadyPullKey] = fakePullRequest{
		HeadRepo: "octocat/factory-scratch",
		HeadRef:  testReadyBranch,
		HeadSHA:  testReadyHeadSHA,
		State:    "open",
	}
	fake.branchRefs[testReadyBranchRefKey] = testReadyHeadSHA
	return fake
}

func TestVerifyReadyDeliveryAcceptsMatchingEvidence(t *testing.T) {
	err := verifyReadyDelivery(t.Context(), verifyingFake(),
		testReadyRepository, testReadyBranch, readyUpdate())
	if err != nil {
		t.Fatalf("matching evidence was rejected: %v", err)
	}
}

func TestVerifyReadyDeliveryRejectsEachClauseIndependently(t *testing.T) {
	// One case per INV-3 clause the server can observe. Each mutates exactly
	// one fact, so a passing test proves that clause is actually checked rather
	// than incidentally satisfied by another.
	for _, testCase := range []struct {
		name    string
		mutate  func(*fakeGitHub, *protocol.AttemptUpdateRequest)
		wantErr string
	}{
		{
			name: "repository",
			mutate: func(f *fakeGitHub, _ *protocol.AttemptUpdateRequest) {
				pr := f.pullRequests[testReadyPullKey]
				pr.HeadRepo = "someone-else/factory-scratch"
				f.pullRequests[testReadyPullKey] = pr
			},
			wantErr: "repository",
		},
		{
			name: "publish branch",
			mutate: func(f *fakeGitHub, _ *protocol.AttemptUpdateRequest) {
				pr := f.pullRequests[testReadyPullKey]
				pr.HeadRef = "factory/some-other-branch"
				f.pullRequests[testReadyPullKey] = pr
			},
			wantErr: "publish branch",
		},
		{
			name: "remote ref",
			mutate: func(f *fakeGitHub, _ *protocol.AttemptUpdateRequest) {
				f.branchRefs[testReadyBranchRefKey] = "ffffffffffffffffffffffffffffffffffffffff"
			},
			wantErr: "remote ref",
		},
		{
			name: "pull request head",
			mutate: func(_ *fakeGitHub, update *protocol.AttemptUpdateRequest) {
				update.PullRequestHeadSHA = "0000000000000000000000000000000000000000"
			},
			wantErr: "pull request head",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := verifyingFake()
			update := readyUpdate()
			testCase.mutate(fake, &update)
			err := verifyReadyDelivery(t.Context(), fake,
				testReadyRepository, testReadyBranch, update)
			if err == nil {
				t.Fatal("mismatched evidence was accepted")
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error %q does not name the failing clause %q", err, testCase.wantErr)
			}
		})
	}
}

func TestVerifyReadyDeliveryRefusesWhenUnconfigured(t *testing.T) {
	// A server with no GitHub credential cannot satisfy INV-3, so it must
	// refuse a ready outcome rather than accept one it did not verify. Failing
	// closed is the rule for this phase.
	err := verifyReadyDelivery(t.Context(), nil,
		testReadyRepository, testReadyBranch, readyUpdate())
	if err == nil {
		t.Fatal("an unconfigured server accepted a ready outcome unverified")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error %q does not explain that the server has no GitHub credential", err)
	}
}

func TestVerifyReadyDeliveryRefusesWhenGitHubIsUnreachable(t *testing.T) {
	// An outage must not be read as a pass. The fake with no recorded pull
	// request stands in for any read that fails.
	err := verifyReadyDelivery(t.Context(), newFakeGitHub(),
		testReadyRepository, testReadyBranch, readyUpdate())
	if err == nil {
		t.Fatal("a failed GitHub read was treated as a successful verification")
	}
}

// agreeWithReadyEvidence installs a GitHub that confirms exactly the delivery
// evidence an update reports, so a test about something else can still get a
// ready outcome accepted. It exists because INV-3 verification made every
// ready path in the suite depend on a GitHub answer.
//
// It is deliberately permissive: it agrees with whatever it is handed. Nothing
// here proves verification works. That is the job of the clause tests above
// and of the INV-8 table test, both of which supply disagreeing facts.
func agreeWithReadyEvidence(t *testing.T, store *Store, update protocol.AttemptUpdateRequest) {
	t.Helper()
	fake, _ := store.github.(*fakeGitHub)
	if fake == nil {
		fake = newFakeGitHub()
		store.github = fake
	}
	owner, repository, number, err := parsePullRequestURL(update.PullRequestURL)
	if err != nil {
		t.Fatalf("the test reported a pull request URL the server cannot parse: %v", err)
	}
	fake.pullRequests[fake.key(owner, repository, number)] = fakePullRequest{
		HeadRepo: owner + "/" + repository,
		HeadRef:  update.PullRequestHeadBranch,
		HeadSHA:  update.PullRequestHeadSHA,
		State:    "open",
	}
	fake.branchRefs[fmt.Sprintf("%s/%s@%s", owner, repository, update.PullRequestHeadBranch)] =
		update.PullRequestHeadSHA
}

func TestAppendAgentUpdateRefusesAnUnverifiableReady(t *testing.T) {
	// The verification is only worth anything if the outcome path enforces it.
	// This drives AppendAgentUpdate, not verifyReadyDelivery directly.
	store := newTestStore(t)
	run, claim := claimRunningAgentWork(t, store, "ready-verification-refused")
	ready := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "22000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Pull request is ready.",
		PullRequestURL:        "https://github.com/owainlewis/factory/pull/342",
		PullRequestHeadBranch: run.Sessions[0].Target.PublishBranch,
		PullRequestHeadSHA:    strings.Repeat("a", 40),
	}
	agreeWithReadyEvidence(t, store, ready)

	// GitHub says the branch points somewhere else. The agent's word alone is
	// exactly what INV-3 refuses to accept.
	fake := store.github.(*fakeGitHub)
	for key := range fake.branchRefs {
		fake.branchRefs[key] = strings.Repeat("f", 40)
	}

	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, ready); !serviceErrorCode(err, "ready_verification_failed") {
		t.Fatalf("an unverifiable ready was accepted, error = %v", err)
	}

	// And nothing was recorded. A refused delivery must leave no outcome.
	var updates int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM work_updates WHERE attempt_id = ? AND status = 'ready'`,
		claim.Attempt.ID).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("a refused ready left %d outcome rows behind", updates)
	}
}

func TestAppendAgentUpdateRecordsHowTheReadyWasVerified(t *testing.T) {
	store := newTestStore(t)
	run, claim := claimRunningAgentWork(t, store, "ready-verification-recorded")
	ready := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "22100000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Pull request is ready.",
		PullRequestURL:        "https://github.com/owainlewis/factory/pull/342",
		PullRequestHeadBranch: run.Sessions[0].Target.PublishBranch,
		PullRequestHeadSHA:    strings.Repeat("a", 40),
	}
	agreeWithReadyEvidence(t, store, ready)
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, ready); err != nil {
		t.Fatal(err)
	}

	var verifiedAt *int64
	var source string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT delivery_verified_at, delivery_verification_source FROM sessions WHERE id = ?`,
		run.Sessions[0].ID).Scan(&verifiedAt, &source); err != nil {
		t.Fatal(err)
	}
	if verifiedAt == nil {
		t.Fatal("a verified ready recorded no delivery_verified_at")
	}
	if source != "server-github" {
		t.Fatalf("delivery_verification_source = %q, want server-github", source)
	}
}

func TestTheReplayProbeMakesNoGitHubCall(t *testing.T) {
	// The worker sends a ReplayOnly probe before every real forward. If the
	// probe verified, every retry would double the GitHub calls and a GitHub
	// outage could reject the replay of an outcome that is already durable.
	store := newTestStore(t)
	run, claim := claimRunningAgentWork(t, store, "ready-verification-replay")
	ready := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "22200000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Pull request is ready.",
		PullRequestURL:        "https://github.com/owainlewis/factory/pull/342",
		PullRequestHeadBranch: run.Sessions[0].Target.PublishBranch,
		PullRequestHeadSHA:    strings.Repeat("a", 40),
	}
	agreeWithReadyEvidence(t, store, ready)
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, ready); err != nil {
		t.Fatal(err)
	}
	fake := store.github.(*fakeGitHub)
	afterReady := len(fake.calls)
	if afterReady == 0 {
		t.Fatal("the ready outcome made no GitHub call, so it was never verified")
	}

	// A probe for an already stored request must not touch GitHub at all.
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, ReplayOnly: true, RequestID: ready.RequestID,
		Status: ready.Status, Message: ready.Message, PullRequestURL: ready.PullRequestURL,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != afterReady {
		t.Fatalf("the replay probe made %d extra GitHub calls: %v",
			len(fake.calls)-afterReady, fake.calls[afterReady:])
	}
}
