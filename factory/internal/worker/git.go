package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const gitCommandTimeout = 30 * time.Second

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type worktree struct {
	Path       string
	Branch     string
	BaseBranch string
	BaseCommit string
	HeadCommit string
}

type worktreeRecovery struct {
	WorkID                string
	PublishBranch         string
	CheckpointSHA         string
	PendingResumeSHA      string
	CheckpointPublished   bool
	PullRequestURL        string
	PullRequestHeadBranch string
	PullRequestHeadSHA    string
}

func resolveRepositories(config Config, gitExecutable string) ([]Repository, error) {
	keys := make([]string, 0, len(config.Repositories))
	for key := range config.Repositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	repositories := make([]Repository, 0, len(keys))
	paths := make(map[string]string)
	identities := make(map[string]string)
	for _, key := range keys {
		repository, err := resolveRepository(key, config.Repositories[key].Path, gitExecutable)
		if err != nil {
			return nil, fmt.Errorf("repository %q: %w", key, err)
		}
		repository.coordinationKey = legacyRepositoryCoordinationKey(repository)
		repository.BaseBranch = config.Repositories[key].BaseBranch
		if repository.BaseBranch != "" {
			ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
			err = validateBaseBranch(ctx, gitExecutable, repository, repository.BaseBranch)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("repository %q: %w", key, err)
			}
		}
		if previous := paths[repository.Path]; previous != "" {
			return nil, fmt.Errorf("repositories %q and %q resolve to the same path", previous, key)
		}
		identityKey := remoteIdentityComparisonKey(repository.RemoteIdentity)
		if previous := identities[identityKey]; previous != "" {
			return nil, fmt.Errorf("repositories %q and %q resolve to the same remote", previous, key)
		}
		paths[repository.Path] = key
		identities[identityKey] = key
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func resolveRepository(key, path, gitExecutable string) (Repository, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Repository{}, fmt.Errorf("inspect path: %w", err)
	}
	if !info.IsDir() {
		return Repository{}, errors.New("path is not a directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	stdout, stderr, commandErr := runCommand(ctx, gitExecutable, canonical, 64<<10, "rev-parse", "--is-inside-work-tree")
	cancel()
	if commandErr == nil && strings.TrimSpace(string(stdout)) != "true" {
		commandErr = errors.New("not a Git worktree")
	}
	var remote string
	if commandErr == nil {
		ctx, cancel = context.WithTimeout(context.Background(), gitCommandTimeout)
		stdout, stderr, commandErr = runCommand(ctx, gitExecutable, canonical, 64<<10, "remote", "get-url", "origin")
		cancel()
		if commandErr == nil {
			remote = strings.TrimSpace(string(stdout))
		}
	}
	if commandErr != nil {
		if !executableUnavailable(commandErr) {
			return Repository{}, commandFailure("verify Git repository", stdout, stderr, commandErr)
		}
		remote, err = readOriginRemote(canonical)
		if err != nil {
			return Repository{}, commandFailure("verify Git repository", stdout, stderr, commandErr)
		}
	}
	identity, err := normalizeRemoteIdentity(remote, canonical)
	if err != nil {
		return Repository{}, err
	}
	return Repository{Key: key, Path: canonical, RemoteIdentity: identity}, nil
}

func executableUnavailable(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var executableError *exec.Error
	return errors.As(err, &executableError) && errors.Is(executableError.Err, exec.ErrNotFound)
}

func readOriginRemote(repository string) (string, error) {
	gitPath := filepath.Join(repository, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", err
	}
	var configPath string
	if info.IsDir() {
		configPath = filepath.Join(gitPath, "config")
	} else if info.Mode().IsRegular() {
		body, err := os.ReadFile(gitPath)
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(body))
		if !strings.HasPrefix(line, "gitdir: ") {
			return "", errors.New(".git file has no gitdir")
		}
		gitDirectory := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(repository, gitDirectory)
		}
		gitDirectory, err = filepath.Abs(gitDirectory)
		if err != nil {
			return "", err
		}
		commonDirectory := gitDirectory
		if common, readErr := os.ReadFile(filepath.Join(gitDirectory, "commondir")); readErr == nil {
			commonDirectory = filepath.Join(gitDirectory, strings.TrimSpace(string(common)))
		}
		configPath = filepath.Join(commonDirectory, "config")
	} else {
		return "", errors.New(".git is neither a directory nor a gitdir file")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	inOrigin := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if inOrigin {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "url" {
				remote := strings.TrimSpace(value)
				if remote != "" {
					return remote, nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("origin remote is required")
}

func normalizeRemoteIdentity(remote, repositoryPath string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("origin remote is empty")
	}
	if prefix, path, found := strings.Cut(remote, ":"); found && strings.Contains(prefix, "@") {
		at := strings.LastIndex(prefix, "@")
		host := prefix[at+1:]
		if host == "" || path == "" {
			return "", errors.New("origin SSH remote is malformed")
		}
		return normalizeHostPath(host, path), nil
	}
	parsed, err := url.Parse(remote)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme == "file" {
			path, err := filepath.EvalSymlinks(parsed.Path)
			if err != nil {
				return "", fmt.Errorf("canonicalize file remote: %w", err)
			}
			return "file://" + filepath.ToSlash(path), nil
		}
		if parsed.Hostname() == "" || parsed.Path == "" {
			return "", errors.New("origin remote URL is malformed")
		}
		return normalizeHostPath(parsed.Host, parsed.Path), nil
	}
	path := remote
	if !filepath.IsAbs(path) {
		path = filepath.Join(repositoryPath, path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize origin path: %w", err)
	}
	return "file://" + filepath.ToSlash(canonical), nil
}

func normalizeHostPath(host, path string) string {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(host) + "/" + path
}

func remoteIdentityComparisonKey(value string) string {
	if strings.HasPrefix(strings.ToLower(value), "github.com/") {
		return strings.ToLower(value)
	}
	return value
}

func sameRemoteIdentity(left, right string) bool {
	return remoteIdentityComparisonKey(left) == remoteIdentityComparisonKey(right)
}

func prepareWorktree(
	ctx context.Context,
	gitExecutable, root string,
	repository Repository,
	sessionID, attemptID string,
	recovery ...worktreeRecovery,
) (worktree, error) {
	if !uuidPattern.MatchString(sessionID) || !uuidPattern.MatchString(attemptID) {
		return worktree{}, errors.New("server returned an invalid Session or attempt ID")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return worktree{}, fmt.Errorf("create worktree root: %w", err)
	}
	if info, err := os.Lstat(root); err != nil {
		return worktree{}, fmt.Errorf("inspect worktree root: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return worktree{}, errors.New("worktree root must be a real directory, not a symlink")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return worktree{}, fmt.Errorf("canonicalize worktree root: %w", err)
	}
	path := filepath.Join(root, attemptID)
	if _, err := os.Lstat(path); err == nil {
		return worktree{}, errors.New("attempt worktree path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return worktree{}, fmt.Errorf("inspect attempt worktree path: %w", err)
	}
	baseBranch, base := repository.BaseBranch, repository.BaseCommit
	if len(recovery) != 0 {
		baseBranch, base, err = resolveRecoveryCommit(ctx, gitExecutable, repository, recovery[0])
	} else if base == "" {
		baseBranch, base, err = resolveBaseCommit(ctx, gitExecutable, repository)
	} else if baseBranch == "" || !commitPattern.MatchString(base) {
		return worktree{}, errors.New("pre-resolved repository base must include a branch and full commit ID")
	}
	if err != nil {
		return worktree{}, err
	}
	branch := "factory/" + sessionID[:12] + "-" + attemptID[:12]
	value := worktree{
		Path: path, Branch: branch, BaseBranch: baseBranch,
		BaseCommit: base, HeadCommit: base,
	}
	return value, nil
}

func resolveRecoveryCommit(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
	recovery worktreeRecovery,
) (string, string, error) {
	if err := validateRegisteredOrigin(ctx, gitExecutable, repository); err != nil {
		return "", "", err
	}
	if recovery.PendingResumeSHA != "" {
		if !commitPattern.MatchString(recovery.PendingResumeSHA) {
			return "", "", errors.New("pending resume SHA is not a full commit ID")
		}
		if recovery.CheckpointPublished {
			remoteSHA, found, err := remotePublishCommitOptional(
				ctx, gitExecutable, repository, recovery.PublishBranch,
			)
			if err != nil {
				return "", "", fmt.Errorf("check authoritative pending resume ref: %w", err)
			}
			if !found {
				return "", "", fmt.Errorf(
					"authoritative pending resume ref refs/heads/%s is missing; restore it to exactly %s before retrying Work %s",
					recovery.PublishBranch, recovery.PendingResumeSHA, recovery.WorkID,
				)
			}
			if remoteSHA != recovery.PendingResumeSHA {
				return "", "", fmt.Errorf(
					"authoritative pending resume ref refs/heads/%s moved to %s; restore it to exactly %s before retrying Work %s",
					recovery.PublishBranch, remoteSHA, recovery.PendingResumeSHA, recovery.WorkID,
				)
			}
		} else if err := ensureCommitAvailable(ctx, gitExecutable, repository, recovery.PendingResumeSHA); err != nil {
			return "", "", fmt.Errorf(
				"authoritative pending resume commit %s is unavailable for Work %s: %w",
				recovery.PendingResumeSHA, recovery.WorkID, err,
			)
		}
		baseBranch, err := recoveryTargetBaseBranch(ctx, gitExecutable, repository)
		if err != nil {
			return "", "", err
		}
		return baseBranch, recovery.PendingResumeSHA, nil
	}

	if recovery.PublishBranch != "" {
		if recovery.PullRequestURL != "" && recovery.PullRequestHeadBranch != "" &&
			recovery.PullRequestHeadBranch != recovery.PublishBranch {
			return "", "", fmt.Errorf(
				"known pull request %s records head branch %s, not immutable publish ref %s; use factory replace %s",
				recovery.PullRequestURL, recovery.PullRequestHeadBranch,
				recovery.PublishBranch, recovery.WorkID,
			)
		}
		remoteSHA, found, err := remotePublishCommitOptional(ctx, gitExecutable, repository, recovery.PublishBranch)
		if err != nil {
			return "", "", fmt.Errorf("check Work publish ref: %w", err)
		}
		if found {
			if recovery.PullRequestURL != "" {
				if recovery.PullRequestHeadSHA == "" {
					return "", "", fmt.Errorf(
						"known pull request %s has no trusted recovery SHA; use factory replace %s",
						recovery.PullRequestURL, recovery.WorkID,
					)
				}
				if remoteSHA != recovery.PullRequestHeadSHA {
					return "", "", fmt.Errorf(
						"known pull request %s publish ref refs/heads/%s is at %s, not trusted recovery SHA %s; restore it only at that SHA or use factory replace %s",
						recovery.PullRequestURL, recovery.PublishBranch, remoteSHA,
						recovery.PullRequestHeadSHA, recovery.WorkID,
					)
				}
			}
			baseBranch, err := recoveryTargetBaseBranch(ctx, gitExecutable, repository)
			if err != nil {
				return "", "", err
			}
			return baseBranch, remoteSHA, nil
		}
		if recovery.PullRequestURL != "" {
			trustedSHA := recovery.PullRequestHeadSHA
			if trustedSHA == "" {
				trustedSHA = "(missing; exact replacement is required)"
			}
			return "", "", fmt.Errorf(
				"known pull request %s has missing publish ref refs/heads/%s; trusted recovery SHA: %s; restore the ref only at that SHA or use factory replace %s",
				recovery.PullRequestURL, recovery.PublishBranch, trustedSHA, recovery.WorkID,
			)
		}
		if recovery.CheckpointPublished {
			trustedSHA := recovery.CheckpointSHA
			if !commitPattern.MatchString(trustedSHA) {
				trustedSHA = "(missing; exact replacement is required)"
			}
			return "", "", fmt.Errorf(
				"previously published checkpoint has missing publish ref refs/heads/%s; trusted checkpoint SHA: %s; restore the ref only at that SHA or use factory replace %s",
				recovery.PublishBranch, trustedSHA, recovery.WorkID,
			)
		}
	}
	return resolveBaseCommit(ctx, gitExecutable, repository)
}

func recoveryTargetBaseBranch(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
) (string, error) {
	if repository.BaseBranch != "" {
		if err := validateBaseBranch(ctx, gitExecutable, repository, repository.BaseBranch); err != nil {
			return "", err
		}
		if _, err := remoteBranchCommit(ctx, gitExecutable, repository, repository.BaseBranch); err != nil {
			return "", err
		}
		return repository.BaseBranch, nil
	}
	branch, _, err := discoverRemoteDefaultBranch(ctx, gitExecutable, repository)
	if err != nil {
		return "", err
	}
	return branch, nil
}

func ensureCommitAvailable(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
	commit string,
) error {
	stdout, _, err := runGitCommand(
		ctx, gitExecutable, repository.Path, 64<<10,
		"rev-parse", "--verify", commit+"^{commit}",
	)
	if err == nil && strings.TrimSpace(string(stdout)) == commit {
		return nil
	}
	stdout, stderr, err := runGitCommand(
		ctx, gitExecutable, repository.Path, 256<<10,
		"fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "origin", commit,
	)
	if err != nil {
		return commandFailure("fetch pending resume commit", stdout, stderr, err)
	}
	stdout, stderr, err = runGitCommand(
		ctx, gitExecutable, repository.Path, 64<<10,
		"rev-parse", "--verify", commit+"^{commit}",
	)
	if err != nil || strings.TrimSpace(string(stdout)) != commit {
		if err == nil {
			err = errors.New("fetched object did not resolve to the pending resume commit")
		}
		return commandFailure("verify pending resume commit", stdout, stderr, err)
	}
	return nil
}

func resolveBaseCommit(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
) (string, string, error) {
	if err := validateRegisteredOrigin(ctx, gitExecutable, repository); err != nil {
		return "", "", err
	}
	branch := repository.BaseBranch
	var base string
	if branch == "" {
		var err error
		branch, base, err = discoverRemoteDefaultBranch(ctx, gitExecutable, repository)
		if err != nil {
			return "", "", err
		}
	}
	if err := validateBaseBranch(ctx, gitExecutable, repository, branch); err != nil {
		return "", "", err
	}
	if base == "" {
		var err error
		base, err = remoteBranchCommit(ctx, gitExecutable, repository, branch)
		if err != nil {
			return "", "", err
		}
	}
	stdout, _, localErr := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"rev-parse", "--verify", base+"^{commit}")
	if localErr != nil || strings.TrimSpace(string(stdout)) != base {
		stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 256<<10,
			"fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", "origin", "refs/heads/"+branch)
		if err != nil {
			return "", "", commandFailure("fetch base branch origin/"+branch, stdout, stderr, err)
		}
		stdout, stderr, err = runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
			"rev-parse", "--verify", base+"^{commit}")
		if err != nil || strings.TrimSpace(string(stdout)) != base {
			if err == nil {
				err = errors.New("fetched branch did not contain the advertised commit")
			}
			return "", "", commandFailure("resolve fetched base branch origin/"+branch, stdout, stderr, err)
		}
	}
	if !commitPattern.MatchString(base) {
		return "", "", errors.New("base branch did not resolve to a full commit ID")
	}
	if err := validateRegisteredOrigin(ctx, gitExecutable, repository); err != nil {
		return "", "", err
	}
	return branch, base, nil
}

func validateRegisteredOrigin(ctx context.Context, gitExecutable string, repository Repository) error {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"remote", "get-url", "origin")
	if err != nil {
		return commandFailure("revalidate repository origin", stdout, stderr, err)
	}
	identity, err := normalizeRemoteIdentity(strings.TrimSpace(string(stdout)), repository.Path)
	if err != nil {
		return fmt.Errorf("revalidate repository origin: %w", err)
	}
	if !sameRemoteIdentity(identity, repository.RemoteIdentity) {
		return errors.New("repository origin changed since worker registration")
	}
	return nil
}

func validateBaseBranch(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
	branch string,
) error {
	if branch == "HEAD" || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "origin/") {
		return errors.New("base_branch must be a short branch name such as main or release/2026.07")
	}
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"check-ref-format", "refs/heads/"+branch)
	if err != nil {
		return commandFailure("validate base_branch", stdout, stderr, err)
	}
	return nil
}

func discoverRemoteDefaultBranch(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
) (string, string, error) {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", "", commandFailure("discover origin default branch", stdout, stderr, err)
	}
	var branch, commit string
	for _, line := range strings.Split(string(stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			if value, found := strings.CutPrefix(fields[1], "refs/heads/"); found {
				branch = value
			}
		} else if len(fields) == 2 && fields[1] == "HEAD" && commitPattern.MatchString(fields[0]) {
			commit = fields[0]
		}
	}
	if branch == "" || commit == "" {
		return "", "", errors.New("origin did not advertise a default branch; set base_branch for this repository")
	}
	return branch, commit, nil
}

func remoteBranchCommit(
	ctx context.Context,
	gitExecutable string,
	repository Repository,
	branch string,
) (string, error) {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 64<<10,
		"ls-remote", "--refs", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", commandFailure("resolve base branch origin/"+branch, stdout, stderr, err)
	}
	fields := strings.Fields(string(stdout))
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !commitPattern.MatchString(fields[0]) {
		return "", errors.New("base branch origin/" + branch + " does not exist")
	}
	return fields[0], nil
}

func addPreparedWorktree(ctx context.Context, gitExecutable string, repository Repository, value worktree) error {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository.Path, 256<<10,
		"worktree", "add", "-b", value.Branch, value.Path, value.BaseCommit)
	if err != nil {
		return commandFailure("create managed worktree", stdout, stderr, err)
	}
	return nil
}

type gitWorktreeEntry struct {
	Path   string
	Head   string
	Branch string
}

func listGitWorktrees(ctx context.Context, gitExecutable, repository string) ([]gitWorktreeEntry, error) {
	stdout, stderr, err := runGitCommand(ctx, gitExecutable, repository, 1<<20, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, commandFailure("list Git worktrees", stdout, stderr, err)
	}
	var entries []gitWorktreeEntry
	var current gitWorktreeEntry
	for _, token := range strings.Split(string(stdout), "\x00") {
		if token == "" {
			if current.Path != "" {
				entries = append(entries, current)
				current = gitWorktreeEntry{}
			}
			continue
		}
		key, value, _ := strings.Cut(token, " ")
		switch key {
		case "worktree":
			if current.Path != "" {
				entries = append(entries, current)
				current = gitWorktreeEntry{}
			}
			current.Path = value
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		}
	}
	if current.Path != "" {
		entries = append(entries, current)
	}
	return entries, nil
}

func runGitCommand(
	ctx context.Context,
	gitExecutable string,
	directory string,
	outputLimit int,
	arguments ...string,
) ([]byte, []byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	return runCommand(commandContext, gitExecutable, directory, outputLimit, arguments...)
}
