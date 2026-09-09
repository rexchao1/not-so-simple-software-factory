package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

// admissionRequestKeyPrefix namespaces the runs created by AdmitWork inside
// the shared runs.request_key column, so an AdmitWork call and an unrelated
// RunTask call can never collide on the same key and make AdmitWork replay
// someone else's run.
const admissionRequestKeyPrefix = "work:"

// AdmitWork accepts one spec and produces one Run. It is the single admission
// path described in design.md section 6. Orchestrator submissions may carry
// pre_approved because a human was present while the spec was written. Every
// other source lands in draft and waits for an explicit approval.
func (s *Store) AdmitWork(
	ctx context.Context, input protocol.AdmitWorkRequest,
) (protocol.AdmitWorkResponse, bool, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Repository = strings.TrimSpace(input.Repository)
	input.Name = strings.TrimSpace(input.Name)
	input.Runtime = strings.TrimSpace(input.Runtime)
	if input.Assurance == "" {
		input.Assurance = protocol.AssuranceReviewed
	}

	if input.RequestKey == "" || len(input.RequestKey) > 200 {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(input.RequestKey, "schedule:") ||
		strings.HasPrefix(input.RequestKey, admissionRequestKeyPrefix) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}
	if !protocol.SupportedAssuranceMode(input.Assurance) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_assurance", "assurance must be reviewed or fast")
	}
	if input.Assurance == protocol.AssuranceFast && input.Source != protocol.WorkSourceOrchestrator {
		return protocol.AdmitWorkResponse{}, false,
			invalid("fast_assurance_not_permitted", "only orchestrator submissions may select fast assurance")
	}
	if !protocol.SupportedWorkSource(input.Source) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_source", "source must be orchestrator, cockpit, or github")
	}
	// INV-1. Only the orchestrator may assert that a human already approved.
	if input.PreApproved && input.Source != protocol.WorkSourceOrchestrator {
		return protocol.AdmitWorkResponse{}, false, invalid(
			"pre_approval_not_permitted",
			"only orchestrator submissions may set pre_approved",
		)
	}
	// An explicit delivery is validated here. An omitted one is resolved to
	// the repository's own default below, once the repository is known.
	if input.Delivery != "" && !protocol.SupportedDeliveryMode(input.Delivery) {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_delivery", "delivery must be pr, pr+automerge, or branch")
	}
	if strings.TrimSpace(input.Spec) == "" {
		return protocol.AdmitWorkResponse{}, false,
			invalid("invalid_spec", "spec is required")
	}
	planning, err := normalizeWorkPlanning(input.Planning, input.PipelineID)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	if input.Brief != nil {
		if input.Source != protocol.WorkSourceOrchestrator {
			return protocol.AdmitWorkResponse{}, false, invalid("brief_not_permitted", "only orchestrator submissions may include a brief")
		}
		for _, field := range []string{input.Brief.Context, input.Brief.Why, input.Brief.Risk, input.Brief.Work} {
			if len([]byte(strings.TrimSpace(field))) > 280 {
				return protocol.AdmitWorkResponse{}, false, invalid("brief_too_large", "each brief field is limited to 280 bytes")
			}
		}
		if strings.TrimSpace(input.Brief.Context)+strings.TrimSpace(input.Brief.Why)+strings.TrimSpace(input.Brief.Risk)+strings.TrimSpace(input.Brief.Work) == "" {
			return protocol.AdmitWorkResponse{}, false, invalid("invalid_brief", "brief must contain at least one field")
		}
	}

	runRequestKey := admissionRequestKeyPrefix + input.RequestKey
	if existing, found, err := s.admittedWork(ctx, runRequestKey); err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	} else if found {
		return existing, false, nil
	}
	// An advisory fast-fail, not the authoritative gate: admitTask re-checks
	// inside the transaction that inserts the Run. Refusing here as well keeps
	// a paused admission from creating the hash-suffixed Task below and
	// leaving it orphaned when admitTask then refuses.
	pause, err := s.FactoryPause(ctx)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	if pause.Paused {
		return protocol.AdmitWorkResponse{}, false, conflict("factory_paused", pauseAdmissionMessage)
	}

	// The repository column on repositories is normalized (lowercased, ".git"
	// stripped) at write time by CreateManagedRepository. GitHub webhooks and
	// most callers send canonical-case identities, so the lookup normalizes
	// the same way before matching, or a case difference alone would return
	// repository_not_found for a repository that plainly is managed.
	identity, err := normalizeManagedGitHubRemote(input.Repository)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	repository, err := s.managedRepositoryByIdentity(ctx, identity)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	if input.Delivery == "" {
		input.Delivery = repository.DefaultDelivery
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = defaultAdmissionTimeoutSeconds
	}

	specification := protocol.SaveTaskRequest{
		Name:             admissionTaskName(input.RequestKey, input.Name),
		SubmittedName:    input.Name,
		Prompt:           input.Spec,
		Runtime:          input.Runtime,
		TimeoutSeconds:   input.TimeoutSeconds,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{repository.ID},
		Schedule:         protocol.TaskSchedule{Enabled: false},
		OutcomeContract:  protocol.OutcomeAgentUpdate,
		PipelineID:       input.PipelineID,
	}
	// A critique reports by exiting, not by calling back. agent_update would
	// wrap the stage in Factory's reporting contract and hold the Work open
	// until the agent called factory update with a pull request URL, and a
	// read-only pass has no pull request to name. process_exit makes the Work
	// result exactly what the critic printed, which is the JSON the light
	// factory reads.
	if input.PipelineID == protocol.CritiquePipelineID {
		specification.OutcomeContract = protocol.OutcomeProcessExit
	}
	// A task_name_conflict here is not a caller error. admissionTaskName is
	// deterministic in the request key, so the conflict IS the signal that
	// this exact request key already created the task. Adopt it and carry
	// on: see adoptAdmittedTask.
	task, err := s.CreateTask(ctx, specification)
	if serviceErrorCode(err, "task_name_conflict") {
		task, err = s.adoptAdmittedTask(ctx, specification)
	}
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}

	// admitTask is called directly, not through RunTask, so source,
	// pre_approved, and delivery are set in the same transaction that
	// inserts the run and its sessions. That also closes the Step 4b race:
	// asDraft is decided before worker selection ever runs, inside that one
	// transaction, so a session is never briefly queued before landing in
	// draft.
	detail, _, err := s.admitTask(ctx, task.ID, runRequestKey, nil, nil, "", admissionProvenance{
		source:      input.Source,
		preApproved: input.PreApproved,
		delivery:    input.Delivery,
		assurance:   input.Assurance,
		asDraft:     !input.PreApproved,
		brief:       input.Brief,
		planning:    planning,
	})
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	return s.admissionResponse(ctx, detail.Run.ID, task.ID, input.Source)
}

// normalizeWorkPlanning validates the optional planning triple.
//
// The triple is all or nothing. A pass that named a checkpoint but no round
// could not be ordered against the other passes on that checkpoint, and one
// that named a round but no checkpoint could not be found at all, so a partial
// triple is refused rather than stored half-empty. Absent is the ordinary
// case and is not an error.
//
// The project is validated exactly as the planning API validates a project id,
// including its case and whitespace folding, so a pass admitted for "Payer"
// joins the checkpoint saved under "payer" rather than silently joining
// nothing.
//
// The Pipeline is checked here because the triple only means anything on a
// critique. Letting an ordinary build carry one would put a build on a
// checkpoint's list of critique rounds, where the page would report it as a
// pass that found nothing.
func normalizeWorkPlanning(
	input *protocol.WorkPlanning, pipelineID string,
) (*protocol.WorkPlanning, error) {
	if input == nil {
		return nil, nil
	}
	project, number, err := validPlanningCheckpointKey(input.Project, input.Number)
	if err != nil {
		return nil, err
	}
	if input.Round < 1 {
		return nil, invalid("invalid_planning_round", "a planning round is 1 or greater")
	}
	if pipelineID != protocol.CritiquePipelineID {
		return nil, invalid(
			"planning_pipeline_required",
			"planning Work must be submitted to the built-in Critique Pipeline",
		)
	}
	return &protocol.WorkPlanning{Project: project, Number: number, Round: input.Round}, nil
}

// admissionTaskName is the deterministic Task name one admission uses.
//
// tasks.name_key is unique, and admission titles repeat constantly ("Update
// dependencies", "Fix flaky test"). A deterministic suffix derived from the
// request key keeps the task creatable on every admission while staying
// stable across a retry of the same key. The base name is truncated, by rune
// rather than by byte so a multibyte title is never cut mid-character, to
// keep the combined name within normalizeTask's 200 rune limit.
//
// The suffix is an internal admission artifact, so input.Name is stored
// unchanged alongside it as tasks.submitted_name. The Drafts approval screen
// is the primary display surface for admitted Work, and it has to be able to
// show the title a human wrote without parsing a value that is deliberately
// opaque.
//
// Being deterministic in the request key is also what makes adoption safe:
// see adoptAdmittedTask.
func admissionTaskName(requestKey, name string) string {
	digest := sha256.Sum256([]byte(requestKey))
	suffix := " (" + hex.EncodeToString(digest[:])[:8] + ")"
	base := []rune(name)
	if limit := maxTaskNameRunes - utf8.RuneCountInString(suffix); len(base) > limit {
		base = base[:limit]
	}
	return string(base) + suffix
}

// adoptAdmittedTask resolves the Task a previous attempt at this exact
// request key already created, so admission heals itself instead of
// permanently rejecting the key.
//
// AdmitWork creates the Task in CreateTask's transaction and the Run in
// admitTask's. The replay check keys on runs.request_key, which only exists
// once the second transaction commits, so between the two a duplicate has
// nothing to replay against and only tasks.name_key stops it. Two failures
// follow, and adoption answers both:
//
//   - Concurrent duplicate. Two clients POST the same request_key, both pass
//     the replay check because neither run has committed, and one loses the
//     insert race. Adopting lets the loser continue into admitTask, which
//     then does hit the replay check and returns the winner's Work. One Work
//     record: AC-3 held properly rather than by accident.
//   - Partial failure. admitTask failed transiently (SQLITE_BUSY) after
//     CreateTask committed, so the run that would trigger the replay never
//     existed and every later retry of the key hit the same 409 forever,
//     with an orphaned hash-suffixed task left in the Tasks list. Adopting
//     the orphan completes the admission and un-poisons the key.
//
// Running both inserts in one transaction would also fix this, but it means
// extracting tx-taking inner functions from CreateTask and admitTask, a
// large refactor of tasks.go, which is already the most expensive file to
// carry across upstream merges. Self-healing is the cheaper equivalent.
//
// The suffix is only 8 hex characters of a sha256, so the name alone is not
// proof of identity. The stored prompt and runtime are compared against this
// submission's, normalized exactly as CreateTask normalized them on the way
// in. A mismatch is a genuine collision rather than this submission's task,
// and is refused with its own code: better a clear error than running the
// wrong spec under a colliding name.
//
// submitted_name is deliberately not part of that comparison. It changes
// nothing about what gets executed, so a difference in it is not evidence of
// a collision, and two ways of differing are both benign: a name_key match
// already means the two titles agree up to normalizeTitleKey, leaving only
// case and spacing; and a Task created before migration 036 has no submitted
// name at all, which would make every adoption of one fail. Refusing an
// otherwise identical submission over a display string would re-poison the
// request key that adoption exists to un-poison.
func (s *Store) adoptAdmittedTask(
	ctx context.Context, specification protocol.SaveTaskRequest,
) (protocol.Task, error) {
	wanted, err := normalizeTask(specification, s.now())
	if err != nil {
		return protocol.Task{}, err
	}
	var id, prompt, runtime string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, prompt, runtime FROM tasks WHERE name_key = ? AND migration_only = 0
	`, wanted.nameKey).Scan(&id, &prompt, &runtime)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing adoptable holds the name. Report the conflict CreateTask
		// already raised rather than inventing a different story.
		return protocol.Task{}, conflict("task_name_conflict", "a Task with this name already exists")
	}
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if prompt != wanted.prompt || runtime != wanted.runtime {
		return protocol.Task{}, conflict(
			"admission_name_collision",
			"a different Task already holds the name generated for this request_key",
		)
	}
	return s.Task(ctx, id)
}

func (s *Store) admissionResponse(
	ctx context.Context, runID, taskID string, source protocol.WorkSource,
) (protocol.AdmitWorkResponse, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, state FROM sessions WHERE run_id = ? ORDER BY target_position, id
	`, runID)
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	defer rows.Close()
	response := protocol.AdmitWorkResponse{RunID: runID, TaskID: taskID, Source: source}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return protocol.AdmitWorkResponse{}, false, unavailable(err)
		}
		response.WorkIDs = append(response.WorkIDs, id)
		response.State = protocol.SessionState(state)
	}
	if err := rows.Err(); err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	return response, true, nil
}

// admittedWork replays a previous admission for the same (already namespaced)
// request key, so two clients submitting the same work create one Run. AC-3.
func (s *Store) admittedWork(
	ctx context.Context, runRequestKey string,
) (protocol.AdmitWorkResponse, bool, error) {
	var runID, taskID, source string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, source FROM runs WHERE request_key = ?
	`, runRequestKey).Scan(&runID, &taskID, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.AdmitWorkResponse{}, false, nil
	}
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, unavailable(err)
	}
	response, _, err := s.admissionResponse(ctx, runID, taskID, protocol.WorkSource(source))
	if err != nil {
		return protocol.AdmitWorkResponse{}, false, err
	}
	return response, true, nil
}

const defaultAdmissionTimeoutSeconds = 3600

// maxTaskNameRunes is the one name length limit. normalizeTask
// (internal/controlplane/tasks.go) enforces it on both the stored and the
// submitted name, and the unique-name suffix is sized against it here, so
// there is no second bound to keep in step with this one.
const maxTaskNameRunes = 200
