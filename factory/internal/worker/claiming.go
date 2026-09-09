package worker

import (
	"context"
	"errors"

	"github.com/owainlewis/factory/internal/protocol"
)

func (manager *Manager) reserveAndClaim(ctx context.Context) {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if manager.claiming || manager.health.State != "healthy" || !manager.registered || manager.healthCheckPending {
		return
	}
	select {
	case manager.slots <- struct{}{}:
		manager.claiming = true
		manager.waitGroup.Add(1)
		go manager.claimOnce(ctx)
	default:
	}
}

func (manager *Manager) claimOnce(ctx context.Context) {
	defer manager.waitGroup.Done()
	claimReservationFinished := false
	finishClaimReservation := func() {
		if claimReservationFinished {
			return
		}
		manager.stateMutex.Lock()
		manager.claiming = false
		manager.stateMutex.Unlock()
		claimReservationFinished = true
	}
	defer finishClaimReservation()
	release := true
	defer func() {
		if release {
			<-manager.slots
		}
	}()
	requestID, err := manager.randomUUID()
	if err != nil {
		manager.markUnhealthy("randomness", err)
		return
	}
	token, err := manager.randomSecret()
	if err != nil {
		manager.markUnhealthy("randomness", err)
		return
	}
	claimContext, cancelClaim, eligible := manager.beginClaim(ctx, requestID)
	if !eligible {
		cancelClaim()
		return
	}
	claim, err := manager.client.claim(claimContext, manager.id, protocol.ClaimRequest{
		RequestID: requestID, LeaseToken: token,
	}, manager.options.TransportBackoffMin, manager.options.TransportBackoffMax)
	eligible = manager.endClaim(claimContext, requestID)
	cancelClaim()
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			manager.logger.Warn("worker_claim_failed", "error_class", apiErrorClass(err))
		}
		return
	}
	if claim == nil {
		return
	}
	if !eligible {
		handle := &attemptHandle{expiry: claim.Attempt.LeaseExpiresAt}
		manager.finishWithoutWorktree(*claim, token, handle, "failed",
			errors.New("worker became ineligible before attempt start"))
		return
	}
	manager.stateMutex.Lock()
	if manager.seen[claim.Attempt.ID] {
		manager.stateMutex.Unlock()
		manager.logger.Warn("duplicate_claim_ignored", "attempt_id", claim.Attempt.ID)
		return
	}
	manager.seen[claim.Attempt.ID] = true
	manager.stateMutex.Unlock()
	release = false
	finishClaimReservation()
	// A successful claim proves work is available. Refill another free slot
	// immediately instead of waiting for the next polling interval. Only one
	// claim request may be in flight, so an empty queue still produces at most
	// one request per interval.
	manager.reserveAndClaim(ctx)
	manager.runAttempt(ctx, *claim, token)
	<-manager.slots
	if ctx.Err() == nil {
		manager.reserveAndClaim(ctx)
	}
}

func (manager *Manager) beginClaim(
	parent context.Context,
	requestID string,
) (context.Context, context.CancelFunc, bool) {
	ctx, cancel := context.WithCancel(parent)
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if manager.health.State != "healthy" || !manager.registered || manager.healthCheckPending {
		return ctx, cancel, false
	}
	manager.pending[requestID] = cancel
	return ctx, cancel, true
}

func (manager *Manager) endClaim(ctx context.Context, requestID string) bool {
	for {
		manager.stateMutex.Lock()
		if _, pending := manager.pending[requestID]; !pending {
			manager.stateMutex.Unlock()
			return false
		}
		if !manager.healthCheckPending {
			delete(manager.pending, requestID)
			// The claim began against an advertised registration. If the probe
			// changed unrelated capabilities, validate the decoded claim against
			// the refreshed health instead of discarding the committed response.
			eligible := manager.health.State == "healthy"
			manager.stateMutex.Unlock()
			return eligible
		}
		done := manager.healthCheckDone
		manager.stateMutex.Unlock()

		select {
		case <-done:
		case <-ctx.Done():
			manager.stateMutex.Lock()
			delete(manager.pending, requestID)
			manager.stateMutex.Unlock()
			return false
		}
	}
}

func (manager *Manager) cancelPendingClaimsLocked() {
	for requestID, cancel := range manager.pending {
		cancel()
		delete(manager.pending, requestID)
	}
}
