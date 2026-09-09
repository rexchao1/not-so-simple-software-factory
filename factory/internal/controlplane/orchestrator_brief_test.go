package controlplane

import (
	"context"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestAdmitWorkPersistsOrchestratorBrief(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, "github.com/example/brief")
	admission, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "brief-00000000-0000-4000-8000-000000000001",
		Repository: repository.RemoteIdentity, Name: "Fix queue", Spec: "Fix queue routing.",
		Runtime: "claude-code", Source: protocol.WorkSourceOrchestrator,
		Brief: &protocol.WorkBrief{Context: "Queue routing", Why: "Unblocks work", Risk: "high — tickets stall", Work: "Go scheduler"},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.Run(context.Background(), admission.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Brief == nil || detail.Run.Brief.Risk != "high — tickets stall" {
		t.Fatalf("brief = %#v", detail.Run.Brief)
	}
}

func TestAdmitWorkRefusesNonOrchestratorBrief(t *testing.T) {
	store := newTestStore(t)
	repository := registerTestRepository(t, store, "github.com/example/no-brief")
	_, _, err := store.AdmitWork(context.Background(), protocol.AdmitWorkRequest{
		RequestKey: "brief-00000000-0000-4000-8000-000000000002",
		Repository: repository.RemoteIdentity, Name: "Fix queue", Spec: "Fix queue routing.",
		Runtime: "claude-code", Source: protocol.WorkSourceCockpit,
		Brief: &protocol.WorkBrief{Context: "Queue routing"},
	})
	if !serviceErrorCode(err, "brief_not_permitted") {
		t.Fatalf("error = %v", err)
	}
}
