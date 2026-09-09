import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, ArrowRight, RotateCcw, StopCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { api, APIError } from "./api";
import { duration, runtimeLabel, timeAgo } from "./format";
import { byteLabel, repositoryName, stageTokens, workerLabel } from "./work-format";
import type { Attempt, StageCost, StageRun, VerificationCheck, WorkDetail } from "./types";
import { ErrorState, InlineError, LoadingState, StatusBadge } from "./ui";
import { AttemptStream } from "./Runs";

type Tab = "brief" | "stages" | "outcome" | "evidence";

// Brief is the default tab because opening a Work item should answer "what is
// this and where does it stand", never open on a wall of logs. Everything raw
// lives under Evidence.
const tabs: Array<{ key: Tab; label: string }> = [
  { key: "brief", label: "Brief" },
  { key: "stages", label: "Stages" },
  { key: "outcome", label: "Outcome" },
  { key: "evidence", label: "Evidence" },
];

export function WorkDetailView({ id, onBack, onRun, onWork, onMissing }: {
  id: string;
  onBack: () => void;
  onRun: (runID: string) => void;
  onWork: (workID: string) => void;
  // Called when the id is not Work. Both ids are UUIDs, so a link saved when
  // /work/<id> meant a Run is indistinguishable until the server answers.
  onMissing: (id: string) => void;
}) {
  const [tab, setTab] = useState<Tab>("brief");
  const client = useQueryClient();
  const query = useQuery({ queryKey: ["work", id], queryFn: () => api.workDetail(id), refetchInterval: 3_000 });
  const invalidate = () => {
    void query.refetch();
    void client.invalidateQueries({ queryKey: ["work"] });
  };
  const cancel = useMutation({ mutationFn: () => api.cancelSession(query.data!.run_id, id), onSuccess: invalidate });
  const retry = useMutation({ mutationFn: () => api.retryWork(id), onSuccess: invalidate });

  // A saved /work/<run-id> from before the routes split is not an error: it is
  // a Run, and sending the operator there is what they meant. Any other
  // failure still surfaces.
  const missing = query.error instanceof APIError && query.error.status === 404;
  useEffect(() => {
    if (missing) onMissing(id);
  }, [missing, id, onMissing]);

  if (query.isPending || missing) return <LoadingState label="Loading Work" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  const detail = query.data;
  const work = detail.work;
  const stages = work.stages ?? [];
  const active = ["blocked", "queued", "preparing", "running"].includes(work.state);
  const retryable = work.state === "failed" || work.state === "cancelled";
  const currentStage = stages.find((stage) => stage.state === "running");
  return <div className="page detail-page work-detail">
    <button className="back-button" onClick={onBack}><ArrowLeft size={14} /> Work</button>
    <div className="detail-heading">
      <div>
        <span className="eyebrow">{detail.source.replace("_", " ")} · {id.slice(0, 8)} · <b>{repositoryName(work.repository_identity)}</b></span>
        <h1>{detail.task_name}</h1>
        <p>{work.repository_identity} · {runtimeLabel(work.required_runtime)}{currentStage?.model ? ` / ${currentStage.model}` : ""} · {work.started_at ? `started ${timeAgo(work.started_at)}` : `admitted ${timeAgo(work.admitted_at)}`}</p>
      </div>
      <div className="detail-actions">
        <StatusBadge state={work.state} />
        {active && <button className="button button-danger-secondary" disabled={cancel.isPending} onClick={() => cancel.mutate()}><StopCircle size={14} /> Cancel</button>}
        {retryable && <button className="button button-secondary" disabled={retry.isPending} onClick={() => retry.mutate()}><RotateCcw size={14} /> Retry</button>}
      </div>
    </div>
    <InlineError error={cancel.error ?? retry.error} />

    <div className="work-tabs" role="tablist" aria-label="Work detail">
      {tabs.map((entry) => <button key={entry.key} role="tab" aria-selected={tab === entry.key} onClick={() => setTab(entry.key)}>
        {entry.label}{entry.key === "stages" && stages.length ? <span className="n">{stages.length}</span> : null}
      </button>)}
    </div>

    {tab === "brief" && <BriefTab detail={detail} onRun={onRun} onWork={onWork} />}
    {tab === "stages" && <StagesTab detail={detail} />}
    {tab === "outcome" && <OutcomeTab detail={detail} />}
    {tab === "evidence" && <EvidenceTab detail={detail} />}
  </div>;
}

function BriefTab({ detail, onRun, onWork }: {
  detail: WorkDetail;
  onRun: (runID: string) => void;
  onWork: (workID: string) => void;
}) {
  const work = detail.work;
  const stages = work.stages ?? [];
  const completed = stages.filter((stage) => stage.state === "succeeded").length;
  const currentStage = stages.find((stage) => stage.state === "running");
  const siblings = detail.siblings ?? [];
  return <>
    <section className="run-summary-strip">
      <div><span>Stage</span><strong>{stages.length ? `${completed} / ${stages.length}${currentStage ? ` · ${currentStage.name}` : ""}` : "No stages"}</strong></div>
      <div><span>Worker</span><strong className="strip-name" title={work.assigned_worker_id}>{detail.worker_name || workerLabel(work) || "Unassigned"}</strong></div>
      <div><span>Elapsed</span><strong>{work.started_at ? duration(work.started_at, work.terminal_at) : "Not started"}</strong></div>
      <div><span>{detail.cost.total_usd === undefined ? "Cost" : "Cost so far"}</span><strong>{costTotalLabel(detail)}</strong></div>
    </section>

    {work.blocked_reason && <p className="work-blocked"><AlertTriangle size={14} /> {work.blocked_reason}</p>}
    {work.failure_reason && <p className="work-blocked"><AlertTriangle size={14} /> {work.failure_reason}</p>}

    <div className={`work-brief-grid ${detail.brief ? "" : "no-brief"}`}>
      {/* Factory never manufactures a brief. Work admitted without one shows
          the operational facts alone rather than an invented summary. */}
      {detail.brief && <section className="panel">
        <div className="panel-heading"><h2>Brief</h2><span>from {detail.source}</span></div>
        <dl className="metadata">
          {detail.brief.context && <div><dt>Context</dt><dd>{detail.brief.context}</dd></div>}
          {detail.brief.why && <div><dt>Why</dt><dd>{detail.brief.why}</dd></div>}
          {detail.brief.risk && <div><dt>Risk</dt><dd className="risk">{detail.brief.risk}</dd></div>}
          {detail.brief.work && <div><dt>Work</dt><dd>{detail.brief.work}</dd></div>}
        </dl>
      </section>}
      <section className="panel">
        <div className="panel-heading"><h2>Where it stands</h2></div>
        <dl className="metadata">
          <div><dt>Repository</dt><dd>{work.repository_identity}</dd></div>
          <div><dt>Run</dt><dd><button className="link-button" onClick={() => onRun(detail.run_id)}>{detail.run_id.slice(0, 8)}</button>{siblings.length ? ` · ${siblings.length + 1} Work items` : ""}</dd></div>
          {siblings.length > 0 && <div><dt>Siblings</dt><dd className="sibling-list">{siblings.map((sibling) => <button key={sibling.id} className="link-button" onClick={() => onWork(sibling.id)}>{repositoryName(sibling.repository_identity)}</button>)}</dd></div>}
          {detail.pipeline && <div><dt>Pipeline</dt><dd>{detail.pipeline.name}</dd></div>}
          <div><dt>Runtime</dt><dd>{runtimeLabel(work.required_runtime)}</dd></div>
          <div><dt>Attempts</dt><dd>{work.attempts?.length ?? 0}</dd></div>
          <div><dt>Admitted</dt><dd>{timeAgo(work.admitted_at)}</dd></div>
        </dl>
      </section>
    </div>
  </>;
}

// costTotalLabel never prints $0.00 for a runtime that reported nothing, and
// says so when a total omits stages that did not report.
function costTotalLabel(detail: WorkDetail): string {
  if (detail.cost.total_usd === undefined) return "Unavailable";
  const total = `$${detail.cost.total_usd.toFixed(2)}`;
  return detail.cost.unavailable_stages ? `${total} + ${detail.cost.unavailable_stages} unavailable` : total;
}

const stageStateMark: Record<StageRun["state"], string> = {
  succeeded: "✓", running: "●", failed: "✕", cancelled: "⊘", pending: "○",
};

// The graph carries a word and a mark for every state, not colour alone.
function StagesTab({ detail }: { detail: WorkDetail }) {
  const stages = detail.work.stages ?? [];
  const handoffs = detail.handoffs ?? [];
  const [openEdge, setOpenEdge] = useState<number | null>(null);
  if (!stages.length) return <p className="quiet-empty">This Work has no recorded pipeline stages.</p>;
  const handoff = handoffs.find((edge) => edge.from_stage === openEdge);
  return <>
    <div className="stage-graph" role="list" aria-label="Pipeline stages">
      {stages.map((stage, index) => {
        const edge = handoffs.find((candidate) => candidate.from_stage === stage.position);
        return <div className="stage-graph-pair" key={stage.position}>
          <div className={`stage-node stage-node-${stage.state}`} role="listitem">
            <div className="stage-node-head"><span className="pos">{index + 1}</span><strong>{stage.name}</strong><span className="kind">{stage.kind ?? "agent"}</span></div>
            <span className="stage-node-state">{stageStateMark[stage.state]} {stateWord(stage)}</span>
            <dl>
              {stage.command && <div><dt>command</dt><dd>{stage.command}</dd></div>}
              {stage.model && <div><dt>model</dt><dd>{stage.model}{stage.effort ? ` · ${stage.effort}` : ""}</dd></div>}
              {stage.review_verdict && <div><dt>verdict</dt><dd>{stage.review_verdict}</dd></div>}
              <div><dt>took</dt><dd>{stage.started_at ? duration(stage.started_at, stage.completed_at) : "—"}</dd></div>
              {stageTokens(stage) > 0 && <div><dt>tokens</dt><dd>{stageTokens(stage).toLocaleString()}</dd></div>}
              <div><dt>cost</dt><dd>{stageCostWord(stage)}</dd></div>
            </dl>
          </div>
          {edge && <div className={`handoff-edge ${edge.delivered ? "carried" : ""}`}>
            <button onClick={() => setOpenEdge(openEdge === edge.from_stage ? null : edge.from_stage)}
              aria-expanded={openEdge === edge.from_stage}
              aria-label={edge.delivered ? `View evidence passed from ${stage.name}` : `No evidence passed from ${stage.name}`}>
              <small>{edge.delivered ? "evidence" : "not sent"}</small>
              <span className="wire" aria-hidden="true"><ArrowRight size={11} /></span>
              <small>{edge.delivered ? `${byteLabel(edge.summary)}${edge.truncated ? " ·" : ""}` : "—"}</small>
            </button>
          </div>}
        </div>;
      })}
    </div>
    {/* "Stage handoff", never "conversation": stages share a worktree and a
        bounded evidence hand-off, not a message channel. */}
    {handoff && <section className="handoff-panel">
      <header>
        <strong>Stage handoff</strong>
        <span className="tag">{stageName(stages, handoff.from_stage)} → {stageName(stages, handoff.to_stage)}</span>
        <span className="src">{handoff.kind.replace("-", " ")}</span>
      </header>
      <div className="handoff-body">
        {handoff.delivered
          ? <pre>{handoff.summary || "The stage recorded no result text."}</pre>
          : <p className="quiet-empty">Stage {handoff.from_stage + 1} did not complete, so no evidence reached the next stage.</p>}
        <p className="handoff-bound">
          {handoff.delivered ? `${byteLabel(handoff.summary)}${handoff.truncated ? " · truncated at the handoff bound" : " · not truncated"}` : `Predecessor state: ${handoff.from_state}`}
          <span> · Full stage output is under Evidence.</span>
        </p>
      </div>
    </section>}
  </>;
}

function stageName(stages: StageRun[], position: number): string {
  return stages.find((stage) => stage.position === position)?.name ?? `Stage ${position + 1}`;
}

function stateWord(stage: StageRun): string {
  if (stage.state === "pending") return "Not started";
  return stage.state.charAt(0).toUpperCase() + stage.state.slice(1);
}

// A code or delivery stage reaches no model, so it cannot have a cost. That is
// "n/a" and is a different fact from a runtime that ran and reported nothing.
function stageCostWord(stage: StageRun): string {
  if (stage.cost_usd !== undefined) return `$${stage.cost_usd.toFixed(2)}`;
  if (stage.kind === "code" || stage.kind === "delivery") return "n/a";
  return "unavailable";
}

function OutcomeTab({ detail }: { detail: WorkDetail }) {
  const work = detail.work;
  const verification = detail.verification;
  const verdicts = (work.stages ?? []).map((stage) => stage.review_verdict).filter(Boolean);
  return <>
    <section className="panel">
      <div className="panel-heading"><h2>Outcome</h2><StatusBadge state={work.state} /></div>
      <div className="outcome-facts">
        <div><span>Result</span><strong>{work.terminal_at ? stateWordFor(work.state) : "In progress"}</strong></div>
        <div><span>Review</span><strong>{verdicts.length ? verdicts.join(", ") : "No verdict recorded"}</strong></div>
        <div><span>Attempts</span><strong>{work.attempts?.length ?? 0}</strong></div>
        <div><span>Queue time</span><strong>{work.started_at ? duration(work.admitted_at, work.started_at) : "Waiting"}</strong></div>
        <div><span>Execution</span><strong>{work.started_at ? duration(work.started_at, work.terminal_at) : "Not started"}</strong></div>
        <div><span>Reported cost</span><strong>{costTotalLabel(detail)}</strong></div>
      </div>
      {work.pull_request_url && <p className="outcome-delivery">Delivered: <a href={work.pull_request_url} target="_blank" rel="noreferrer">{work.pull_request_url}</a></p>}
      {work.failure_reason && <p className="work-blocked"><AlertTriangle size={14} /> {work.failure_reason}</p>}
    </section>

    <section className="panel">
      <div className="panel-heading">
        <h2>Verification</h2>
        <span>{verification.recorded_checks ? `${verification.recorded_checks} recorded · ${verification.passed} passed${verification.failed ? ` · ${verification.failed} failed` : ""}` : "No recorded checks"}</span>
      </div>
      {verification.items?.length
        ? <><div className="verify-list">
            {/* Keyed on the source and position too: a code stage and an agent's
                report can name the same command with the same state, and a
                name-and-state key collides there, so React reconciles the two
                rows into one and a poll can leave a stale one on screen. */}
            {verification.items.map((check, index) => <VerifyRow key={`${check.source}-${check.name}-${index}`} check={check} />)}
          </div>
          {/* Checks, never tests: Factory holds exit statuses, not test counts. */}
          <p className="verify-note">Counts are of verification commands Factory ran, not of test cases. A row marked agent-reported is the agent’s own claim.</p></>
        : <p className="quiet-empty">This pipeline recorded no code stages, so Factory ran no verification of its own.</p>}
    </section>

    <CostPanel detail={detail} />
  </>;
}

function stateWordFor(state: string): string {
  return state.charAt(0).toUpperCase() + state.slice(1).replace(/-/g, " ");
}

function VerifyRow({ check }: { check: VerificationCheck }) {
  const mark = check.state === "passed" ? "✓" : check.state === "failed" ? "✕" : "◦";
  return <div className={`verify-row verify-${check.state}`}>
    <span className="mark" aria-hidden="true">{mark}</span>
    <span className="name" title={check.name}>{check.name}</span>
    <span className="src">{check.source.replace("-", " ")}</span>
    <span className="st">{check.detail || check.state}</span>
  </div>;
}

function CostPanel({ detail }: { detail: WorkDetail }) {
  const byStage = detail.cost.by_stage ?? [];
  const byAttempt = detail.cost.by_attempt ?? [];
  if (!byStage.length && !byAttempt.length) return null;
  return <section className="panel">
    <div className="panel-heading"><h2>Reported cost</h2><span>{byAttempt.length} attempt{byAttempt.length === 1 ? "" : "s"}</span></div>
    <div className="cost-split">
      <div className="cost-row head"><span>Stage</span><span className="n">Tokens</span><span className="n">Cost</span></div>
      {byStage.map((stage) => <StageCostRow key={stage.position} stage={stage} />)}
      <div className="cost-row total">
        <span>Total across stages</span>
        <span className="n">{totalTokens(byStage) ? totalTokens(byStage).toLocaleString() : "—"}</span>
        <span className={detail.cost.total_usd === undefined ? "n na" : "n"}>{detail.cost.total_usd === undefined ? "unavailable" : `$${detail.cost.total_usd.toFixed(2)}`}</span>
      </div>
    </div>
    {detail.cost.unavailable_stages > 0 && <p className="verify-note">{detail.cost.unavailable_stages} stage{detail.cost.unavailable_stages === 1 ? "" : "s"} ran a model and reported no cost, so this total is partial.</p>}
    {byAttempt.length > 1 && <div className="cost-split cost-attempts">
      <div className="cost-row head"><span>Attempt</span><span className="n">State</span><span className="n">Cost</span></div>
      {byAttempt.map((attempt) => <div className="cost-row" key={attempt.attempt_number}>
        <span>Attempt {attempt.attempt_number}</span>
        <span className="n">{attempt.state}</span>
        <span className={attempt.cost_usd === undefined ? "n na" : "n"}>{attempt.cost_usd === undefined ? "unavailable" : `$${attempt.cost_usd.toFixed(2)}`}</span>
      </div>)}
    </div>}
  </section>;
}

function StageCostRow({ stage }: { stage: StageCost }) {
  const tokens = stageTokens(stage);
  const unavailable = stage.cost_usd === undefined;
  const notApplicable = unavailable && (stage.kind === "code" || stage.kind === "delivery");
  return <div className="cost-row">
    <span>{stage.position + 1} · {stage.name}{stage.model ? ` — ${stage.model}` : notApplicable ? ` — ${stage.kind} stage` : ""}</span>
    <span className="n">{tokens ? tokens.toLocaleString() : "—"}</span>
    <span className={unavailable ? "n na" : "n"}>{unavailable ? (notApplicable ? "n/a" : "unavailable") : `$${stage.cost_usd!.toFixed(2)}`}</span>
  </div>;
}

function totalTokens(stages: StageCost[]): number {
  return stages.reduce((sum, stage) => sum + stageTokens(stage), 0);
}

// Evidence is where raw output lives. Every section is collapsed, so opening a
// Work item never begins with a wall of logs, and nothing is deleted to
// achieve that.
function EvidenceTab({ detail }: { detail: WorkDetail }) {
  const work = detail.work;
  const stages = work.stages ?? [];
  const attempts = work.attempts ?? [];
  const updates = work.updates ?? [];
  return <div className="evidence-list">
    {stages.map((stage) => (stage.result || stage.error) && <details className="prompt-panel" key={`result-${stage.position}`}>
      <summary>Stage {stage.position + 1} · {stage.name} — raw result</summary>
      {stage.error && <pre className="evidence-error">{stage.error}</pre>}
      {stage.result && <pre>{stage.result}</pre>}
    </details>)}
    {stages.map((stage) => stage.prompt && <details className="prompt-panel" key={`prompt-${stage.position}`}>
      <summary>Stage {stage.position + 1} · {stage.name} — frozen prompt</summary>
      <pre>{stage.prompt}</pre>
    </details>)}
    {detail.task_prompt && <details className="prompt-panel">
      <summary>Task snapshot</summary><pre>{detail.task_prompt}</pre>
    </details>}
    {work.resolved_prompt && <details className="prompt-panel">
      <summary>Resolved prompt</summary><pre>{work.resolved_prompt}</pre>
    </details>}
    {updates.length > 0 && <details className="prompt-panel">
      <summary>Agent updates <span className="n">{updates.length}</span></summary>
      <ol className="update-list">{updates.map((update, index) => <li key={index}>
        <span><strong>{update.status}</strong><small>{update.actor} · {timeAgo(update.accepted_at)}</small></span>
        <p>{update.message}</p>
      </li>)}</ol>
    </details>}
    {attempts.map((attempt: Attempt) => <details className="prompt-panel" key={attempt.id}>
      <summary>Attempt {attempt.attempt_number} · runtime events <span className="n">{attempt.state}</span></summary>
      <div className="evidence-attempt"><AttemptStream attempt={attempt} /></div>
    </details>)}
    <details className="prompt-panel">
      <summary>Identifiers and timestamps</summary>
      <dl className="metadata">
        <div><dt>Work</dt><dd className="mono">{work.id}</dd></div>
        <div><dt>Run</dt><dd className="mono">{detail.run_id}</dd></div>
        <div><dt>Task</dt><dd className="mono">{detail.task_id}</dd></div>
        <div><dt>Repository</dt><dd className="mono">{work.repository_id}</dd></div>
        <div><dt>Admitted</dt><dd className="mono">{work.admitted_at}</dd></div>
        {work.started_at && <div><dt>Started</dt><dd className="mono">{work.started_at}</dd></div>}
        {work.terminal_at && <div><dt>Terminal</dt><dd className="mono">{work.terminal_at}</dd></div>}
        <div><dt>Updated</dt><dd className="mono">{detail.updated_at}</dd></div>
      </dl>
    </details>
    {!stages.length && !attempts.length && <p className="quiet-empty">No evidence has been recorded yet.</p>}
  </div>;
}
