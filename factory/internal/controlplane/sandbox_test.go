package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func dockerProfileRequest(sandbox *protocol.Sandbox) protocol.SaveExecutionProfileRequest {
	return protocol.SaveExecutionProfileRequest{
		Name: "Sandboxed claude", Kind: protocol.BackendDocker, Runtime: protocol.RuntimeClaudeCode,
		Provider: "anthropic", Model: "claude-opus-5", TimeoutSeconds: 600,
		ResourceClass: "standard", MaxConcurrent: 2, Enabled: true, Healthy: true,
		Sandbox: sandbox,
	}
}

// A docker profile is creatable, which it was not before: the kind CHECK named
// fake_cloud_run and nothing else. It stores its posture and, unlike a fake,
// creates no synthetic worker, because a real Worker executes it.
func TestDockerProfileIsCreatableAndCreatesNoSyntheticWorker(t *testing.T) {
	store := newTestStore(t)
	profile, err := store.CreateExecutionProfile(context.Background(), dockerProfileRequest(
		&protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkNone, CPU: "2", Memory: "4g"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Kind != protocol.BackendDocker {
		t.Fatalf("kind = %q, want docker", profile.Kind)
	}
	if profile.Sandbox == nil || profile.Sandbox.Image != "factory/agent:1" ||
		profile.Sandbox.Network != protocol.NetworkNone || profile.Sandbox.CPU != "2" ||
		profile.Sandbox.Memory != "4g" {
		t.Fatalf("sandbox = %#v", profile.Sandbox)
	}
	if profile.SyntheticWorkerID != "" {
		t.Fatalf("synthetic worker = %q, want none for a docker profile", profile.SyntheticWorkerID)
	}
	var synthetic int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM workers WHERE synthetic = 1`).Scan(&synthetic); err != nil {
		t.Fatal(err)
	}
	if synthetic != 0 {
		t.Fatalf("synthetic workers = %d, want 0", synthetic)
	}
	// The read path returns what the write path stored.
	read, err := store.ExecutionProfile(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Sandbox == nil || read.Sandbox.Image != "factory/agent:1" {
		t.Fatalf("re-read sandbox = %#v", read.Sandbox)
	}
}

// allowlist is in design.md and is not implemented. It is rejected rather than
// quietly treated as open, so no Run can believe it is restricted when it is
// not.
func TestDockerProfileRejectsTheUnimplementedAllowlistPosture(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateExecutionProfile(context.Background(), dockerProfileRequest(
		&protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkAllowlist},
	))
	if !serviceErrorCode(err, "sandbox_network_unsupported") {
		t.Fatalf("error = %v, want sandbox_network_unsupported", err)
	}
}

// broker is implemented, so it is accepted and stored as itself. The control
// plane does not check whether the Worker that will run it holds a broker
// credential: it cannot know which Worker claims the run, and a profile that
// only validated against the machine the operator happened to be on would be
// wrong the moment a second Worker enrolled. The Worker makes that check.
func TestDockerProfileAcceptsTheBrokerPosture(t *testing.T) {
	store := newTestStore(t)
	profile, err := store.CreateExecutionProfile(context.Background(), dockerProfileRequest(
		&protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkBroker},
	))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Sandbox == nil || profile.Sandbox.Network != protocol.NetworkBroker {
		t.Fatalf("sandbox = %#v, want the broker posture", profile.Sandbox)
	}
	read, err := store.ExecutionProfile(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Sandbox == nil || read.Sandbox.Network != protocol.NetworkBroker {
		t.Fatalf("re-read sandbox = %#v, want the broker posture", read.Sandbox)
	}
}

func TestDockerProfileValidation(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name    string
		request protocol.SaveExecutionProfileRequest
		code    string
	}{
		{
			name:    "no sandbox at all",
			request: dockerProfileRequest(nil),
			code:    "invalid_sandbox",
		},
		{
			name:    "no image",
			request: dockerProfileRequest(&protocol.Sandbox{Network: protocol.NetworkNone}),
			code:    "invalid_sandbox_image",
		},
		{
			name:    "unknown posture",
			request: dockerProfileRequest(&protocol.Sandbox{Image: "alpine:3", Network: "host"}),
			code:    "invalid_sandbox_network",
		},
		{
			name: "a docker profile synthesizes nothing",
			request: func() protocol.SaveExecutionProfileRequest {
				request := dockerProfileRequest(&protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone})
				request.FakeOutcome = "succeeded"
				return request
			}(),
			code: "invalid_fake_cloud_outcome",
		},
		{
			name: "an unknown kind is still rejected",
			request: func() protocol.SaveExecutionProfileRequest {
				request := dockerProfileRequest(&protocol.Sandbox{Image: "alpine:3", Network: protocol.NetworkNone})
				request.Kind = "podman"
				return request
			}(),
			code: "invalid_execution_profile_kind",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := store.CreateExecutionProfile(context.Background(), testCase.request); !serviceErrorCode(err, testCase.code) {
				t.Fatalf("error = %v, want %s", err, testCase.code)
			}
		})
	}
}

// A fake profile still behaves exactly as it did, including its synthetic
// worker. The kind gate was widened, not replaced.
func TestFakeCloudProfileIsUnchanged(t *testing.T) {
	store := newTestStore(t)
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "API cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 600,
		ResourceClass: "standard", MaxConcurrent: 2, Enabled: true, Healthy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.FakeOutcome != "succeeded" || profile.FakeResult != "Fake Cloud Run Attempt completed." {
		t.Fatalf("fake defaults were lost: %#v", profile)
	}
	if profile.SyntheticWorkerID == "" || profile.Sandbox != nil {
		t.Fatalf("fake profile = %#v, want a synthetic worker and no sandbox", profile)
	}
}

// The posture is frozen onto the Run, not read from the profile at execution
// time, so editing the profile mid Run cannot change how a dispatched attempt
// is confined.
func TestSandboxIsFrozenOntoTheRunSnapshot(t *testing.T) {
	store := newTestStore(t)
	profile, err := store.CreateExecutionProfile(context.Background(), dockerProfileRequest(
		&protocol.Sandbox{Image: "factory/agent:1", Network: protocol.NetworkOpen},
	))
	if err != nil {
		t.Fatal(err)
	}
	repository := registerTestRepository(t, store, admissionRepositoryIdentity)
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Sandboxed work", Prompt: "Do the task.", Runtime: protocol.RuntimeClaudeCode,
		RepositoryIDs: []string{repository.ID}, ExecutionProfileID: profile.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "sandbox-freeze", ExecutionProfileID: profile.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.Execution.Backend != protocol.BackendDocker {
		t.Fatalf("frozen backend = %q, want docker", run.Run.Execution.Backend)
	}
	if run.Run.Execution.Sandbox == nil || run.Run.Execution.Sandbox.Image != "factory/agent:1" ||
		run.Run.Execution.Sandbox.Network != protocol.NetworkOpen {
		t.Fatalf("frozen sandbox = %#v", run.Run.Execution.Sandbox)
	}

	// Editing the profile afterwards must not reach the frozen Run.
	edit := dockerProfileRequest(&protocol.Sandbox{Image: "factory/agent:2", Network: protocol.NetworkNone})
	edit.ExpectedVersion = profile.Version
	if _, err := store.UpdateExecutionProfile(context.Background(), profile.ID, edit); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Run(context.Background(), run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Run.Execution.Sandbox == nil || reread.Run.Execution.Sandbox.Image != "factory/agent:1" ||
		reread.Run.Execution.Sandbox.Network != protocol.NetworkOpen {
		t.Fatalf("the frozen posture moved when the profile changed: %#v", reread.Run.Execution.Sandbox)
	}
}
