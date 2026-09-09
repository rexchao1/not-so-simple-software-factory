package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/owainlewis/factory/internal/protocol"
)

type repositoryLockEntry struct {
	available  chan struct{}
	references int
	poisoned   error
}

// repositoryLockSet serializes Git metadata changes for one repository while
// allowing unrelated repositories to prepare concurrently. Healthy entries
// exist only while held or awaited. Poisoned entries remain fail-closed and are
// bounded by the worker's configured and managed repository sets.
type repositoryLockSet struct {
	mutex   sync.Mutex
	entries map[string]*repositoryLockEntry
}

func (locks *repositoryLockSet) acquire(ctx context.Context, key string) (func(), error) {
	if key == "" {
		return nil, errors.New("repository coordination key is required")
	}
	locks.mutex.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*repositoryLockEntry)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &repositoryLockEntry{available: make(chan struct{}, 1)}
		entry.available <- struct{}{}
		locks.entries[key] = entry
	}
	entry.references++
	locks.mutex.Unlock()

	select {
	case <-ctx.Done():
		locks.removeReference(key, entry)
		return nil, ctx.Err()
	case <-entry.available:
	}
	locks.mutex.Lock()
	poisoned := entry.poisoned
	locks.mutex.Unlock()
	if poisoned != nil {
		entry.available <- struct{}{}
		locks.removeReference(key, entry)
		return nil, fmt.Errorf("repository coordination is unavailable: %w", poisoned)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.available <- struct{}{}
			locks.removeReference(key, entry)
		})
	}, nil
}

func (locks *repositoryLockSet) removeReference(key string, entry *repositoryLockEntry) {
	locks.mutex.Lock()
	defer locks.mutex.Unlock()
	entry.references--
	if entry.references == 0 && entry.poisoned == nil && locks.entries[key] == entry {
		delete(locks.entries, key)
	}
}

func (locks *repositoryLockSet) poison(key string, cause error) {
	locks.mutex.Lock()
	defer locks.mutex.Unlock()
	if entry := locks.entries[key]; entry != nil && entry.poisoned == nil {
		entry.poisoned = cause
	}
}

func managedRepositoryCoordinationKey(repositoryID string) string {
	return "managed:" + repositoryID
}

func legacyRepositoryCoordinationKey(repository Repository) string {
	if repository.Path != "" {
		return "legacy-path:" + repository.Path
	}
	return "legacy-remote:" + remoteIdentityComparisonKey(repository.RemoteIdentity)
}

func repositoryCoordinationKey(repository Repository) string {
	if repository.coordinationKey != "" {
		return repository.coordinationKey
	}
	return legacyRepositoryCoordinationKey(repository)
}

func (manager *Manager) coordinationKeyForManifest(manifest attemptManifest) string {
	if repository, exists := manager.repositoriesByKey[manifest.RepositoryKey]; exists &&
		repository.Path == manifest.RepositoryPath {
		return repository.coordinationKey
	}
	return managedRepositoryCoordinationKey(manifest.RepositoryID)
}

func (manager *Manager) reserveManagedRepositoryCache(repositoryID string) (bool, error) {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	if manager.managedRepositoryIDs == nil {
		manager.managedRepositoryIDs = make(map[string]bool)
	}
	if manager.managedRepositoryReservations == nil {
		manager.managedRepositoryReservations = make(map[string]bool)
	}
	if manager.managedRepositoryIDs[repositoryID] || manager.managedRepositoryReservations[repositoryID] {
		return false, nil
	}
	if len(manager.managedRepositoryIDs)+len(manager.managedRepositoryReservations) >=
		protocol.MaxRepositoryCacheEntries {
		return false, fmt.Errorf("managed repository cache limit of %d entries reached",
			protocol.MaxRepositoryCacheEntries)
	}
	manager.managedRepositoryReservations[repositoryID] = true
	return true, nil
}

func (manager *Manager) finishManagedRepositoryCacheReservation(repositoryID string, installed bool) {
	manager.stateMutex.Lock()
	defer manager.stateMutex.Unlock()
	delete(manager.managedRepositoryReservations, repositoryID)
	if installed {
		manager.managedRepositoryIDs[repositoryID] = true
	}
}
