package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const repositoryAcquisitionTimeout = 5 * time.Minute

func normalizeManagedGitHubIdentity(value string) (identity, slug string, err error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") ||
		!validGitHubOwner(parts[1]) || !validGitHubRepository(parts[2]) {
		return "", "", errors.New("managed repository identity must use github.com/owner/repository")
	}
	owner := strings.ToLower(parts[1])
	repository := strings.ToLower(parts[2])
	return "github.com/" + owner + "/" + repository, owner + "/" + repository, nil
}

func validGitHubOwner(value string) bool {
	if len(value) < 1 || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validGitHubRepository(value string) bool {
	if len(value) < 1 || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func (manager *Manager) repositoryForClaim(ctx context.Context, claim protocol.Claim) (Repository, error) {
	if repository, exists := manager.repositoriesByKey[claim.Repository.Key]; exists {
		if !sameRemoteIdentity(repository.RemoteIdentity, claim.Repository.RemoteIdentity) {
			return Repository{}, errors.New("claim repository identity does not match the configured repository")
		}
		return repository, nil
	}
	return manager.acquireManagedRepository(ctx, claim.Repository)
}

func (manager *Manager) acquireManagedRepository(
	ctx context.Context,
	claimRepository protocol.Repository,
) (Repository, error) {
	if !uuidPattern.MatchString(claimRepository.ID) {
		return Repository{}, errors.New("claim contains an invalid repository ID")
	}
	identity, slug, err := normalizeManagedGitHubIdentity(claimRepository.RemoteIdentity)
	if err != nil {
		return Repository{}, err
	}
	if !strings.EqualFold(identity, claimRepository.RemoteIdentity) {
		return Repository{}, errors.New("claim contains a non-canonical managed repository identity")
	}

	waitContext, cancelWait := context.WithTimeout(ctx, repositoryAcquisitionTimeout)
	release, err := manager.repositoryLocks.acquire(
		waitContext, managedRepositoryCoordinationKey(claimRepository.ID),
	)
	cancelWait()
	if err != nil {
		return Repository{}, err
	}
	defer release()
	acquisitionContext, cancelAcquisition := context.WithTimeout(ctx, repositoryAcquisitionTimeout)
	defer cancelAcquisition()

	cacheRoot, err := manager.prepareRepositoryCacheRoot()
	if err != nil {
		return Repository{}, err
	}
	target := filepath.Join(cacheRoot, claimRepository.ID)
	if info, inspectErr := os.Lstat(target); errors.Is(inspectErr, os.ErrNotExist) {
		reserved, reserveErr := manager.reserveManagedRepositoryCache(claimRepository.ID)
		if reserveErr != nil {
			return Repository{}, reserveErr
		}
		installed, cloneErr := manager.cloneManagedRepository(acquisitionContext, cacheRoot, target, slug)
		if reserved || installed {
			manager.finishManagedRepositoryCacheReservation(claimRepository.ID, installed)
		}
		if cloneErr != nil {
			return Repository{}, cloneErr
		}
	} else if inspectErr != nil {
		return Repository{}, fmt.Errorf("inspect managed repository cache: %w", inspectErr)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Repository{}, errors.New("managed repository cache entry must be a real directory")
	}

	repository, err := resolveRepository(claimRepository.Key, target, manager.options.GitExecutable)
	if err != nil {
		return Repository{}, fmt.Errorf("validate managed repository cache: %w", err)
	}
	if repository.Path != target {
		return Repository{}, errors.New("managed repository cache path escaped its assigned entry")
	}
	if !strings.EqualFold(repository.RemoteIdentity, identity) {
		return Repository{}, errors.New("managed repository cache origin does not match the assigned repository")
	}
	repository.coordinationKey = managedRepositoryCoordinationKey(claimRepository.ID)
	repository.BaseBranch, repository.BaseCommit, err = resolveBaseCommit(
		acquisitionContext, manager.options.GitExecutable, repository,
	)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve managed repository base: %w", err)
	}
	manager.finishManagedRepositoryCacheReservation(claimRepository.ID, true)
	return repository, nil
}

func (manager *Manager) prepareRepositoryCacheRoot() (string, error) {
	root := filepath.Join(manager.dataDirectory, "repositories")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create managed repository cache: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect managed repository cache: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("managed repository cache root must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize managed repository cache: %w", err)
	}
	return canonical, nil
}

func managedRepositoryCacheIDs(dataDirectory string) (map[string]bool, error) {
	root := filepath.Join(dataDirectory, "repositories")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]bool), nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect managed repository cache: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("managed repository cache root must be a real directory")
	}
	// New calls this only after taking the worker data-directory lock, so no
	// clone can still own one of these temporary directories. Removing them on
	// startup bounds disk use after a hard crash during gh repo clone.
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list managed repository cache: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".clone-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return nil, fmt.Errorf("remove interrupted managed repository clone: %w", err)
		}
	}
	return managedRepositoryCacheIDsFromRoot(root)
}

func managedRepositoryCacheIDsFromRoot(root string) (map[string]bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list managed repository cache: %w", err)
	}
	ids := make(map[string]bool)
	for _, entry := range entries {
		if uuidPattern.MatchString(entry.Name()) {
			ids[entry.Name()] = true
		}
	}
	return ids, nil
}

func (manager *Manager) cloneManagedRepository(
	ctx context.Context,
	root, target, slug string,
) (installed bool, resultErr error) {
	temporaryRoot, err := os.MkdirTemp(root, ".clone-")
	if err != nil {
		return false, fmt.Errorf("create managed repository clone directory: %w", err)
	}
	defer func() {
		if temporaryRoot != "" {
			if err := os.RemoveAll(temporaryRoot); err != nil && resultErr == nil {
				resultErr = fmt.Errorf("remove managed repository clone directory: %w", err)
			}
		}
	}()
	clonePath := filepath.Join(temporaryRoot, "repository")
	stdout, stderr, err := runCommand(
		ctx,
		manager.options.GitHubExecutable,
		"",
		256<<10,
		"repo", "clone", slug, clonePath, "--", "--no-checkout",
	)
	if err != nil {
		return false, commandFailure("clone managed GitHub repository", stdout, stderr, err)
	}
	repository, err := resolveRepository(slug, clonePath, manager.options.GitExecutable)
	if err != nil {
		return false, fmt.Errorf("validate cloned managed repository: %w", err)
	}
	expectedIdentity := "github.com/" + slug
	if !strings.EqualFold(repository.RemoteIdentity, expectedIdentity) {
		return false, errors.New("cloned managed repository origin does not match the assigned repository")
	}
	if err := os.Rename(clonePath, target); err != nil {
		return false, fmt.Errorf("install managed repository cache entry: %w", err)
	}
	installed = true
	if err := os.Remove(temporaryRoot); err != nil {
		return true, fmt.Errorf("remove managed repository clone directory: %w", err)
	}
	temporaryRoot = ""
	return true, nil
}
