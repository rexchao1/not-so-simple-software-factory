package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestHealthRegistrationChangedWhenOneRuntimeLosesReadiness(t *testing.T) {
	previous := health{
		State: "healthy",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimePi, Status: protocol.CapabilityReady},
		},
	}
	next := previous
	next.Capabilities = append([]protocol.Capability(nil), previous.Capabilities...)
	next.Capabilities[0].Status = protocol.CapabilityUnauthenticated

	if !healthRegistrationChanged(previous, next) {
		t.Fatal("runtime readiness change did not invalidate the advertised registration")
	}
}

func TestHealthRegistrationUnchangedForEquivalentCapabilities(t *testing.T) {
	previous := health{
		State: "healthy",
		Capabilities: []protocol.Capability{
			{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
		},
		SourceAccess: []protocol.SourceAccess{{Provider: "github", Hostname: "github.com"}},
	}
	next := previous
	next.Capabilities = append([]protocol.Capability(nil), previous.Capabilities...)
	next.SourceAccess = append([]protocol.SourceAccess(nil), previous.SourceAccess...)

	if healthRegistrationChanged(previous, next) {
		t.Fatal("equivalent health invalidated the advertised registration")
	}
}

func TestStaleRegistrationCannotReenableClaimingAfterCapabilityChange(t *testing.T) {
	manager := &Manager{
		health: health{
			State: "healthy",
			Capabilities: []protocol.Capability{
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimePi, Status: protocol.CapabilityReady},
			},
		},
		registered: true,
	}
	_, staleGeneration := manager.registrationSnapshot()
	next := manager.health
	next.Capabilities = append([]protocol.Capability(nil), manager.health.Capabilities...)
	next.Capabilities[0].Status = protocol.CapabilityUnauthenticated
	manager.setHealth(next)

	manager.stateMutex.Lock()
	manager.completeRegistrationLocked(staleGeneration)
	registered := manager.registered
	manager.stateMutex.Unlock()
	if registered {
		t.Fatal("stale registration re-enabled claiming after runtime readiness changed")
	}
}

func TestUnchangedHealthCheckPreservesPendingClaim(t *testing.T) {
	manager := &Manager{
		health:     health{State: "healthy"},
		registered: true,
		pending:    make(map[string]context.CancelFunc),
	}
	claimContext, cancel, eligible := manager.beginClaim(context.Background(), "claim-1")
	defer cancel()
	if !eligible {
		t.Fatal("healthy registered Worker did not begin claim")
	}
	if !manager.beginHealthCheck() {
		t.Fatal("health check did not start")
	}
	result := make(chan bool, 1)
	go func() {
		result <- manager.endClaim(claimContext, "claim-1")
	}()
	select {
	case eligible := <-result:
		t.Fatalf("claim returned before health evidence arrived: eligible=%t", eligible)
	case <-time.After(20 * time.Millisecond):
	}
	manager.setHealth(health{State: "healthy"})
	select {
	case eligible := <-result:
		if !eligible {
			t.Fatal("unchanged healthy evidence invalidated the pending claim")
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not resume after health evidence arrived")
	}
	select {
	case <-claimContext.Done():
		t.Fatal("unchanged healthy evidence cancelled the pending claim")
	default:
	}
	if !manager.isHealthy() {
		t.Fatal("Worker did not become claimable after unchanged healthy evidence")
	}
}

func TestUnrelatedRuntimeHealthChangePreservesPendingClaimForValidation(t *testing.T) {
	manager := &Manager{
		config: Config{Runtime: protocol.RuntimeCodex, Runtimes: []string{protocol.RuntimeCodex, protocol.RuntimePi}},
		options: Options{RuntimeExecutables: map[string]string{
			protocol.RuntimeCodex: "codex",
			protocol.RuntimePi:    "pi",
		}},
		health: health{
			State: "healthy",
			Capabilities: []protocol.Capability{
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimeCodex, Status: protocol.CapabilityReady},
				{Kind: protocol.CapabilityKindRuntime, Name: protocol.RuntimePi, Status: protocol.CapabilityReady},
			},
		},
		registered: true,
		pending:    make(map[string]context.CancelFunc),
	}
	claimContext, cancel, eligible := manager.beginClaim(context.Background(), "claim-1")
	defer cancel()
	if !eligible || !manager.beginHealthCheck() {
		t.Fatal("healthy registered Worker did not begin claim and health check")
	}
	result := make(chan bool, 1)
	go func() {
		result <- manager.endClaim(claimContext, "claim-1")
	}()
	next := manager.health
	next.Capabilities = append([]protocol.Capability(nil), next.Capabilities...)
	next.Capabilities[0].Status = protocol.CapabilityUnauthenticated
	manager.setHealth(next)
	select {
	case <-claimContext.Done():
		t.Fatal("unrelated runtime change cancelled the pending claim")
	default:
	}
	select {
	case eligible := <-result:
		if !eligible {
			t.Fatal("healthy claim was discarded before its required runtime could be validated")
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not finish after refreshed capabilities arrived")
	}
	if !manager.supportsRuntime(protocol.RuntimePi) {
		t.Fatal("unchanged Pi runtime became unavailable")
	}
	if manager.supportsRuntime(protocol.RuntimeCodex) {
		t.Fatal("changed Codex runtime remained available")
	}
}

func TestUnhealthyHealthCheckInvalidatesPendingClaim(t *testing.T) {
	manager := &Manager{
		health:     health{State: "healthy"},
		logger:     slog.New(slog.DiscardHandler),
		registered: true,
		pending:    make(map[string]context.CancelFunc),
	}
	claimContext, cancel, eligible := manager.beginClaim(context.Background(), "claim-1")
	defer cancel()
	if !eligible || !manager.beginHealthCheck() {
		t.Fatal("healthy registered Worker did not begin claim and health check")
	}
	result := make(chan bool, 1)
	go func() {
		result <- manager.endClaim(claimContext, "claim-1")
	}()
	manager.setHealth(health{State: "unhealthy", Error: errors.New("runtime unavailable")})
	select {
	case <-claimContext.Done():
	case <-time.After(time.Second):
		t.Fatal("unhealthy evidence did not cancel the pending claim")
	}
	select {
	case eligible := <-result:
		if eligible {
			t.Fatal("claim became eligible after the Worker became unhealthy")
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not finish after the Worker became unhealthy")
	}
}
