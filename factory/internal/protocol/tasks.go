package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	MaxTasks                = 500
	MaxPipelines            = 200
	MaxPipelineStages       = 20
	MaxTaskRepositories     = 100
	MaxTaskPromptBytes      = 64 * 1024
	MaxWorkTargets          = 100
	MaxProgressBytes        = 2 * 1024
	MaxOutcomeBytes         = 8 * 1024
	MaxWaitingReasonBytes   = 2 * 1024
	MaxTerminalMessageBytes = 8 * 1024
	MaxQuestionBytes        = 8 * 1024
	MaxAnswerBytes          = 8 * 1024
	MaxUpdatesPerAttempt    = 200
	MaxProgressPerAttempt   = 199
)

const (
	DefaultPipelineID = "00000000-0000-0000-0000-000000000001"
	FastPipelineID    = "00000000-0000-0000-0000-000000000002"
	// CritiquePipelineID is the built-in critique pass the light factory runs
	// against a checkpoint PRD. Its stage is read only, so the guarantee that
	// a critic changes nothing is the Worker's and not the prompt's.
	CritiquePipelineID = "00000000-0000-0000-0000-000000000003"
)

type PipelineStage struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Command  string `json:"command,omitempty"`
	// Model and Effort are optional. Empty means inherit, and the chain is
	// resolved once at admission. A code stage must leave both empty: it never
	// reaches a model at all, which is INV-7.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// ReadOnly denies the runtime every tool that writes. It is only meaningful
	// on an agent stage, since a code stage runs a declared command and a
	// delivery stage runs Factory's own git operations, and it is only
	// enforceable on claude-code, which is the one runtime with a tool-denial
	// flag. Both rules are refused at validation rather than ignored here.
	ReadOnly bool `json:"read_only,omitempty"`
}

func (s PipelineStage) Execution() StageExecution {
	return StageExecution{Model: s.Model, Effort: s.Effort}
}

type Pipeline struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Generation int             `json:"generation"`
	Stages     []PipelineStage `json:"stages"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type PipelinePage struct {
	Pipelines []Pipeline `json:"pipelines"`
}

type SavePipelineRequest struct {
	Name               string          `json:"name"`
	Stages             []PipelineStage `json:"stages"`
	ExpectedGeneration int             `json:"expected_generation,omitempty"`
}

type PipelineSnapshot struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Generation int             `json:"generation"`
	Stages     []PipelineStage `json:"stages"`
}

type StageRunState string

const (
	StagePending   StageRunState = "pending"
	StageRunning   StageRunState = "running"
	StageSucceeded StageRunState = "succeeded"
	StageFailed    StageRunState = "failed"
	StageCancelled StageRunState = "cancelled"
)

type StageRun struct {
	Position      int                   `json:"position"`
	Name          string                `json:"name"`
	Kind          string                `json:"kind,omitempty"`
	Prompt        string                `json:"prompt,omitempty"`
	Command       string                `json:"command,omitempty"`
	Model         string                `json:"model,omitempty"`
	Effort        string                `json:"effort,omitempty"`
	ReadOnly      bool                  `json:"read_only,omitempty"`
	State         StageRunState         `json:"state"`
	Result        string                `json:"result,omitempty"`
	Error         string                `json:"error,omitempty"`
	ReviewVerdict ReviewVerdict         `json:"review_verdict,omitempty"`
	CostUSD       *float64              `json:"cost_usd,omitempty"`
	Usage         *Usage                `json:"usage,omitempty"`
	Models        map[string]ModelUsage `json:"models,omitempty"`
	StartedAt     *time.Time            `json:"started_at,omitempty"`
	CompletedAt   *time.Time            `json:"completed_at,omitempty"`
}

func (s StageRun) Execution() StageExecution {
	return StageExecution{Model: s.Model, Effort: s.Effort}
}

type StartStageRequest struct {
	LeaseToken      string `json:"lease_token"`
	SupervisorPID   *int64 `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  *int64 `json:"process_group_id,omitempty"`
}

type CompleteStageRequest struct {
	LeaseToken string        `json:"lease_token"`
	State      StageRunState `json:"state"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	// ReviewVerdict is recorded by a reviewing stage. It is refused on
	// position 0, which is the implementing stage: a verdict on the work you
	// wrote yourself is self-approval and INV-8 must not count it.
	ReviewVerdict ReviewVerdict `json:"review_verdict,omitempty"`
	// CostUSD, Usage, and Models are this stage's own share of the Attempt's
	// cost, in the shape CompleteAttemptRequest carries for the sum.
	CostUSD *float64              `json:"cost_usd,omitempty"`
	Usage   *Usage                `json:"usage,omitempty"`
	Models  map[string]ModelUsage `json:"models,omitempty"`
}

type OutcomeContract string

const (
	OutcomeProcessExit OutcomeContract = "process_exit"
	OutcomeAgentUpdate OutcomeContract = "agent_update"
)

type WorkSource string

const (
	WorkSourceOrchestrator WorkSource = "orchestrator"
	WorkSourceCockpit      WorkSource = "cockpit"
	WorkSourceGitHub       WorkSource = "github"
)

func SupportedWorkSource(source WorkSource) bool {
	return source == WorkSourceOrchestrator || source == WorkSourceCockpit ||
		source == WorkSourceGitHub
}

type AssuranceMode string

const (
	AssuranceReviewed AssuranceMode = "reviewed"
	AssuranceFast     AssuranceMode = "fast"
)

func SupportedAssuranceMode(mode AssuranceMode) bool {
	return mode == AssuranceReviewed || mode == AssuranceFast
}

type DeliveryMode string

const (
	DeliveryPullRequest          DeliveryMode = "pr"
	DeliveryPullRequestAutoMerge DeliveryMode = "pr+automerge"
	DeliveryBranch               DeliveryMode = "branch"
)

func SupportedDeliveryMode(mode DeliveryMode) bool {
	return mode == DeliveryPullRequest || mode == DeliveryPullRequestAutoMerge ||
		mode == DeliveryBranch
}

type TaskSchedule struct {
	Enabled       bool       `json:"enabled"`
	Cron          string     `json:"cron,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	NextDueAt     *time.Time `json:"next_due_at,omitempty"`
	PendingDueAt  *time.Time `json:"pending_due_at,omitempty"`
	HealthStatus  string     `json:"health_status"`
	HealthCode    string     `json:"health_code,omitempty"`
	HealthMessage string     `json:"health_message,omitempty"`
}

type TaskRepository struct {
	ID             string `json:"id"`
	RemoteIdentity string `json:"remote_identity"`
}

type Task struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Prompt             string           `json:"prompt,omitempty"`
	PromptPreview      string           `json:"prompt_preview,omitempty"`
	Runtime            string           `json:"runtime"`
	ExecutionProfileID string           `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int              `json:"timeout_seconds"`
	ConcurrencyLimit   int              `json:"concurrency_limit"`
	Generation         int              `json:"generation"`
	OutcomeContract    OutcomeContract  `json:"outcome_contract"`
	PipelineID         string           `json:"pipeline_id"`
	PipelineName       string           `json:"pipeline_name"`
	Archived           bool             `json:"archived"`
	ReadOnly           bool             `json:"read_only"`
	Repositories       []TaskRepository `json:"repositories"`
	RepositoryCount    int              `json:"repository_count"`
	Schedule           TaskSchedule     `json:"schedule"`
	LastRunState       string           `json:"last_run_state,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// SessionExecution is the worker-facing execution record for one Session.
type SessionExecution struct {
	ID                    string `json:"id"`
	SessionID             string `json:"session_id"`
	AssignedWorkerID      string `json:"assigned_worker_id"`
	RequiredRuntime       string `json:"required_runtime"`
	State                 string `json:"state"`
	CancellationRequested bool   `json:"cancellation_requested"`
	// Sandbox is the frozen container posture, copied from the Run's execution
	// snapshot. Its presence is what tells the Worker to spawn the runtime
	// inside a container rather than as a bare process.
	Sandbox   *Sandbox  `json:"sandbox,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ClaimedSession contains the immutable input a Worker needs to execute a
// single repository session. It intentionally uses only the Tasks model.
type ClaimedSession struct {
	ID                    string          `json:"id"`
	RunID                 string          `json:"run_id"`
	TaskName              string          `json:"task_name"`
	Prompt                string          `json:"prompt"`
	Stages                []StageRun      `json:"stages"`
	WorkerID              string          `json:"worker_id"`
	RepositoryID          string          `json:"repository_id"`
	RequiredRuntime       string          `json:"required_runtime"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	OutcomeContract       OutcomeContract `json:"outcome_contract"`
	Target                WorkTarget      `json:"target"`
	CheckpointSHA         string          `json:"checkpoint_sha,omitempty"`
	PendingResumeSHA      string          `json:"pending_resume_sha,omitempty"`
	CheckpointPublished   bool            `json:"checkpoint_published,omitempty"`
	PullRequestURL        string          `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string          `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string          `json:"pull_request_head_sha,omitempty"`
	State                 string          `json:"state"`
	AdmittedAt            time.Time       `json:"admitted_at"`
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type SaveTaskRequest struct {
	RequestKey string `json:"request_key,omitempty"`
	Name       string `json:"name"`
	// SubmittedName lets admission supply the title a human wrote alongside
	// the uniquified Name it derives from it. Every other caller leaves it
	// empty, because for them Name already is the submitted name.
	SubmittedName      string          `json:"submitted_name,omitempty"`
	Prompt             string          `json:"prompt"`
	Runtime            string          `json:"runtime"`
	ExecutionProfileID string          `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	ConcurrencyLimit   int             `json:"concurrency_limit"`
	RepositoryIDs      []string        `json:"repository_ids"`
	Schedule           TaskSchedule    `json:"schedule"`
	ExpectedGeneration int             `json:"expected_generation,omitempty"`
	OutcomeContract    OutcomeContract `json:"outcome_contract,omitempty"`
	PipelineID         string          `json:"pipeline_id,omitempty"`
}

type SetTaskArchivedRequest struct {
	Archived           *bool `json:"archived"`
	ExpectedGeneration int   `json:"expected_generation"`
}

type SetTaskOutcomeContractRequest struct {
	OutcomeContract    OutcomeContract `json:"outcome_contract"`
	ExpectedGeneration int             `json:"expected_generation"`
}

type RunTaskRequest struct {
	RequestKey         string `json:"request_key"`
	ExecutionProfileID string `json:"execution_profile_id,omitempty"`
}

// WorkBrief is compact, operator-authored-at-admission context. Factory never
// asks an agent to create it; only the trusted orchestrator may provide one.
type WorkBrief struct {
	Context string `json:"context,omitempty"`
	Why     string `json:"why,omitempty"`
	Risk    string `json:"risk,omitempty"`
	Work    string `json:"work,omitempty"`
}

// FactoryPause is the durable global admission and dispatch switch. It never
// cancels attempts which have already begun.
type FactoryPause struct {
	Paused   bool       `json:"paused"`
	PausedAt *time.Time `json:"paused_at,omitempty"`
}

type AdmitWorkRequest struct {
	RequestKey     string        `json:"request_key"`
	Repository     string        `json:"repository"`
	Name           string        `json:"name"`
	Spec           string        `json:"spec"`
	Runtime        string        `json:"runtime"`
	Source         WorkSource    `json:"source"`
	PreApproved    bool          `json:"pre_approved"`
	Delivery       DeliveryMode  `json:"delivery,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	PipelineID     string        `json:"pipeline_id,omitempty"`
	Assurance      AssuranceMode `json:"assurance,omitempty"`
	Brief          *WorkBrief    `json:"brief,omitempty"`
	// Planning names the checkpoint and the round of review this Work is a
	// pass against. It is absent on ordinary Work and required in full when
	// present, so the roadmap joins a pass to its checkpoint on a triple that
	// is either wholly there or wholly missing.
	Planning *WorkPlanning `json:"planning,omitempty"`
}

// WorkPlanning is the planning triple a critique Work item carries. It is the
// join the Planning page reads passes and their cost through, which is why the
// round is part of it: two critiques of the same checkpoint are two rounds of
// one conversation, not two unrelated runs.
type WorkPlanning struct {
	Project string `json:"project"`
	Number  int    `json:"number"`
	Round   int    `json:"round"`
}

type AdmitWorkResponse struct {
	RunID   string       `json:"run_id"`
	TaskID  string       `json:"task_id"`
	WorkIDs []string     `json:"work_ids"`
	State   SessionState `json:"state"`
	Source  WorkSource   `json:"source"`
}

type ApproveWorkRequest struct {
	Actor string `json:"actor"`
}

type DiscardTaskOccurrenceRequest struct {
	PendingDueAt time.Time `json:"pending_due_at"`
}

type SessionState string

const (
	SessionDraft      SessionState = "draft"
	SessionBlocked    SessionState = "blocked"
	SessionQueued     SessionState = "queued"
	SessionPreparing  SessionState = "preparing"
	SessionRunning    SessionState = "running"
	SessionNeedsInput SessionState = "needs-input"
	SessionReady      SessionState = "ready"
	SessionSucceeded  SessionState = "succeeded"
	SessionFailed     SessionState = "failed"
	SessionNoChange   SessionState = "no-change"
	SessionCancelled  SessionState = "cancelled"
)

// SupportedSessionState reports whether a value is one of the states the
// sessions CHECK constraint admits. A listing filter validates against this
// rather than passing caller text into SQL.
func SupportedSessionState(state SessionState) bool {
	switch state {
	case SessionDraft, SessionBlocked, SessionQueued, SessionPreparing, SessionRunning,
		SessionNeedsInput, SessionReady, SessionSucceeded, SessionFailed,
		SessionNoChange, SessionCancelled:
		return true
	default:
		return false
	}
}

// WorkFilter narrows a Work listing. Every field is optional, and an empty
// filter lists all Work newest first.
type WorkFilter struct {
	RepositoryID string
	RunID        string
	States       []SessionState
	Planning     PlanningFilter
}

// PlanningFilter says whether a Work listing includes planning passes. Its
// zero value, PlanningFilterAll, means every item, so a caller that never
// sets the field sees no change in behavior.
type PlanningFilter string

const (
	PlanningFilterAll     PlanningFilter = ""
	PlanningFilterExclude PlanningFilter = "exclude"
	PlanningFilterOnly    PlanningFilter = "only"
)

// SupportedPlanningFilter reports whether value is a filter WorkPage accepts.
func SupportedPlanningFilter(value PlanningFilter) bool {
	switch value {
	case PlanningFilterAll, PlanningFilterExclude, PlanningFilterOnly:
		return true
	default:
		return false
	}
}

type WorkState = SessionState

const (
	WorkDraft      WorkState = SessionDraft
	WorkQueued     WorkState = SessionQueued
	WorkRunning    WorkState = SessionRunning
	WorkNeedsInput WorkState = SessionNeedsInput
	WorkReady      WorkState = SessionReady
	WorkSucceeded  WorkState = SessionSucceeded
	WorkFailed     WorkState = SessionFailed
	WorkNoChange   WorkState = SessionNoChange
	WorkCancelled  WorkState = SessionCancelled
)

type ExecutionOwner string

const (
	ExecutionOwnerNone          ExecutionOwner = "none"
	ExecutionOwnerWorkerAttempt ExecutionOwner = "worker_attempt"
	ExecutionOwnerOperator      ExecutionOwner = "operator"
)

type WorkUpdateStatus string

const (
	WorkUpdateRunning    WorkUpdateStatus = "running"
	WorkUpdateReady      WorkUpdateStatus = "ready"
	WorkUpdateNeedsInput WorkUpdateStatus = "needs-input"
	WorkUpdateFailed     WorkUpdateStatus = "failed"
	WorkUpdateNoChange   WorkUpdateStatus = "no-change"
)

func SupportedWorkUpdateStatus(status WorkUpdateStatus) bool {
	return status == WorkUpdateRunning || status == WorkUpdateReady ||
		status == WorkUpdateNeedsInput || status == WorkUpdateFailed || status == WorkUpdateNoChange
}

// WorkUpdateMerged records that the factory merged the pull request. It is
// never reported by an agent: the only writer is the control plane, with
// actor = system. It is deliberately absent from SupportedWorkUpdateStatus,
// which validates what an agent may send.
const WorkUpdateMerged WorkUpdateStatus = "merged"

// ReviewVerdict is recorded by a reviewing pipeline stage. INV-8 gates an
// automatic merge on ReviewVerdictApprove, and on nothing else.
type ReviewVerdict string

const (
	ReviewVerdictNone           ReviewVerdict = ""
	ReviewVerdictApprove        ReviewVerdict = "approve"
	ReviewVerdictRequestChanges ReviewVerdict = "request-changes"
	ReviewVerdictBlocked        ReviewVerdict = "blocked"
)

// ReviewVerdictMarker is how a reviewing agent records its verdict. The agent
// writes a line like "FACTORY-VERDICT: approve" in its stage result, and the
// Worker turns the last such line into CompleteStageRequest.ReviewVerdict.
//
// A marker rather than a flag on "factory update" is deliberate. The verdict
// belongs to a stage, and the stage is what proves the reviewer did not write
// the code; an outcome flag would let the author approve their own work.
const ReviewVerdictMarker = "FACTORY-VERDICT:"

// ParseReviewVerdict reads the last verdict marker from a stage result and
// returns the empty verdict when there is none, when the value is not one the
// schema admits, or when the text is absent entirely.
//
// Every one of those cases means "no verdict recorded", on which INV-8 refuses
// to merge. A misspelled verdict must not fail the stage and must not approve
// it: it simply does not count, which is the fail-closed direction.
func ParseReviewVerdict(result string) ReviewVerdict {
	verdict := ReviewVerdictNone
	for _, line := range strings.Split(result, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ReviewVerdictMarker) {
			continue
		}
		candidate := ReviewVerdict(strings.ToLower(strings.TrimSpace(
			strings.TrimPrefix(trimmed, ReviewVerdictMarker))))
		if candidate != ReviewVerdictNone && SupportedReviewVerdict(candidate) {
			verdict = candidate
			continue
		}
		// A marker line that names nothing legal clears any earlier verdict.
		// Reading a typo as "the approval from three lines up still stands"
		// would be the one wrong way to fail.
		verdict = ReviewVerdictNone
	}
	return verdict
}

// SupportedReviewVerdict reports whether a verdict is one the schema admits.
// The empty verdict is supported and means no review was recorded, which
// INV-8 treats as "do not merge".
func SupportedReviewVerdict(verdict ReviewVerdict) bool {
	switch verdict {
	case ReviewVerdictNone, ReviewVerdictApprove,
		ReviewVerdictRequestChanges, ReviewVerdictBlocked:
		return true
	}
	return false
}

type WorkUpdateActor string

const (
	WorkUpdateActorAgent    WorkUpdateActor = "agent"
	WorkUpdateActorOperator WorkUpdateActor = "operator"
	WorkUpdateActorSystem   WorkUpdateActor = "system"
)

func SupportedWorkUpdateActor(actor WorkUpdateActor) bool {
	return actor == WorkUpdateActorAgent || actor == WorkUpdateActorOperator || actor == WorkUpdateActorSystem
}

type RunState string

const (
	RunDraft     RunState = "draft"
	RunQueued    RunState = "queued"
	RunBlocked   RunState = "blocked"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunPartial   RunState = "partial"
	RunCancelled RunState = "cancelled"
)

type TaskSnapshot struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// SubmittedName is the title the submitter actually wrote, when that
	// differs from Name. Admission has to uniquify Name because tasks.name_key
	// is UNIQUE, so Name carries a deduplication suffix on every admitted
	// Task; this field is what a human should be shown instead. It is empty
	// for every Task whose stored name is already the submitted one, which is
	// the ordinary case rather than a fault.
	SubmittedName      string           `json:"submitted_name,omitempty"`
	Prompt             string           `json:"prompt,omitempty"`
	Runtime            string           `json:"runtime"`
	ExecutionProfileID string           `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int              `json:"timeout_seconds,omitempty"`
	ConcurrencyLimit   int              `json:"concurrency_limit,omitempty"`
	Generation         int              `json:"generation"`
	OutcomeContract    OutcomeContract  `json:"outcome_contract"`
	Pipeline           PipelineSnapshot `json:"pipeline"`
	Repositories       []TaskRepository `json:"repositories,omitempty"`
	ScheduleCron       string           `json:"cron,omitempty"`
	ScheduleTimezone   string           `json:"timezone,omitempty"`
}

// ProcedureSnapshot is the product name for the immutable Task snapshot.
// The alias keeps existing Task and scheduled Run clients source compatible.
type ProcedureSnapshot = TaskSnapshot

type WorkTarget struct {
	ID                 string `json:"id"`
	Position           int    `json:"position"`
	TargetKey          string `json:"target_key"`
	TargetKind         string `json:"target_kind"`
	RepositoryID       string `json:"repository_id"`
	RepositoryIdentity string `json:"repository_identity"`
	SourceKind         string `json:"source_kind"`
	SourceKey          string `json:"source_key"`
	SourceReference    string `json:"source_reference"`
	ContextSnapshot    string `json:"context_snapshot,omitempty"`
	PublishBranch      string `json:"publish_branch"`
}

type WorkUpdate struct {
	ID                    string           `json:"id"`
	WorkID                string           `json:"work_id"`
	AttemptID             string           `json:"attempt_id,omitempty"`
	RequestID             string           `json:"request_id"`
	Sequence              int              `json:"sequence"`
	Status                WorkUpdateStatus `json:"status"`
	Message               string           `json:"message"`
	PullRequestURL        string           `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string           `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string           `json:"pull_request_head_sha,omitempty"`
	CheckpointSHA         string           `json:"checkpoint_sha,omitempty"`
	CheckpointPublished   bool             `json:"checkpoint_published,omitempty"`
	Actor                 WorkUpdateActor  `json:"actor"`
	AcceptedAt            time.Time        `json:"accepted_at"`
}

// AgentUpdateRequest is the credential-free request sent from the injected
// factory helper to the Worker-local socket. The token scopes the request to
// one Attempt; Worker and lease credentials are deliberately absent.
type AgentUpdateRequest struct {
	WorkID         string           `json:"work_id"`
	AttemptID      string           `json:"attempt_id"`
	UpdateToken    string           `json:"update_token"`
	RequestID      string           `json:"request_id"`
	Status         WorkUpdateStatus `json:"status"`
	Message        string           `json:"message"`
	PullRequestURL string           `json:"pull_request_url,omitempty"`
}

// AttemptUpdateRequest is the Worker-to-control-plane form of an agent
// update. Delivery evidence has already been checked by the Worker for new
// requests. ReplayOnly asks for a durable exact-request lookup before mutable
// delivery state is checked again. The lease token never crosses the
// Worker-local update protocol.
type AttemptUpdateRequest struct {
	LeaseToken            string           `json:"lease_token"`
	ReplayOnly            bool             `json:"replay_only,omitempty"`
	RequestID             string           `json:"request_id"`
	Status                WorkUpdateStatus `json:"status"`
	Message               string           `json:"message"`
	PullRequestURL        string           `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string           `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string           `json:"pull_request_head_sha,omitempty"`
	CheckpointSHA         string           `json:"checkpoint_sha,omitempty"`
	CheckpointPublished   bool             `json:"checkpoint_published,omitempty"`
}

type WorkAnswerRequest struct {
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
	Actor     string `json:"actor"`
}

type WorkAnswer struct {
	ID               string    `json:"id"`
	WorkID           string    `json:"work_id"`
	QuestionUpdateID string    `json:"question_update_id"`
	RequestID        string    `json:"request_id"`
	Message          string    `json:"message"`
	Actor            string    `json:"actor"`
	AcceptedAt       time.Time `json:"accepted_at"`
}

type ReplaceWorkRequest struct {
	RequestKey string `json:"request_key"`
	WorkID     string `json:"work_id"`
}

type WorkReplacement struct {
	Result     AdmissionResult `json:"result"`
	RequestKey string          `json:"request_key"`
	Run        RunDetail       `json:"run"`
}

type WorkUpdatePage struct {
	Updates   []WorkUpdate `json:"updates"`
	NextAfter int          `json:"next_after,omitempty"`
	HasMore   bool         `json:"has_more"`
}

const (
	PersistentAutoProfileID = "persistent-auto"
	BackendPersistent       = "persistent"
	BackendDocker           = "docker"
	BackendFakeCloudRun     = "fake_cloud_run"
	CommitResolvePerAttempt = "resolve_per_attempt"
	CommitFrozen            = "frozen_commit"
)

// WorkerDispatched separates the two questions the backend field used to
// answer at once. Both persistent and docker are leased and executed by a real
// Worker against a real worktree; they differ only in how the runtime process
// is spawned. fake_cloud_run is synthesized by the control plane and touches no
// repository, so every routing, contract, and resume decision keyed on "is a
// Worker involved" asks this rather than comparing against persistent.
func WorkerDispatched(backend string) bool {
	return backend == BackendPersistent || backend == BackendDocker
}

type ExecutionSnapshot struct {
	ProfileID              string   `json:"profile_id"`
	ProfileVersion         int      `json:"profile_version"`
	Backend                string   `json:"backend"`
	Runtime                string   `json:"runtime"`
	Provider               string   `json:"provider"`
	Model                  string   `json:"model"`
	TimeoutSeconds         int      `json:"timeout_seconds"`
	ResourceClass          string   `json:"resource_class"`
	CommitResolutionPolicy string   `json:"commit_resolution_policy"`
	Sandbox                *Sandbox `json:"sandbox,omitempty"`
}

type ExecutionProfile struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	Runtime           string    `json:"runtime"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	TimeoutSeconds    int       `json:"timeout_seconds"`
	ResourceClass     string    `json:"resource_class"`
	MaxConcurrent     int       `json:"max_concurrent"`
	Enabled           bool      `json:"enabled"`
	Healthy           bool      `json:"healthy"`
	HealthReason      string    `json:"health_reason,omitempty"`
	FakeOutcome       string    `json:"fake_outcome,omitempty"`
	FakeResult        string    `json:"fake_result,omitempty"`
	FakeError         string    `json:"fake_error,omitempty"`
	Sandbox           *Sandbox  `json:"sandbox,omitempty"`
	SyntheticWorkerID string    `json:"synthetic_worker_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type SaveExecutionProfileRequest struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Runtime         string   `json:"runtime"`
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	ResourceClass   string   `json:"resource_class"`
	MaxConcurrent   int      `json:"max_concurrent"`
	Enabled         bool     `json:"enabled"`
	Healthy         bool     `json:"healthy"`
	HealthReason    string   `json:"health_reason,omitempty"`
	FakeOutcome     string   `json:"fake_outcome,omitempty"`
	FakeResult      string   `json:"fake_result,omitempty"`
	FakeError       string   `json:"fake_error,omitempty"`
	Sandbox         *Sandbox `json:"sandbox,omitempty"`
	ExpectedVersion int      `json:"expected_version,omitempty"`
}

type ExecutionProfilePage struct {
	Profiles []ExecutionProfile `json:"profiles"`
}

type Session struct {
	ID                    string            `json:"id"`
	RunID                 string            `json:"run_id"`
	RepositoryID          string            `json:"repository_id"`
	RepositoryIdentity    string            `json:"repository_identity"`
	ResolvedPrompt        string            `json:"resolved_prompt,omitempty"`
	RequiredRuntime       string            `json:"required_runtime"`
	Execution             ExecutionSnapshot `json:"execution"`
	TimeoutSeconds        int               `json:"timeout_seconds"`
	State                 SessionState      `json:"state"`
	BlockedReason         string            `json:"blocked_reason,omitempty"`
	AssignedWorkerID      string            `json:"assigned_worker_id,omitempty"`
	CancellationRequested bool              `json:"cancellation_requested"`
	RetryMayRepeatEffects bool              `json:"retry_may_repeat_effects"`
	AdmittedAt            time.Time         `json:"admitted_at"`
	StartedAt             *time.Time        `json:"started_at,omitempty"`
	TerminalAt            *time.Time        `json:"terminal_at,omitempty"`
	Result                string            `json:"result,omitempty"`
	FailureReason         string            `json:"failure_reason,omitempty"`
	Target                WorkTarget        `json:"target"`
	PredecessorWorkID     string            `json:"predecessor_work_id,omitempty"`
	ExecutionOwner        ExecutionOwner    `json:"execution_owner"`
	WaitingReason         string            `json:"waiting_reason,omitempty"`
	LatestProgress        string            `json:"latest_progress,omitempty"`
	Question              string            `json:"question,omitempty"`
	CheckpointSHA         string            `json:"checkpoint_sha,omitempty"`
	PendingResumeSHA      string            `json:"pending_resume_sha,omitempty"`
	CheckpointPublished   bool              `json:"checkpoint_published,omitempty"`
	ApprovedBy            string            `json:"approved_by,omitempty"`
	ApprovedAt            *time.Time        `json:"approved_at,omitempty"`
	Delivery              DeliveryMode      `json:"delivery"`
	Answer                string            `json:"answer,omitempty"`
	AnsweredBy            string            `json:"answered_by,omitempty"`
	PullRequestURL        string            `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string            `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string            `json:"pull_request_head_sha,omitempty"`
	TerminalMessage       string            `json:"terminal_message,omitempty"`
	Stages                []StageRun        `json:"stages,omitempty"`
	Updates               []WorkUpdate      `json:"updates,omitempty"`
	Attempts              []Attempt         `json:"attempts,omitempty"`
}

// Work is the product-facing name for the durable Session-backed lifecycle.
type Work = Session

type Run struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"task_id"`
	Task            TaskSnapshot      `json:"task"`
	Execution       ExecutionSnapshot `json:"execution"`
	OutcomeContract OutcomeContract   `json:"outcome_contract"`
	Targets         []WorkTarget      `json:"targets"`
	Source          string            `json:"source"`
	Assurance       AssuranceMode     `json:"assurance"`
	Brief           *WorkBrief        `json:"brief,omitempty"`
	ScheduledAt     *time.Time        `json:"scheduled_at,omitempty"`
	State           RunState          `json:"state"`
	NeedsAttention  bool              `json:"needs_attention"`
	SessionCount    int               `json:"session_count"`
	SucceededCount  int               `json:"succeeded_count"`
	ReadyCount      int               `json:"ready_count"`
	NeedsInputCount int               `json:"needs_input_count"`
	NoChangeCount   int               `json:"no_change_count"`
	FailedCount     int               `json:"failed_count"`
	CancelledCount  int               `json:"cancelled_count"`
	ActiveCount     int               `json:"active_count"`
	AdmittedAt      time.Time         `json:"admitted_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	TerminalAt      *time.Time        `json:"terminal_at,omitempty"`
}

type RunDetail struct {
	Run              Run             `json:"run"`
	ProviderSnapshot json.RawMessage `json:"provider_snapshot,omitempty"`
	Sessions         []Session       `json:"sessions"`
}

type RunSummary struct {
	ID         string              `json:"id"`
	TaskName   string              `json:"task_name"`
	State      RunState            `json:"state"`
	Source     string              `json:"source"`
	AdmittedAt time.Time           `json:"admitted_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Sessions   []RunSessionSummary `json:"sessions"`
}

type RunListSummary struct {
	ID              string     `json:"id"`
	TaskName        string     `json:"task_name"`
	State           RunState   `json:"state"`
	Source          string     `json:"source"`
	NeedsAttention  bool       `json:"needs_attention"`
	SessionCount    int        `json:"session_count"`
	SucceededCount  int        `json:"succeeded_count"`
	ReadyCount      int        `json:"ready_count"`
	NeedsInputCount int        `json:"needs_input_count"`
	NoChangeCount   int        `json:"no_change_count"`
	FailedCount     int        `json:"failed_count"`
	CancelledCount  int        `json:"cancelled_count"`
	ActiveCount     int        `json:"active_count"`
	AdmittedAt      time.Time  `json:"admitted_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TerminalAt      *time.Time `json:"terminal_at,omitempty"`
}

type RunListPage struct {
	Runs       []RunListSummary `json:"runs"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// WorkStage is the stage a Work item is on, or the one it stopped on. It is
// deliberately narrower than StageRun: a list response must not carry a
// stage's prompt, command, result or error, any of which can run to hundreds
// of kilobytes on a single row.
type WorkStage struct {
	Position int           `json:"position"`
	Name     string        `json:"name"`
	Kind     string        `json:"kind,omitempty"`
	State    StageRunState `json:"state"`
	Model    string        `json:"model,omitempty"`
	Effort   string        `json:"effort,omitempty"`
}

// WorkListSummary is one card on the Work board: a single repository's share
// of one admitted Work item.
//
// CostUSD is a pointer because a runtime that reports no cost and a runtime
// that spent nothing are different facts. Only Claude Code reports cost today,
// so a nil here means "unavailable" and must never be rendered as $0.00.
type WorkListSummary struct {
	ID                 string       `json:"id"`
	RunID              string       `json:"run_id"`
	TaskID             string       `json:"task_id"`
	TaskName           string       `json:"task_name"`
	RepositoryID       string       `json:"repository_id"`
	RepositoryIdentity string       `json:"repository_identity"`
	State              SessionState `json:"state"`
	Source             string       `json:"source"`
	Brief              *WorkBrief   `json:"brief,omitempty"`
	BlockedReason      string       `json:"blocked_reason,omitempty"`
	FailureReason      string       `json:"failure_reason,omitempty"`
	AssignedWorkerID   string       `json:"assigned_worker_id,omitempty"`
	// AssignedWorkerName is what an operator recognises. The id is a UUID and
	// is kept for correlation, but a card showing it instead of the name is
	// unreadable.
	AssignedWorkerName string `json:"assigned_worker_name,omitempty"`
	Runtime            string `json:"runtime,omitempty"`
	PullRequestURL     string `json:"pull_request_url,omitempty"`
	NeedsAttention     bool   `json:"needs_attention"`
	// Planning is true exactly when the session has a planning triple, the
	// same test roadmapPasses uses to find critique passes.
	Planning        bool       `json:"planning"`
	CurrentStage    *WorkStage `json:"current_stage,omitempty"`
	StageCount      int        `json:"stage_count"`
	CompletedStages int        `json:"completed_stage_count"`
	AttemptCount    int        `json:"attempt_count"`
	// CostUSD is the sum over the stages that reported one. Nil means no
	// stage of this Work reported any cost at all.
	CostUSD      *float64             `json:"reported_cost_usd,omitempty"`
	Verification *VerificationSummary `json:"verification,omitempty"`
	AdmittedAt   time.Time            `json:"admitted_at"`
	StartedAt    *time.Time           `json:"started_at,omitempty"`
	TerminalAt   *time.Time           `json:"terminal_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

// VerificationCheckSource says who is vouching for a check. A code stage is
// Factory's own evidence: it ran the command and holds the exit status. An
// agent-reported check is a claim parsed out of an agent's own summary, which
// Factory did not execute and cannot confirm.
type VerificationCheckSource string

const (
	VerificationSourceCodeStage     VerificationCheckSource = "code-stage"
	VerificationSourceAgentReported VerificationCheckSource = "agent-reported"
)

type VerificationCheckState string

const (
	VerificationPassed VerificationCheckState = "passed"
	VerificationFailed VerificationCheckState = "failed"
	VerificationNotRun VerificationCheckState = "not-run"
)

type VerificationCheck struct {
	Name   string                  `json:"name"`
	Source VerificationCheckSource `json:"source"`
	State  VerificationCheckState  `json:"state"`
	Detail string                  `json:"detail,omitempty"`
}

// VerificationSummary counts checks, never tests. Factory knows which commands
// a code stage ran and how they exited; it does not know how many test cases
// those commands contained, and it does not guess.
type VerificationSummary struct {
	RecordedChecks int                 `json:"recorded_checks"`
	Passed         int                 `json:"passed"`
	Failed         int                 `json:"failed"`
	NotRun         int                 `json:"unknown"`
	Items          []VerificationCheck `json:"items,omitempty"`
}

type WorkListPage struct {
	Work       []WorkListSummary `json:"work"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type RunSessionSummary struct {
	ID                 string       `json:"id"`
	RepositoryIdentity string       `json:"repository_identity"`
	State              SessionState `json:"state"`
	BlockedReason      string       `json:"blocked_reason,omitempty"`
	AssignedWorkerID   string       `json:"assigned_worker_id,omitempty"`
	AttemptCount       int          `json:"attempt_count"`
	Result             string       `json:"result,omitempty"`
	FailureReason      string       `json:"failure_reason,omitempty"`
}

type RunPage struct {
	Runs       []Run  `json:"runs"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type OverviewRunMetrics struct {
	Window                  string   `json:"window"`
	TotalRuns               int      `json:"total_runs"`
	CompletedRuns           int      `json:"completed_runs"`
	CompletionRate          *float64 `json:"completion_rate"`
	AverageQueueTimeSeconds *float64 `json:"average_queue_time_seconds"`
	AverageCycleTimeSeconds *float64 `json:"average_cycle_time_seconds"`
}

type Overview struct {
	ActiveRuns       int                `json:"active_runs"`
	NeedsAttention   int                `json:"needs_attention"`
	CompletedLast24H int                `json:"completed_last_24h"`
	Cost             OverviewCost       `json:"cost"`
	WorkersOnline    int                `json:"workers_online"`
	WorkersTotal     int                `json:"workers_total"`
	RunMetrics       OverviewRunMetrics `json:"run_metrics"`
	RecentRuns       []Run              `json:"recent_runs"`
	UpcomingTasks    []Task             `json:"upcoming_tasks"`
	GeneratedAt      time.Time          `json:"generated_at"`
}

// OverviewCost reports 24-hour spend without letting an absence pass as a
// zero. Every total is a pointer: nil means no runtime reported anything,
// which is a different fact from a measured zero.
//
// UnavailableWork is what keeps a total honest. Only Claude Code reports cost
// today, so most fleets have real spend the figures cannot see, and a total
// that does not say so reads as complete when it is not.
type OverviewCost struct {
	// TotalUSD, MeasuredWork, UnavailableWork, AverageUSD and the dearest-Work
	// fields cover every terminal Work item Factory has ever run. A lifetime
	// figure answers "what has this cost me"; a day-scoped one resets before an
	// operator has necessarily looked at it.
	TotalUSD        *float64 `json:"total_usd,omitempty"`
	MeasuredWork    int      `json:"measured_work"`
	UnavailableWork int      `json:"unavailable_work"`
	AverageUSD      *float64 `json:"average_usd,omitempty"`
	HighestUSD      *float64 `json:"highest_usd,omitempty"`
	HighestWorkID   string   `json:"highest_work_id,omitempty"`
	HighestWorkName string   `json:"highest_work_name,omitempty"`
	// RecentUSD is the trailing RecentDays of reported spend, which is the rate
	// the lifetime total is growing at.
	RecentUSD  *float64    `json:"recent_usd,omitempty"`
	RecentDays int         `json:"recent_days"`
	ByModel    []ModelCost `json:"by_model,omitempty"`
}

// ModelCost is one model's share of all reported spend, dearest first.
type ModelCost struct {
	Model    string  `json:"model"`
	CostUSD  float64 `json:"cost_usd"`
	Attempts int     `json:"attempts"`
}

// StageHandoff is the bounded evidence one stage passed to the next.
//
// It is derived from the predecessor's stored stage row rather than stored
// separately. The Worker builds the same envelope at execution time from the
// same fields, so persisting a second copy would be a projection that can
// drift from its source with no way to tell which is right.
//
// This is deliberately not called a conversation. Stages share a worktree and
// a bounded evidence hand-off, not a message channel.
type StageHandoff struct {
	FromStage int           `json:"from_stage"`
	ToStage   int           `json:"to_stage"`
	Kind      string        `json:"kind"`
	FromState StageRunState `json:"from_state"`
	Summary   string        `json:"summary"`
	Truncated bool          `json:"truncated"`
	// Delivered is false when the predecessor never finished, so the successor
	// received nothing. It distinguishes "no evidence" from "empty evidence".
	Delivered bool `json:"delivered"`
}

// WorkSibling is another repository's share of the same Run.
type WorkSibling struct {
	ID                 string       `json:"id"`
	RepositoryIdentity string       `json:"repository_identity"`
	State              SessionState `json:"state"`
}

// WorkCost breaks a Work item's spend down far enough to answer "what did the
// retry cost me". Every figure is a pointer or omitted when unreported.
type WorkCost struct {
	TotalUSD  *float64              `json:"total_usd,omitempty"`
	ByStage   []StageCost           `json:"by_stage,omitempty"`
	ByAttempt []AttemptCost         `json:"by_attempt,omitempty"`
	ByModel   map[string]ModelUsage `json:"by_model,omitempty"`
	// UnavailableStages counts stages that ran a model and reported no cost,
	// so a partial total can say so instead of passing as complete.
	UnavailableStages int `json:"unavailable_stages"`
}

type StageCost struct {
	Position int      `json:"position"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind,omitempty"`
	Model    string   `json:"model,omitempty"`
	CostUSD  *float64 `json:"cost_usd,omitempty"`
	Usage    *Usage   `json:"usage,omitempty"`
}

type AttemptCost struct {
	AttemptNumber int      `json:"attempt_number"`
	State         string   `json:"state"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
	Usage         *Usage   `json:"usage,omitempty"`
}

// WorkDetail is one Work item's full record: what it is, where it stands, what
// each stage did and passed on, and what it cost.
type WorkDetail struct {
	Work       Work              `json:"work"`
	RunID      string            `json:"run_id"`
	TaskID     string            `json:"task_id"`
	TaskName   string            `json:"task_name"`
	TaskPrompt string            `json:"task_prompt,omitempty"`
	Source     string            `json:"source"`
	Assurance  AssuranceMode     `json:"assurance,omitempty"`
	Brief      *WorkBrief        `json:"brief,omitempty"`
	Pipeline   *PipelineSnapshot `json:"pipeline,omitempty"`
	Siblings   []WorkSibling     `json:"siblings,omitempty"`
	Handoffs   []StageHandoff    `json:"handoffs,omitempty"`
	// WorkerName is the assigned Worker's readable name, resolved so the detail
	// page shows it rather than the UUID the Work record carries.
	WorkerName     string              `json:"worker_name,omitempty"`
	Verification   VerificationSummary `json:"verification"`
	Cost           WorkCost            `json:"cost"`
	NeedsAttention bool                `json:"needs_attention"`
	UpdatedAt      time.Time           `json:"updated_at"`
}
