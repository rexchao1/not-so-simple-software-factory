package worker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultPollInterval         = 2 * time.Second
	defaultHealthInterval       = 30 * time.Second
	defaultRegistrationInterval = 10 * time.Second
	defaultLeaseRenewInterval   = 10 * time.Second
	defaultLeaseRetryInterval   = 2 * time.Second
	defaultShutdownTimeout      = 30 * time.Second
	defaultWorkerVersion        = "dev"
)

type Options struct {
	GitExecutable        string
	GitHubExecutable     string
	RuntimeExecutable    string
	RuntimeExecutables   map[string]string
	SupervisorCommand    []string
	HTTPClient           *http.Client
	Random               io.Reader
	WorkerVersion        string
	PollInterval         time.Duration
	HealthInterval       time.Duration
	RegistrationInterval time.Duration
	LeaseRenewInterval   time.Duration
	LeaseRetryInterval   time.Duration
	TransportBackoffMin  time.Duration
	TransportBackoffMax  time.Duration
	ShutdownTimeout      time.Duration
}

type Manager struct {
	config            Config
	options           Options
	logger            *slog.Logger
	id                string
	dataDirectory     string
	lock              *dataLock
	repositories      []Repository
	repositoriesByKey map[string]Repository
	client            *client
	manifests         *manifestStore
	slots             chan struct{}

	stateMutex                    sync.Mutex
	health                        health
	active                        map[string]*attemptHandle
	seen                          map[string]bool
	retained                      map[string]protocol.RetainedWorktree
	retainedCounts                map[string]int
	managedRepositoryIDs          map[string]bool
	managedRepositoryReservations map[string]bool
	disposed                      map[string]bool
	pending                       map[string]context.CancelFunc
	claiming                      bool
	healthCheckPending            bool
	healthCheckDone               chan struct{}
	fatalHealth                   error
	registered                    bool
	registrationGeneration        uint64
	advertisedGeneration          uint64
	hasAdvertisedRegistration     bool
	closed                        bool

	randomMutex     sync.Mutex
	repositoryLocks repositoryLockSet
	// registrationMutex serializes registrations and protects capacityHandoffs.
	// A positive handoff count pauses periodic registration without serializing
	// repository-local cleanup for unrelated attempts.
	registrationMutex sync.Mutex
	capacityHandoffs  int
	waitGroup         sync.WaitGroup
}

func New(config Config, options Options, logger *slog.Logger) (*Manager, error) {
	if err := ensureSupportedPlatform(); err != nil {
		return nil, err
	}
	config.Runtimes = configuredRuntimes(config)
	if config.Runtime == "" {
		config.Runtime = config.Runtimes[0]
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	options = options.withDefaults(config.Runtime, config.Runtimes)
	if len(options.SupervisorCommand) == 0 {
		return nil, errors.New("resolve factory-worker executable for attempt supervision")
	}
	dataDirectory, err := resolveDataDirectory(config.DataDirectory)
	if err != nil {
		return nil, err
	}
	lock, err := lockDataDirectory(dataDirectory)
	if err != nil {
		return nil, err
	}
	cleanupLock := true
	defer func() {
		if cleanupLock {
			_ = lock.Close()
		}
	}()
	id, err := loadOrCreateWorkerID(dataDirectory, options.Random)
	if err != nil {
		return nil, err
	}
	repositories, err := resolveRepositories(config, options.GitExecutable)
	if err != nil {
		return nil, err
	}
	managedRepositoryIDs, err := managedRepositoryCacheIDs(dataDirectory)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	httpClient, err := workerHTTPClient(config, options.HTTPClient)
	if err != nil {
		return nil, err
	}
	workerClient := newClient(config.Server, httpClient)
	credentialPath := filepath.Join(dataDirectory, "worker-credential")
	if err := adoptLegacyWorkerCredentialFiles(dataDirectory, config.Server); err != nil {
		return nil, err
	}
	workerClient.credential, err = loadCredentialFile(credentialPath, config.Server)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		byKey[repository.Key] = repository
	}
	cleanupLock = false
	return &Manager{
		config:                        config,
		options:                       options,
		logger:                        logger,
		id:                            id,
		dataDirectory:                 dataDirectory,
		lock:                          lock,
		repositories:                  repositories,
		repositoriesByKey:             byKey,
		client:                        workerClient,
		manifests:                     newManifestStore(dataDirectory, id),
		slots:                         make(chan struct{}, config.MaxConcurrent),
		health:                        health{State: "unhealthy"},
		active:                        make(map[string]*attemptHandle),
		seen:                          make(map[string]bool),
		retained:                      make(map[string]protocol.RetainedWorktree),
		retainedCounts:                make(map[string]int),
		managedRepositoryIDs:          managedRepositoryIDs,
		managedRepositoryReservations: make(map[string]bool),
		disposed:                      make(map[string]bool),
		pending:                       make(map[string]context.CancelFunc),
	}, nil
}

func workerHTTPClient(config Config, provided *http.Client) (*http.Client, error) {
	if provided != nil || config.CACertificate == "" {
		return provided, nil
	}
	pem, err := os.ReadFile(config.CACertificate)
	if err != nil {
		return nil, fmt.Errorf("read ca_certificate: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("ca_certificate contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: transport}, nil
}

func (options Options) withDefaults(primaryRuntime string, runtimes []string) Options {
	if options.GitExecutable == "" {
		options.GitExecutable = "git"
	}
	if options.GitHubExecutable == "" {
		options.GitHubExecutable = "gh"
	}
	if options.RuntimeExecutables == nil {
		options.RuntimeExecutables = make(map[string]string, len(runtimes))
	} else {
		copy := make(map[string]string, len(options.RuntimeExecutables)+len(runtimes))
		for runtime, executable := range options.RuntimeExecutables {
			copy[runtime] = executable
		}
		options.RuntimeExecutables = copy
	}
	if options.RuntimeExecutable != "" {
		options.RuntimeExecutables[primaryRuntime] = options.RuntimeExecutable
	}
	for _, runtime := range runtimes {
		if options.RuntimeExecutables[runtime] == "" {
			options.RuntimeExecutables[runtime] = defaultRuntimeExecutable(runtime)
		}
	}
	if len(options.SupervisorCommand) == 0 {
		executable, err := os.Executable()
		if err == nil {
			options.SupervisorCommand = supervisorCommandLine(executable)
		}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.WorkerVersion == "" {
		options.WorkerVersion = defaultWorkerVersion
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.HealthInterval <= 0 {
		options.HealthInterval = defaultHealthInterval
	}
	if options.RegistrationInterval <= 0 {
		options.RegistrationInterval = defaultRegistrationInterval
	}
	if options.LeaseRenewInterval <= 0 {
		options.LeaseRenewInterval = defaultLeaseRenewInterval
	}
	if options.LeaseRetryInterval <= 0 {
		options.LeaseRetryInterval = defaultLeaseRetryInterval
	}
	if options.TransportBackoffMin <= 0 {
		options.TransportBackoffMin = time.Second
	}
	if options.TransportBackoffMax <= 0 {
		options.TransportBackoffMax = 30 * time.Second
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	return options
}

func defaultRuntimeExecutable(runtime string) string {
	switch runtime {
	case protocol.RuntimePi:
		return "pi"
	case protocol.RuntimeClaudeCode:
		return "claude"
	default:
		return "codex"
	}
}

func (manager *Manager) runtimeExecutable(runtime string) string {
	if executable := manager.options.RuntimeExecutables[runtime]; executable != "" {
		return executable
	}
	if runtime == manager.config.Runtime {
		if manager.options.RuntimeExecutable != "" {
			return manager.options.RuntimeExecutable
		}
		return defaultRuntimeExecutable(runtime)
	}
	return ""
}

func (manager *Manager) ID() string { return manager.id }

func (manager *Manager) Run(ctx context.Context) error {
	defer manager.Close()
	enrollmentBackoff := manager.options.TransportBackoffMin
	for {
		err := manager.client.enroll(ctx, manager.id, manager.config.EnrollmentToken,
			filepath.Join(manager.dataDirectory, "worker-credential"))
		if err == nil {
			break
		}
		var retryable retryableEnrollmentError
		if !errors.As(err, &retryable) {
			return err
		}
		manager.logger.Warn("worker_enrollment_retry", "error_class", "transient", "error", err)
		timer := time.NewTimer(enrollmentBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		enrollmentBackoff *= 2
		if enrollmentBackoff > manager.options.TransportBackoffMax {
			enrollmentBackoff = manager.options.TransportBackoffMax
		}
	}
	for {
		err := manager.reconcile(ctx)
		if err == nil {
			break
		}
		if !reconciliationNeedsRetry(err) {
			manager.markUnhealthy("startup_reconciliation", err)
			break
		}
		manager.logger.Warn("startup_reconciliation_retry", "error_class", "transient", "error", err)
		timer := time.NewTimer(manager.options.LeaseRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	manager.setHealth(checkHealth(ctx, manager.options.GitExecutable,
		manager.options.GitHubExecutable, manager.config.Runtime,
		manager.config.Runtimes, manager.options.RuntimeExecutables))
	manager.register(ctx)

	healthTicker := time.NewTicker(manager.options.HealthInterval)
	defer healthTicker.Stop()
	registrationTicker := time.NewTicker(manager.options.RegistrationInterval)
	defer registrationTicker.Stop()
	claimTimer := time.NewTimer(0)
	defer claimTimer.Stop()
	healthResults := make(chan health, 1)
	startHealthCheck := func() {
		if !manager.beginHealthCheck() {
			return
		}
		manager.waitGroup.Add(1)
		go func() {
			defer manager.waitGroup.Done()
			result := checkHealth(ctx, manager.options.GitExecutable,
				manager.options.GitHubExecutable, manager.config.Runtime,
				manager.config.Runtimes, manager.options.RuntimeExecutables)
			select {
			case healthResults <- result:
			case <-ctx.Done():
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			manager.stopAll("cancelled")
			return manager.waitForShutdown()
		case <-healthTicker.C:
			startHealthCheck()
		case result := <-healthResults:
			if manager.setHealth(result) {
				manager.register(ctx)
			}
		case <-registrationTicker.C:
			manager.register(ctx)
		case <-claimTimer.C:
			if manager.isHealthy() {
				manager.reserveAndClaim(ctx)
			}
			claimTimer.Reset(manager.jitteredPollInterval())
		}
	}
}

func (manager *Manager) Close() error {
	manager.stateMutex.Lock()
	if manager.closed {
		manager.stateMutex.Unlock()
		return nil
	}
	manager.closed = true
	manager.stateMutex.Unlock()
	return manager.lock.Close()
}

func (manager *Manager) stopAll(reason string) {
	manager.stateMutex.Lock()
	handles := make([]*attemptHandle, 0, len(manager.active))
	for _, handle := range manager.active {
		handles = append(handles, handle)
	}
	manager.stateMutex.Unlock()
	for _, handle := range handles {
		handle.stop(reason)
	}
}

func (manager *Manager) waitForShutdown() error {
	done := make(chan struct{})
	go func() {
		manager.waitGroup.Wait()
		close(done)
	}()
	timer := time.NewTimer(manager.options.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		manager.stateMutex.Lock()
		for _, handle := range manager.active {
			handle.mutex.Lock()
			if handle.supervisor != nil {
				_ = handle.supervisor.closeControl()
			}
			handle.mutex.Unlock()
		}
		manager.stateMutex.Unlock()
		return errors.New("worker shutdown exceeded thirty seconds")
	}
}

func (manager *Manager) randomUUID() (string, error) {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	return newUUID(manager.options.Random)
}

func (manager *Manager) randomSecret() (string, error) {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	body := make([]byte, 32)
	if _, err := io.ReadFull(manager.options.Random, body); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func (manager *Manager) jitteredPollInterval() time.Duration {
	manager.randomMutex.Lock()
	defer manager.randomMutex.Unlock()
	var value [1]byte
	if _, err := io.ReadFull(manager.options.Random, value[:]); err != nil {
		return manager.options.PollInterval
	}
	// Empty polls use up to 20 percent positive jitter.
	return manager.options.PollInterval + time.Duration(int64(manager.options.PollInterval)*int64(value[0])/1275)
}

func DefaultConfigPath() (string, error) {
	if value := workerConfigEnvironment(); value != "" {
		return value, nil
	}
	root := dataHomeEnvironment()
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".factory")
	}
	return filepath.Join(root, "worker.toml"), nil
}

func ValidateNoLegacyDefaultConfig() error {
	if workerConfigEnvironment() != "" || dataHomeEnvironment() != "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	currentRoot := filepath.Join(home, ".factory")
	if _, found, err := findPreviewWorkerState(currentRoot); err != nil {
		return err
	} else if found {
		return nil
	}
	legacyRoot := filepath.Join(home, ".factory-v2")
	legacyState, found, err := findPreviewWorkerState(legacyRoot)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf(
			"found preview worker state at %s; refusing to create a new worker identity by default; set FACTORY_DATA_HOME=%s to keep using it, or archive the old state after resolving its work",
			legacyState,
			legacyRoot,
		)
	}
	return nil
}

func findPreviewWorkerState(root string) (string, bool, error) {
	for _, candidate := range []string{
		filepath.Join(root, "worker.toml"),
		filepath.Join(root, "workers"),
	} {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect preview worker state %s: %w", candidate, err)
		}
	}
	return "", false, nil
}

func dataHomeEnvironment() string {
	if value := os.Getenv("FACTORY_DATA_HOME"); value != "" {
		return value
	}
	return os.Getenv("FACTORY_V2_DATA_HOME")
}

func workerConfigEnvironment() string {
	if value := os.Getenv("FACTORY_WORKER_CONFIG"); value != "" {
		return value
	}
	return os.Getenv("FACTORY_V2_WORKER_CONFIG")
}
