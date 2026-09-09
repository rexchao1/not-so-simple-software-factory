package worker

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

func healthRegistrationChanged(previous, next health) bool {
	return previous.State != next.State ||
		!reflect.DeepEqual(previous.Capabilities, next.Capabilities) ||
		!reflect.DeepEqual(previous.SourceAccess, next.SourceAccess)
}

func (manager *Manager) setHealth(value health) bool {
	manager.stateMutex.Lock()
	previous := manager.health
	if manager.fatalHealth != nil {
		value = health{State: "unhealthy", Error: manager.fatalHealth}
	}
	registrationChanged := healthRegistrationChanged(previous, value)
	manager.health = value
	if registrationChanged {
		manager.registrationGeneration++
		manager.registered = false
	}
	if value.State != "healthy" {
		manager.cancelPendingClaimsLocked()
	}
	manager.finishHealthCheckLocked()
	manager.stateMutex.Unlock()
	if value.Error != nil && previous.State != value.State {
		manager.logger.Warn("worker_unhealthy", "error_class", "runtime_health", "error", value.Error)
	}
	if value.State == "healthy" && previous.State != "healthy" {
		manager.logger.Info("worker_healthy",
			"git_version", value.GitVersion,
			"runtimes", manager.config.Runtimes)
	}
	return registrationChanged
}

func (manager *Manager) markUnhealthy(errorClass string, err error) {
	manager.stateMutex.Lock()
	manager.fatalHealth = err
	manager.health.State = "unhealthy"
	manager.health.Error = err
	manager.registrationGeneration++
	manager.registered = false
	manager.cancelPendingClaimsLocked()
	manager.stateMutex.Unlock()
	manager.logger.Error("worker_unhealthy", "error_class", errorClass, "error", err)
}

func (manager *Manager) isHealthy() bool {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	return manager.health.State == "healthy" && manager.registered && !manager.healthCheckPending
}

func (manager *Manager) beginHealthCheck() bool {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if manager.healthCheckPending {
		return false
	}
	manager.healthCheckPending = true
	manager.healthCheckDone = make(chan struct{})
	return true
}

func (manager *Manager) finishHealthCheckLocked() {
	if !manager.healthCheckPending {
		return
	}
	manager.healthCheckPending = false
	close(manager.healthCheckDone)
	manager.healthCheckDone = nil
}

func (manager *Manager) registrationSnapshot() (protocol.WorkerRegistration, uint64) {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	return manager.registrationLocked(), manager.registrationGeneration
}

func (manager *Manager) registrationLocked() protocol.WorkerRegistration {
	repositories := make([]protocol.RepositoryRegistration, 0, len(manager.repositories))
	retained := make([]protocol.RetainedWorktree, 0, len(manager.retained))
	disposedAttemptIDs := make([]string, 0, len(manager.disposed))
	managedRepositoryIDs := make([]string, 0, len(manager.managedRepositoryIDs))
	for _, value := range manager.retained {
		retained = append(retained, value)
	}
	for attemptID := range manager.disposed {
		disposedAttemptIDs = append(disposedAttemptIDs, attemptID)
	}
	for repositoryID := range manager.managedRepositoryIDs {
		managedRepositoryIDs = append(managedRepositoryIDs, repositoryID)
	}
	sort.Strings(managedRepositoryIDs)
	for _, repository := range manager.repositories {
		repositories = append(repositories, protocol.RepositoryRegistration{
			Key: repository.Key, RemoteIdentity: repository.RemoteIdentity,
			RetainedCount: manager.retainedCounts[repository.RemoteIdentity],
		})
	}
	acceptsManagedRepositories := hasGitHubSourceAccess(manager.health.SourceAccess)
	return protocol.WorkerRegistration{
		Name:                       strings.TrimSpace(manager.config.Name),
		Labels:                     manager.config.Labels,
		WorkerVersion:              manager.options.WorkerVersion,
		ClaimProtocolVersion:       protocol.ClaimProtocolVersion,
		Runtime:                    manager.config.Runtime,
		RuntimeVersion:             manager.health.RuntimeVersion,
		Capabilities:               append([]protocol.Capability(nil), manager.health.Capabilities...),
		Capacity:                   manager.config.MaxConcurrent,
		ActiveCount:                len(manager.slots),
		Health:                     manager.health.State,
		Repositories:               repositories,
		SourceAccess:               append([]protocol.SourceAccess(nil), manager.health.SourceAccess...),
		AcceptsManagedRepositories: acceptsManagedRepositories,
		ManagedRepositoryIDs:       managedRepositoryIDs,
		RetainedWorktrees:          retained,
		CapacityHandoffVersion:     1,
		DisposedAttemptIDs:         disposedAttemptIDs,
		WeeklyLimit:                manager.health.WeeklyLimit,
	}
}

func hasGitHubSourceAccess(values []protocol.SourceAccess) bool {
	for _, value := range values {
		if value.Provider == "github" && value.Hostname == "github.com" {
			return true
		}
	}
	return false
}

func (manager *Manager) register(ctx context.Context) {
	manager.registrationMutex.Lock()
	defer manager.registrationMutex.Unlock()
	if manager.capacityHandoffs != 0 {
		manager.heartbeatWorkerLocked(ctx)
		return
	}
	manager.registerLocked(ctx)
}

func (manager *Manager) heartbeatWorkerLocked(ctx context.Context) {
	manager.stateMutex.Lock()
	generation := manager.registrationGeneration
	registrationCurrent := manager.hasAdvertisedRegistration && manager.advertisedGeneration == generation
	manager.stateMutex.Unlock()
	if !registrationCurrent {
		return
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if _, err := manager.client.heartbeatWorker(requestContext, manager.id); err != nil {
		manager.stateMutex.Lock()
		manager.registered = false
		manager.cancelPendingClaimsLocked()
		manager.stateMutex.Unlock()
		manager.logger.Warn("worker_heartbeat_failed", "error_class", apiErrorClass(err))
		return
	}
	manager.stateMutex.Lock()
	if manager.hasAdvertisedRegistration && manager.registrationGeneration == generation &&
		manager.advertisedGeneration == generation {
		manager.registered = true
	}
	manager.stateMutex.Unlock()
}

func (manager *Manager) registerLocked(ctx context.Context) {
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	registration, generation := manager.registrationSnapshot()
	if _, err := manager.client.register(requestContext, manager.id, registration); err != nil {
		manager.stateMutex.Lock()
		manager.registered = false
		manager.cancelPendingClaimsLocked()
		manager.stateMutex.Unlock()
		var apiError *APIError
		if errors.As(err, &apiError) && apiError.Status < 500 {
			manager.markUnhealthy("worker_registration", errors.New("worker configuration was rejected by the control plane"))
		}
		manager.logger.Warn("worker_registration_failed", "error_class", apiErrorClass(err))
		return
	}
	if err := manager.manifests.removeDisposedManifests(registration.DisposedAttemptIDs); err != nil {
		manager.markUnhealthy("attempt_manifest", err)
		return
	}
	if err := manager.manifests.clearDisposals(registration.DisposedAttemptIDs); err != nil {
		manager.markUnhealthy("disposal_journal", err)
		return
	}
	manager.stateMutex.Lock()
	for _, attemptID := range registration.DisposedAttemptIDs {
		delete(manager.disposed, attemptID)
	}
	manager.completeRegistrationLocked(generation)
	manager.stateMutex.Unlock()
}

func (manager *Manager) completeRegistrationLocked(generation uint64) {
	manager.registered = generation == manager.registrationGeneration
	if manager.registered {
		manager.advertisedGeneration = generation
		manager.hasAdvertisedRegistration = true
	}
	if !manager.registered {
		manager.cancelPendingClaimsLocked()
	}
}
