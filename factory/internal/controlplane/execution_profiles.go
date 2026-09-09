package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type normalizedExecutionProfile struct {
	name, nameKey, kind, runtime, provider, model, resourceClass string
	fakeOutcome, fakeResult, fakeError, healthReason             string
	sandbox                                                      string
	timeoutSeconds, maxConcurrent                                int
	enabled, healthy                                             bool
}

// normalizeSandbox validates the container posture a docker profile declares
// and returns it as the JSON that is stored and later frozen onto each Run.
//
// allowlist is rejected rather than accepted and quietly treated as open. The
// design names three postures; docker supplies two of them directly, and a
// real allowlist needs an egress proxy that this phase does not build. A run
// that believed it was restricted when it was not would be worse than one that
// could not start.
//
// broker is accepted. It is bridge networking with a route to the credential
// broker on the Worker's host, so it constrains what an agent holds, not where
// it can reach. The Worker refuses to start a broker container when its own
// environment lacks the broker credential, for the same reason: a run that
// believed it had a credential route when it did not would fail confusingly
// partway through rather than at the boundary.
func normalizeSandbox(input *protocol.Sandbox) (string, error) {
	if input == nil {
		return "", invalid("invalid_sandbox", "a docker profile requires a sandbox")
	}
	sandbox := protocol.Sandbox{
		Image:   strings.TrimSpace(input.Image),
		Network: strings.TrimSpace(input.Network),
		CPU:     strings.TrimSpace(input.CPU),
		Memory:  strings.TrimSpace(input.Memory),
	}
	if sandbox.Image == "" || len(sandbox.Image) > 500 {
		return "", invalid("invalid_sandbox_image", "sandbox image is required and limited to 500 bytes")
	}
	if sandbox.Network == "" {
		sandbox.Network = protocol.NetworkNone
	}
	if !protocol.SupportedNetworkPosture(sandbox.Network) {
		return "", invalid("invalid_sandbox_network", "sandbox network must be none, allowlist, open, or broker")
	}
	if !protocol.ImplementedNetworkPosture(sandbox.Network) {
		return "", invalid(
			"sandbox_network_unsupported",
			"the allowlist posture needs an egress proxy that this build does not run; use none, open, or broker",
		)
	}
	if len(sandbox.CPU) > 20 || len(sandbox.Memory) > 20 {
		return "", invalid("invalid_sandbox_limits", "sandbox cpu and memory are limited to 20 bytes each")
	}
	encoded, err := json.Marshal(sandbox)
	if err != nil {
		return "", unavailable(err)
	}
	return string(encoded), nil
}

func normalizeExecutionProfile(input protocol.SaveExecutionProfileRequest) (normalizedExecutionProfile, error) {
	value := normalizedExecutionProfile{
		name: strings.TrimSpace(input.Name), nameKey: normalizeTitleKey(input.Name),
		kind: strings.TrimSpace(input.Kind), runtime: strings.TrimSpace(input.Runtime),
		provider: strings.TrimSpace(input.Provider), model: strings.TrimSpace(input.Model),
		resourceClass: strings.TrimSpace(input.ResourceClass), timeoutSeconds: input.TimeoutSeconds,
		maxConcurrent: input.MaxConcurrent, enabled: input.Enabled, healthy: input.Healthy,
		healthReason: strings.TrimSpace(input.HealthReason), fakeOutcome: strings.TrimSpace(input.FakeOutcome),
		fakeResult: input.FakeResult, fakeError: input.FakeError,
	}
	if value.name == "" || utf8.RuneCountInString(value.name) > 200 {
		return value, invalid("invalid_execution_profile_name", "name is required and limited to 200 characters")
	}
	if value.kind == "" {
		value.kind = protocol.BackendFakeCloudRun
	}
	if value.kind != protocol.BackendFakeCloudRun && value.kind != protocol.BackendDocker {
		return value, invalid("invalid_execution_profile_kind", "kind must be fake_cloud_run or docker")
	}
	if value.kind == protocol.BackendDocker {
		sandbox, err := normalizeSandbox(input.Sandbox)
		if err != nil {
			return value, err
		}
		value.sandbox = sandbox
		if input.FakeOutcome != "" || input.FakeResult != "" || input.FakeError != "" {
			return value, invalid("invalid_fake_cloud_outcome", "a docker profile synthesizes no outcome")
		}
		value.fakeOutcome, value.fakeResult, value.fakeError = "", "", ""
	}
	if !protocol.SupportedRuntime(value.runtime) {
		return value, invalid("invalid_execution_profile_runtime", "runtime is not supported")
	}
	if value.provider == "" || len(value.provider) > 200 || value.model == "" || len(value.model) > 200 {
		return value, invalid("invalid_execution_profile_model", "provider and model are required and limited to 200 bytes")
	}
	if value.timeoutSeconds == 0 {
		value.timeoutSeconds = int(defaultTaskTimeout.Seconds())
	}
	if value.timeoutSeconds < 1 || value.timeoutSeconds > int(protocol.MaxTimeout.Seconds()) {
		return value, invalid("invalid_execution_profile_timeout", "timeout_seconds must be between 1 and 28800")
	}
	if value.resourceClass == "" {
		value.resourceClass = "standard"
	}
	if len(value.resourceClass) > 200 {
		return value, invalid("invalid_execution_profile_resource_class", "resource_class is limited to 200 bytes")
	}
	if value.maxConcurrent == 0 {
		value.maxConcurrent = 10
	}
	if value.maxConcurrent < 1 || value.maxConcurrent > 100 {
		return value, invalid("invalid_execution_profile_capacity", "max_concurrent must be between 1 and 100")
	}
	if value.kind == protocol.BackendFakeCloudRun {
		if value.fakeOutcome == "" {
			value.fakeOutcome = "succeeded"
		}
		if value.fakeOutcome != "succeeded" && value.fakeOutcome != "failed" && value.fakeOutcome != "running" {
			return value, invalid("invalid_fake_cloud_outcome", "fake_outcome must be succeeded, failed, or running")
		}
		if len([]byte(value.fakeResult)) > protocol.MaxResultBytes || len([]byte(value.fakeError)) > protocol.MaxErrorBytes {
			return value, invalid("invalid_fake_cloud_result", "fake result or error exceeds its storage limit")
		}
		if value.fakeOutcome == "succeeded" && value.fakeResult == "" {
			value.fakeResult = "Fake Cloud Run Attempt completed."
		}
		if value.fakeOutcome == "failed" && value.fakeError == "" {
			value.fakeError = "Fake Cloud Run Attempt failed."
		}
	}
	if !value.healthy && value.healthReason == "" {
		value.healthReason = "Profile health validation has not passed."
	}
	if value.healthy {
		value.healthReason = ""
	}
	return value, nil
}

func syntheticWorkerID(profileID string) string { return "cloud-run-" + profileID }

func (s *Store) CreateExecutionProfile(ctx context.Context, input protocol.SaveExecutionProfileRequest) (protocol.ExecutionProfile, error) {
	value, err := normalizeExecutionProfile(input)
	if err != nil {
		return protocol.ExecutionProfile{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	defer tx.Rollback()
	id, err := newID()
	if err != nil {
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO execution_profiles(id, name, name_key, current_version, enabled, healthy, health_reason, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
	`, id, value.name, value.nameKey, value.enabled, value.healthy, value.healthReason, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.ExecutionProfile{}, conflict("execution_profile_name_conflict", "an execution profile with this name already exists")
		}
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	if err := insertExecutionProfileVersion(ctx, tx, id, 1, value, now); err != nil {
		return protocol.ExecutionProfile{}, err
	}
	// Only a fake gets a synthetic Worker. A docker profile is dispatched to a
	// real Worker against a real worktree, so inventing one would advertise
	// capacity that cannot execute anything.
	if value.kind == protocol.BackendFakeCloudRun {
		if err := upsertSyntheticWorker(ctx, tx, id, 1, value, now); err != nil {
			return protocol.ExecutionProfile{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	return s.ExecutionProfile(ctx, id)
}

func (s *Store) UpdateExecutionProfile(ctx context.Context, id string, input protocol.SaveExecutionProfileRequest) (protocol.ExecutionProfile, error) {
	if id == protocol.PersistentAutoProfileID {
		return protocol.ExecutionProfile{}, conflict("execution_profile_read_only", "persistent-auto is built in and cannot be edited")
	}
	if input.ExpectedVersion < 1 {
		return protocol.ExecutionProfile{}, invalid("execution_profile_version_required", "expected_version is required")
	}
	value, err := normalizeExecutionProfile(input)
	if err != nil {
		return protocol.ExecutionProfile{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	nextVersion := input.ExpectedVersion + 1
	result, err := tx.ExecContext(ctx, `
		UPDATE execution_profiles SET name = ?, name_key = ?, current_version = ?, enabled = ?, healthy = ?,
		       health_reason = ?, updated_at = ?
		WHERE id = ? AND current_version = ?
	`, value.name, value.nameKey, nextVersion, value.enabled, value.healthy, value.healthReason, now, id, input.ExpectedVersion)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.ExecutionProfile{}, conflict("execution_profile_name_conflict", "an execution profile with this name already exists")
		}
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_profiles WHERE id = ?`, id).Scan(&exists)
		if exists == 0 {
			return protocol.ExecutionProfile{}, ErrNotFound
		}
		return protocol.ExecutionProfile{}, conflict("execution_profile_version_conflict", "the execution profile changed; refresh and try again")
	}
	if err := insertExecutionProfileVersion(ctx, tx, id, nextVersion, value, now); err != nil {
		return protocol.ExecutionProfile{}, err
	}
	if value.kind == protocol.BackendFakeCloudRun {
		if err := upsertSyntheticWorker(ctx, tx, id, nextVersion, value, now); err != nil {
			return protocol.ExecutionProfile{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.ExecutionProfile{}, unavailable(err)
	}
	return s.ExecutionProfile(ctx, id)
}

func insertExecutionProfileVersion(ctx context.Context, tx *sql.Tx, id string, version int, value normalizedExecutionProfile, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO execution_profile_versions(
			profile_id, version, kind, runtime, provider, model, timeout_seconds, resource_class,
			max_concurrent, commit_resolution_policy, fake_outcome, fake_result, fake_error, sandbox, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, version, value.kind, value.runtime, value.provider, value.model, value.timeoutSeconds,
		value.resourceClass, value.maxConcurrent, protocol.CommitFrozen, value.fakeOutcome,
		value.fakeResult, value.fakeError, value.sandbox, now)
	if err != nil {
		return unavailable(err)
	}
	return nil
}

func upsertSyntheticWorker(ctx context.Context, tx *sql.Tx, profileID string, version int, value normalizedExecutionProfile, now int64) error {
	capabilities, _ := json.Marshal([]protocol.Capability{{
		Kind: protocol.CapabilityKindRuntime, Name: value.runtime, Status: protocol.CapabilityReady,
		Version: value.provider + "/" + value.model,
	}})
	health := "unhealthy"
	if value.enabled && value.healthy {
		health = "healthy"
	}
	workerID := syntheticWorkerID(profileID)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO workers(
			id, name, labels_json, worker_version, claim_protocol_version, runtime, runtime_version,
			capacity, active_count, health, capabilities_json, source_access_json,
			accepts_managed_repositories, managed_repository_ids_json, retained_worktrees_json,
			registered_at, last_heartbeat, synthetic
		) VALUES (?, ?, '{}', 'factory-synthetic', 0, ?, ?, ?, 0, ?, ?, '[]', 0, '[]', '[]', ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, runtime = excluded.runtime, runtime_version = excluded.runtime_version,
			capacity = excluded.capacity, health = excluded.health, capabilities_json = excluded.capabilities_json,
			last_heartbeat = excluded.last_heartbeat
		WHERE workers.synthetic = 1
	`, workerID, value.name, value.runtime, "profile-v"+strconv.Itoa(version), value.maxConcurrent,
		health, capabilities, now, now)
	if err != nil {
		return unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return conflict("synthetic_worker_id_conflict", "the synthetic Worker ID is already owned by a persistent Worker")
	}
	return nil
}

func persistentAutoProfile() protocol.ExecutionProfile {
	return protocol.ExecutionProfile{
		ID: protocol.PersistentAutoProfileID, Name: "Persistent auto", Kind: protocol.BackendPersistent,
		Version: 1, Provider: "worker", Model: "worker-default", ResourceClass: "worker",
		MaxConcurrent: protocol.MaxWorkerCapacity, Enabled: true, Healthy: true,
	}
}

func (s *Store) ExecutionProfiles(ctx context.Context) (protocol.ExecutionProfilePage, error) {
	page := protocol.ExecutionProfilePage{Profiles: []protocol.ExecutionProfile{persistentAutoProfile()}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.name, v.kind, p.current_version, v.runtime, v.provider, v.model,
		       v.timeout_seconds, v.resource_class, v.max_concurrent, p.enabled, p.healthy,
		       p.health_reason, v.fake_outcome, v.fake_result, v.fake_error, v.sandbox,
		       p.created_at, p.updated_at
		FROM execution_profiles p
		JOIN execution_profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		ORDER BY p.name_key, p.id
	`)
	if err != nil {
		return page, unavailable(err)
	}
	defer rows.Close()
	for rows.Next() {
		profile, err := scanExecutionProfile(rows)
		if err != nil {
			return page, unavailable(err)
		}
		page.Profiles = append(page.Profiles, profile)
	}
	return page, rows.Err()
}

func (s *Store) ExecutionProfile(ctx context.Context, id string) (protocol.ExecutionProfile, error) {
	if id == "" || id == protocol.PersistentAutoProfileID {
		return persistentAutoProfile(), nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.name, v.kind, p.current_version, v.runtime, v.provider, v.model,
		       v.timeout_seconds, v.resource_class, v.max_concurrent, p.enabled, p.healthy,
		       p.health_reason, v.fake_outcome, v.fake_result, v.fake_error, v.sandbox,
		       p.created_at, p.updated_at
		FROM execution_profiles p
		JOIN execution_profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		WHERE p.id = ?
	`, id)
	profile, err := scanExecutionProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, ErrNotFound
	}
	if err != nil {
		return profile, unavailable(err)
	}
	return profile, nil
}

func scanExecutionProfile(row scanner) (protocol.ExecutionProfile, error) {
	var profile protocol.ExecutionProfile
	var enabled, healthy int
	var created, updated int64
	var sandbox string
	err := row.Scan(&profile.ID, &profile.Name, &profile.Kind, &profile.Version, &profile.Runtime,
		&profile.Provider, &profile.Model, &profile.TimeoutSeconds, &profile.ResourceClass,
		&profile.MaxConcurrent, &enabled, &healthy, &profile.HealthReason, &profile.FakeOutcome,
		&profile.FakeResult, &profile.FakeError, &sandbox, &created, &updated)
	profile.Enabled, profile.Healthy = enabled != 0, healthy != 0
	if profile.Kind == protocol.BackendFakeCloudRun {
		profile.SyntheticWorkerID = syntheticWorkerID(profile.ID)
	}
	if sandbox != "" {
		var decoded protocol.Sandbox
		if decodeErr := json.Unmarshal([]byte(sandbox), &decoded); decodeErr == nil {
			profile.Sandbox = &decoded
		}
	}
	profile.CreatedAt, profile.UpdatedAt = fromMillis(created), fromMillis(updated)
	return profile, err
}
