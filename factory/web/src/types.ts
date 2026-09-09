export type Runtime = "pi" | "codex" | "claude-code";
export type ExecutionBackend = "persistent" | "fake_cloud_run";
export type SessionState = "draft" | "blocked" | "queued" | "preparing" | "running" | "needs-input" | "ready" | "succeeded" | "failed" | "no-change" | "cancelled";
export type RunState = "draft" | "blocked" | "queued" | "running" | "succeeded" | "failed" | "partial" | "cancelled";
// Migration 035 widened the runs source CHECK for the admission path. A draft
// only ever reaches the cockpit through AdmitWork, so its source is always one
// of the three admission sources, never one of the three original ones.
export type RunSource = "manual" | "schedule" | "provider_history" | "orchestrator" | "cockpit" | "github";

export interface ExecutionProfile {
  id: string;
  name: string;
  kind: ExecutionBackend;
  version: number;
  runtime: Runtime | "";
  provider: string;
  model: string;
  timeout_seconds: number;
  resource_class: string;
  max_concurrent: number;
  enabled: boolean;
  healthy: boolean;
  health_reason?: string;
  synthetic_worker_id: string;
}

export interface ExecutionSnapshot {
  profile_id: string;
  profile_version: number;
  backend: ExecutionBackend;
  runtime: Runtime;
  provider: string;
  model: string;
  timeout_seconds: number;
  resource_class: string;
  commit_resolution_policy: "resolve_per_attempt" | "frozen_commit";
}

export interface TaskRepository {
  id: string;
  remote_identity: string;
}

export interface TaskSchedule {
  enabled: boolean;
  cron?: string;
  timezone?: string;
  next_due_at?: string;
  pending_due_at?: string;
  health_status: "disabled" | "healthy" | "blocked" | "error";
  health_code?: string;
  health_message?: string;
}

export interface Task {
  id: string;
  name: string;
  prompt: string;
  prompt_preview?: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  pipeline_id?: string;
  pipeline_name?: string;
  archived: boolean;
  read_only: boolean;
  repositories: TaskRepository[] | null;
  repository_count: number;
  schedule: TaskSchedule;
  last_run_state?: RunState;
  created_at: string;
  updated_at: string;
}

export interface SaveTaskInput {
  name: string;
  prompt: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  repository_ids: string[];
  schedule: { enabled: boolean; cron?: string; timezone?: string };
  expected_generation?: number;
  pipeline_id?: string;
}

export type PipelineStageKind = "agent" | "code" | "delivery";

export interface PipelineStage {
  position: number;
  name: string;
  kind?: PipelineStageKind;
  prompt?: string;
  command?: string;
  model?: string;
  effort?: string;
}

export interface Pipeline {
  id: string;
  name: string;
  generation: number;
  stages: PipelineStage[];
  created_at: string;
  updated_at: string;
}

export interface SavePipelineInput {
  name: string;
  stages: Array<{ name: string; kind?: PipelineStageKind; prompt?: string; command?: string; model?: string; effort?: string }>;
  expected_generation?: number;
}

export interface Usage {
  input_tokens?: number;
  output_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
}

export interface ModelUsage extends Usage {
  cost_usd: number;
}

export interface StageRun extends PipelineStage {
  usage?: Usage;
  // Absent means the runtime reported no cost, which is not the same as a
  // cost of zero. The server stores *float64 and omits the field when NULL,
  // so `undefined` must never be coerced to 0 for display or for a total.
  cost_usd?: number;
  models?: Record<string, ModelUsage>;
  state: "pending" | "running" | "succeeded" | "failed" | "cancelled";
  review_verdict?: "approve" | "request-changes" | "blocked";
  result?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
}

export interface TaskSnapshot {
  id: string;
  name: string;
  // The title the submitter wrote, when it differs from the uniquified name
  // admission had to store. Absent or empty for every other Task.
  submitted_name?: string;
  prompt: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  repositories: TaskRepository[] | null;
  cron?: string;
  timezone?: string;
  pipeline?: {
    id: string;
    name: string;
    generation: number;
    stages: PipelineStage[];
  };
}

export interface Attempt {
  id: string;
  execution_id: string;
  worker_id: string;
  attempt_number: number;
  state: "preparing" | "running" | "succeeded" | "failed" | "cancelled" | "lost";
  lease_expires_at: string;
  result?: string;
  error?: string;
  // As on StageRun: absent means unreported, never zero.
  cost_usd?: number;
  usage?: Usage;
  models?: Record<string, ModelUsage>;
  started_at?: string;
  completed_at?: string;
  created_at: string;
}

export interface WorkUpdate {
  status: "running" | "ready" | "needs-input" | "failed" | "no-change" | "merged";
  message: string;
  actor: "agent" | "operator" | "system";
  accepted_at: string;
}

export interface Session {
  id: string;
  run_id: string;
  repository_id: string;
  repository_identity: string;
  resolved_prompt?: string;
  required_runtime: Runtime;
  execution: ExecutionSnapshot;
  timeout_seconds: number;
  state: SessionState;
  blocked_reason?: string;
  assigned_worker_id?: string;
  cancellation_requested: boolean;
  retry_may_repeat_effects: boolean;
  admitted_at: string;
  started_at?: string;
  terminal_at?: string;
  result?: string;
  failure_reason?: string;
  approved_by?: string;
  approved_at?: string;
  pull_request_url?: string;
  pull_request_head_branch?: string;
  pull_request_head_sha?: string;
  stages?: StageRun[] | null;
  updates?: WorkUpdate[] | null;
  attempts?: Attempt[] | null;
}

// A Run's Work targets. Admission creates exactly one per draft Run, and the
// target id is the Work id the approval endpoint takes.
export interface WorkTarget {
  id: string;
  repository_identity: string;
}

export interface FactoryPause {
  paused: boolean;
  paused_at?: string;
}

export interface WorkBrief {
  context?: string;
  why?: string;
  risk?: string;
  work?: string;
}

export interface Run {
  id: string;
  task_id: string;
  task: TaskSnapshot;
  execution: ExecutionSnapshot;
  targets?: WorkTarget[] | null;
  source: RunSource;
  assurance?: "reviewed" | "fast";
  brief?: WorkBrief;
  scheduled_at?: string;
  state: RunState;
  needs_attention: boolean;
  session_count: number;
  succeeded_count: number;
  ready_count: number;
  needs_input_count: number;
  no_change_count: number;
  failed_count: number;
  cancelled_count: number;
  active_count: number;
  admitted_at: string;
  updated_at: string;
  terminal_at?: string;
}

export interface RunDetail {
  run: Run;
  sessions: Session[] | null;
}

export type VerificationCheckSource = "code-stage" | "agent-reported";
export type VerificationCheckState = "passed" | "failed" | "not-run";

export interface VerificationCheck {
  name: string;
  source: VerificationCheckSource;
  state: VerificationCheckState;
  detail?: string;
}

// Counts of checks, never of tests. Factory knows which commands a code stage
// ran and how they exited; it does not know how many test cases those commands
// contained and does not guess.
export interface VerificationSummary {
  recorded_checks: number;
  passed: number;
  failed: number;
  unknown: number;
  items?: VerificationCheck[] | null;
}

export interface WorkStage {
  position: number;
  name: string;
  kind?: PipelineStageKind;
  state: StageRun["state"];
  model?: string;
  effort?: string;
}

// WorkItem is one card on the Work board: a single repository's share of one
// admitted Work item. A Run can span several repositories, so this, not Run,
// is what an operator works on.
export interface WorkItem {
  id: string;
  run_id: string;
  task_id: string;
  task_name: string;
  repository_id: string;
  repository_identity: string;
  state: SessionState;
  source: RunSource;
  brief?: WorkBrief;
  blocked_reason?: string;
  failure_reason?: string;
  assigned_worker_id?: string;
  // The readable name. The id stays for correlation, but a card showing a
  // UUID instead of a name is unreadable.
  assigned_worker_name?: string;
  runtime?: Runtime;
  pull_request_url?: string;
  needs_attention: boolean;
  // True exactly when the session has a planning triple: a critique pass run
  // from the Planning page, not build work.
  planning: boolean;
  current_stage?: WorkStage;
  stage_count: number;
  completed_stage_count: number;
  attempt_count: number;
  // Absent means no stage of this Work reported a cost. That is not a cost of
  // zero, and must never render as $0.00.
  reported_cost_usd?: number;
  verification?: VerificationSummary;
  admitted_at: string;
  started_at?: string;
  terminal_at?: string;
  updated_at: string;
}

export interface StageHandoff {
  from_stage: number;
  to_stage: number;
  kind: "agent-result" | "command-output" | "review-verdict" | "delivery-evidence";
  from_state: StageRun["state"];
  summary: string;
  truncated: boolean;
  // False when the predecessor never finished, so the successor received
  // nothing. Distinct from an empty summary, which means it received nothing
  // useful.
  delivered: boolean;
}

export interface WorkSibling {
  id: string;
  repository_identity: string;
  state: SessionState;
}

export interface StageCost {
  position: number;
  name: string;
  kind?: PipelineStageKind;
  model?: string;
  cost_usd?: number;
  usage?: Usage;
}

export interface AttemptCost {
  attempt_number: number;
  state: string;
  cost_usd?: number;
  usage?: Usage;
}

export interface WorkCost {
  total_usd?: number;
  by_stage?: StageCost[] | null;
  by_attempt?: AttemptCost[] | null;
  by_model?: Record<string, ModelUsage> | null;
  // Stages that reached a model and reported nothing, so a partial total can
  // say it is partial instead of passing as complete.
  unavailable_stages: number;
}

export interface WorkDetail {
  work: Session;
  run_id: string;
  task_id: string;
  task_name: string;
  task_prompt?: string;
  source: RunSource;
  assurance?: "reviewed" | "fast";
  brief?: WorkBrief;
  pipeline?: { id: string; name: string; generation: number; stages: PipelineStage[] };
  siblings?: WorkSibling[] | null;
  handoffs?: StageHandoff[] | null;
  worker_name?: string;
  verification: VerificationSummary;
  cost: WorkCost;
  needs_attention: boolean;
  updated_at: string;
}

export interface WorkPage {
  work: WorkItem[] | null;
  next_cursor?: string;
}

export interface RunPage {
  runs: Run[] | null;
  next_cursor?: string;
}

export interface ModelCost {
  model: string;
  cost_usd: number;
  attempts: number;
}

// Every total is optional: absent means no runtime reported anything, which is
// a different fact from a measured zero. unavailable_work is what keeps a
// total honest, since only Claude Code reports cost today.
export interface OverviewCost {
  // Lifetime figures. A day-scoped total resets before an operator has
  // necessarily looked at it, so these cover every terminal Work item.
  total_usd?: number;
  measured_work: number;
  unavailable_work: number;
  average_usd?: number;
  highest_usd?: number;
  highest_work_id?: string;
  highest_work_name?: string;
  // The trailing window, which is the rate the lifetime total is growing at.
  recent_usd?: number;
  recent_days: number;
  by_model?: ModelCost[] | null;
}

export interface Overview {
  active_runs: number;
  needs_attention: number;
  completed_last_24h: number;
  cost: OverviewCost;
  workers_online: number;
  workers_total: number;
  run_metrics: {
    window: "24h";
    total_runs: number;
    completed_runs: number;
    completion_rate: number | null;
    average_queue_time_seconds: number | null;
    average_cycle_time_seconds: number | null;
  };
  recent_runs: Run[] | null;
  upcoming_tasks: Task[] | null;
  generated_at: string;
}

export interface Capability {
  kind: "tool" | "runtime";
  name: string;
  status: "ready" | "missing" | "unauthenticated" | "unhealthy";
  version?: string;
  message?: string;
}

export interface Repository {
  id: string;
  key: string;
  remote_identity: string;
  retained_count: number;
}

export interface RetainedWorktree {
  attempt_id: string;
  repository_id: string;
  path: string;
  reason: string;
  cleanup_command: string;
}

export interface Worker {
  id: string;
  name: string;
  labels?: Record<string, string>;
  worker_version: string;
  runtime: Runtime;
  runtime_version: string;
  capabilities?: Capability[];
  capacity: number;
  active_count: number;
  health: "healthy" | "unhealthy";
  online: boolean;
  repositories: Repository[];
  source_access?: Array<{ provider: string; hostname: string }>;
  accepts_managed_repositories?: boolean;
  repository_cache_count?: number;
  retained_worktrees: RetainedWorktree[];
  registered_at: string;
  last_heartbeat: string;
  current_run_title?: string;
}

export type DeliveryMode = "pr" | "pr+automerge" | "branch";

export interface ManagedRepository {
  id: string;
  remote_identity: string;
  enabled: boolean;
  default_delivery: DeliveryMode;
  created_at: string;
  updated_at: string;
}

export interface ManagedRepositoryWorkerReadiness {
  id: string;
  name: string;
  cached: boolean;
  advertised: boolean;
  ready: boolean;
  reason: string;
}

export interface ManagedRepositoryReadiness {
  routing_ready: boolean;
  workers: ManagedRepositoryWorkerReadiness[];
}

export interface AttemptEvent {
  sequence: number;
  kind: string;
  payload: unknown;
  server_time: string;
}

export interface AttemptEventPage {
  events: AttemptEvent[];
  next_after: number;
  has_more: boolean;
}

export interface APIErrorBody {
  error: { code: string; message: string };
}

// The Roadmap is the factory's own planning state, read from its database on
// every request. This page never writes it; the light factory does, through
// /api/v1/planning.
//
// Five statuses and no more. The server refuses anything else, so a sixth word
// here would be a label the page can draw and the factory can never send.
export type CheckpointStatus = "planned" | "review" | "fog" | "frozen" | "built";

// A pebble's state, work_id and pull_request_url are joined in from the
// factory's own Work rows. An empty state means no Work carries this pebble
// yet, which is different from one whose build failed. "built" is not a
// session state at all: it means the pebble was built outside the factory and
// no Work row was found for it. built_ref is the commit or pull request that
// records it, and is carried whether or not a Work row later won the state.
export interface RoadmapPebble {
  ordinal: number;
  slug: string;
  title: string;
  summary?: string;
  state?: SessionState | "built" | "";
  work_id?: string;
  pull_request_url?: string;
  built_ref?: string;
}

// StoneState is the one word the page colours a stone box by. Trouble
// outranks progress and progress outranks done, so a stone never looks
// finished while part of it is broken or still moving.
export type StoneState = "planned" | "part" | "working" | "failed" | "done";

// A stone is one big chunk of a checkpoint. A checkpoint split before
// anyone grouped it arrives as a single stone holding every pebble, so
// nothing is ever hidden by a grouping.
export interface RoadmapStone {
  id: string;
  title: string;
  statement?: string;
  pebbles: RoadmapPebble[] | null;
  state: StoneState;
}

// One critique of one checkpoint. It is a factory Work item, so work_id opens
// the Work page where its findings, its attempts and its failure actually
// live. mode is always "critique"; the field stays because a second kind of
// pass would be a second value here rather than a second shape.
export interface RoadmapPass {
  at: string;
  mode: string;
  round: number;
  model?: string;
  cost_usd: number;
  duration_ms?: number;
  outcome?: string;
  work_id?: string;
}

// The critique that has been submitted and has not come back: queued,
// preparing or running. Absent means nothing is turning, which is the usual
// answer. It is a Work row like any other, so it cannot go stale the way a
// file held open by a killed process could.
export interface RoadmapLivePass {
  mode: string;
  round: number;
  model?: string;
  started: string;
  work_id?: string;
}

export interface RoadmapCheckpoint {
  number: number;
  title: string;
  summary?: string;
  status: CheckpointStatus;
  planned: boolean;
  stones: RoadmapStone[] | null;
  pebbles: RoadmapPebble[] | null;
  passes: RoadmapPass[] | null;
  live?: RoadmapLivePass | null;
  cost_usd: number;
  pass_rounds: number;
}

export interface RoadmapProject {
  project: string;
  title: string;
  statement?: string;
  checkpoints: RoadmapCheckpoint[] | null;
  live?: RoadmapLivePass | null;
  cost_usd: number;
  built_count: number;
}

export interface RoadmapWaiting {
  project: string;
  number: number;
  title: string;
  status: CheckpointStatus;
  reason: string;
  action: string;
  cost_usd: number;
  pass_rounds: number;
}

export interface Roadmap {
  configured: boolean;
  projects: RoadmapProject[] | null;
  waiting: RoadmapWaiting[] | null;
  read_at: string;
}

// One checkpoint read straight from /api/v1/planning. The roadmap deliberately
// carries none of this: body is a whole PRD and answers a whole review, and
// putting either on a payload that lists every checkpoint of every project
// would make the Planning page pay for text it shows one of at a time.
//
// Both are read-only here. The review happens in lavish and the answers are
// saved by the light factory; the cockpit shows what was written and offers no
// way to change it.
export interface PlanningCheckpoint {
  project: string;
  number: number;
  title: string;
  summary?: string;
  status: CheckpointStatus;
  body?: string;
  answers?: string;
  frozen_at?: string;
  created_at: string;
  updated_at: string;
}

// api.roadmap normalizes both nullable arrays away once, so a view never
// repeats the check. Go writes a nil slice as null, and every list on this
// response is legitimately empty at some point in a project's life.
export type LoadedRoadmap = Roadmap & { projects: RoadmapProject[]; waiting: RoadmapWaiting[] };
