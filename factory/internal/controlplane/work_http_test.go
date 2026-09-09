package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestWorkAnswerHTTPStoresTrustedContext(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	input := protocol.WorkAnswerRequest{
		RequestID: "64000000-0000-4000-8000-000000000001",
		Message:   "Preserve the existing response shape.",
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		server.URL+"/api/v1/work/"+work.ID+"/answer", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var answer protocol.WorkAnswer
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&answer) != nil ||
		answer.WorkID != work.ID || answer.Message != input.Message {
		t.Fatalf("answer response = %d %#v", response.StatusCode, answer)
	}
}

// TestWorkAnswerHTTPCarriesActor proves the answer endpoint returns the actor
// that answered, so an HTTP caller can see who gave the answer without a
// second read of the Work.
func TestWorkAnswerHTTPCarriesActor(t *testing.T) {
	store, _, _, work := needsInputWork(t)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	input := protocol.WorkAnswerRequest{
		RequestID: "64000000-0000-4000-8000-000000000002",
		Message:   "Preserve the existing response shape.",
		Actor:     "overseer",
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		server.URL+"/api/v1/work/"+work.ID+"/answer", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var answer protocol.WorkAnswer
	if response.StatusCode != http.StatusOK || json.Unmarshal(raw, &answer) != nil ||
		answer.WorkID != work.ID || answer.Message != input.Message || answer.Actor != "overseer" ||
		!bytes.Contains(raw, []byte(`"actor":"overseer"`)) {
		t.Fatalf("answer response = %d %s", response.StatusCode, raw)
	}
}

// TestAdmitWorkHTTPRejectsSelfApprovedCockpitSubmission proves INV-1 at the
// HTTP boundary, not just at the store layer: a cockpit submission cannot set
// pre_approved, since a cockpit submission is exactly the case where no
// human has necessarily reviewed the spec yet.
func TestAdmitWorkHTTPRejectsSelfApprovedCockpitSubmission(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, "github.com/example/http-admit")
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	input := protocol.AdmitWorkRequest{
		RequestKey:  "71000000-0000-4000-8000-000000000001",
		Repository:  repository.RemoteIdentity,
		Name:        "Self-approved cockpit submission",
		Spec:        "spec text",
		Runtime:     "claude-code",
		Source:      protocol.WorkSourceCockpit,
		PreApproved: true,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, server.URL+"/api/v1/work", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	var errorBody protocol.ErrorBody
	if err := json.NewDecoder(response.Body).Decode(&errorBody); err != nil {
		t.Fatal(err)
	}
	if errorBody.Error.Code != "pre_approval_not_permitted" {
		t.Fatalf("error code = %q, want pre_approval_not_permitted", errorBody.Error.Code)
	}
}

// TestApproveWorkHTTP proves the human gate is reachable over HTTP: a draft
// admitted through POST /api/v1/work can be approved through
// POST /api/v1/work/{work_id}/approve, and the approval leaves the session
// out of draft with the approving actor recorded. The worker is eligible for
// the admitted repository, so the approval takes the queued path rather than
// stalling blocked, which is the half of the gate that dispatches.
func TestApproveWorkHTTP(t *testing.T) {
	store := newTestStore(t)
	eligibleWorkerForAdmission(t, store, "worker-approve-http")
	admission := admitDraftForTest(t, store)
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)

	input := protocol.ApproveWorkRequest{Actor: "octocat"}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		server.URL+"/api/v1/work/"+admission.WorkIDs[0]+"/approve", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var work protocol.Work
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&work) != nil {
		t.Fatalf("approve response = %d", response.StatusCode)
	}
	if work.State != protocol.SessionQueued {
		t.Fatalf("state = %q, want queued", work.State)
	}
	if work.ApprovedBy != input.Actor {
		t.Fatalf("approved_by = %q, want %q", work.ApprovedBy, input.Actor)
	}
	assertQueued(t, store, admission.WorkIDs[0])
}
