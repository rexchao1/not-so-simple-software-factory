import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, RotateCcw, StopCircle } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { duration, eventSummary, timeAgo } from "./format";
import type { Attempt, AttemptEvent, Run, Session } from "./types";
import { ErrorState, InlineError, LoadingState, StatusBadge } from "./ui";

// successfulSessions counts every outcome that finished the work, not only
// the plain succeeded one: a delivered pull request and a no-change result are
// both completions.
function successfulSessions(run: Run): number {
  return run.succeeded_count + run.ready_count + run.no_change_count;
}

export function RunDetailView({ id, onBack }: { id: string; onBack: () => void }) {
  const client = useQueryClient();
  const query = useQuery({ queryKey: ["runs", id], queryFn: () => api.run(id), refetchInterval: 3_000 });
  const cancel = useMutation({ mutationFn: () => api.cancelRun(id), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  const retry = useMutation({ mutationFn: (sessionId: string) => api.retrySession(id, sessionId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  const cancelSession = useMutation({ mutationFn: (sessionId: string) => api.cancelSession(id, sessionId), onSuccess: () => { void query.refetch(); void client.invalidateQueries({ queryKey: ["runs"] }); } });
  if (query.isPending) return <LoadingState label="Loading Run" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const { run, sessions } = query.data;
  const execution = run.execution.backend === "persistent"
    ? "Automatic persistent Worker"
    : `Cloud Run · ${run.execution.provider} / ${run.execution.model}`;
  return <div className="page run-detail-clean">
    <button className="back-button" onClick={onBack}><ArrowLeft size={14} /> Work</button>
    <div className="detail-heading run-detail-heading"><div><span className="eyebrow">{run.source.replace("_", " ")} · {run.id.slice(0, 8)}</span><h1>{run.task.name}</h1><p>{run.session_count} repository session{run.session_count === 1 ? "" : "s"} · {execution} · started {timeAgo(run.admitted_at)}</p></div><div className="detail-actions"><StatusBadge state={run.state} />{run.active_count > 0 && <button className="button button-danger-secondary" disabled={cancel.isPending} onClick={() => cancel.mutate()}><StopCircle size={14} /> Cancel</button>}</div></div>
    <InlineError error={cancel.error ?? retry.error ?? cancelSession.error} />
    {run.brief && <section className="ticket-brief panel" aria-label="Ticket brief"><div className="panel-heading"><h2>Brief</h2></div><dl className="metadata">{run.brief.context && <div><dt>Context</dt><dd>{run.brief.context}</dd></div>}{run.brief.why && <div><dt>Why</dt><dd>{run.brief.why}</dd></div>}{run.brief.risk && <div><dt>Risk</dt><dd>{run.brief.risk}</dd></div>}{run.brief.work && <div><dt>Work</dt><dd>{run.brief.work}</dd></div>}</dl></section>}
    <section className="run-summary-strip"><div><span>Pipeline</span><strong>{run.task.pipeline?.name ?? "Single agent"}</strong></div><div><span>Stages</span><strong>{run.task.pipeline?.stages.length ?? 1}</strong></div><div><span>Completed</span><strong>{successfulSessions(run)}</strong></div><div><span>Duration</span><strong>{run.terminal_at ? duration(run.admitted_at, run.terminal_at) : "Active"}</strong></div></section>
    {run.terminal_at && <OutcomePanel run={run} sessions={sessions ?? []} />}
    <section className="panel session-panel"><div className="panel-heading"><h2>Sessions</h2><span>{sessions?.length ?? 0}</span></div>{(sessions ?? []).map((session) => <SessionRow key={session.id} session={session} onRetry={() => retry.mutate(session.id)} onCancel={() => cancelSession.mutate(session.id)} />)}</section>
    <details className="prompt-panel"><summary>Task snapshot</summary><pre>{run.task.prompt}</pre></details>
  </div>;
}

function OutcomePanel({ run, sessions }: { run: Run; sessions: Session[] }) {
  const attempts = sessions.flatMap((session) => session.attempts ?? []);
  const cost = attempts.reduce((sum, attempt) => sum + (attempt.cost_usd ?? 0), 0);
  const checks = sessions.flatMap((session) => session.stages ?? []).filter((stage) => stage.kind === "code");
  const passedChecks = checks.filter((stage) => stage.state === "succeeded").length;
  const failedChecks = checks.filter((stage) => stage.state === "failed").length;
  const verdicts = sessions.flatMap((session) => session.stages ?? []).map((stage) => stage.review_verdict).filter(Boolean);
  return <section className="outcome-panel panel" aria-label="Outcome"><div className="panel-heading"><h2>Outcome</h2><StatusBadge state={run.state} /></div><div className="outcome-facts"><div><span>Result</span><strong>{successfulSessions(run)}/{run.session_count} completed</strong></div><div><span>Verification</span><strong>{checks.length ? `${passedChecks} passed${failedChecks ? ` · ${failedChecks} failed` : ""}` : "No recorded checks"}</strong></div><div><span>Review</span><strong>{verdicts.length ? verdicts.join(", ") : "No verdict recorded"}</strong></div><div><span>Attempts</span><strong>{attempts.length}</strong></div>{cost > 0 && <div><span>Reported cost</span><strong>${cost.toFixed(2)}</strong></div>}</div></section>;
}

function stageTokens(stage: NonNullable<Session["stages"]>[number]) {
  const usage = stage.usage;
  if (!usage) return 0;
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) +
    (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
}

function SessionRow({ session, onRetry, onCancel }: { session: Session; onRetry: () => void; onCancel: () => void }) {
  const totalCost = (session.attempts ?? []).reduce((sum, attempt) => sum + (attempt.cost_usd ?? 0), 0);
  const activeStage = (session.stages ?? []).find((stage) => stage.state === "running");
  const active = ["blocked", "queued", "preparing", "running"].includes(session.state);
  const [open, setOpen] = useState(false);
  return <details className="session-row" onToggle={(event) => setOpen(event.currentTarget.open)}><summary><span className="session-repo"><strong>{session.repository_identity}</strong><small>{session.assigned_worker_id ? `Worker ${session.assigned_worker_id}${activeStage ? ` · ${activeStage.name}` : ""}` : session.blocked_reason ?? "Waiting for a Worker"}</small></span><StatusBadge state={session.state} /><span className="session-time">{session.terminal_at && session.started_at ? duration(session.started_at, session.terminal_at) : session.started_at ? "Active" : "Waiting"}</span></summary>{open && <div className="session-detail-body">{session.failure_reason && <p className="session-failure"><AlertTriangle size={14} /> {session.failure_reason}</p>}<section className="stage-view" aria-label="Pipeline stages"><div className="panel-heading"><h3>Stages</h3>{activeStage && <span>Working now: {activeStage.name}</span>}</div><div className="stage-run-list">{(session.stages ?? []).map((stage, index) => <div className={`stage-run stage-run-${stage.state}`} key={stage.position}><span>{index + 1}</span><div><strong>{stage.name}</strong><small>{stage.state}{stage.kind && stage.kind !== "agent" ? ` · ${stage.kind}` : ""}{stage.model ? ` · ${stage.model}` : ""}{stage.effort ? ` · ${stage.effort}` : ""}{stageTokens(stage) ? ` · ${stageTokens(stage).toLocaleString()} tokens` : ""}{stage.started_at && stage.completed_at ? ` · ${duration(stage.started_at, stage.completed_at)}` : ""}</small></div><StatusBadge state={stage.state} /></div>)}</div></section>{totalCost > 0 && <p className="session-cost">Reported agent cost: <strong>${totalCost.toFixed(2)}</strong></p>}{session.result && <pre>{session.result}</pre>}{(session.attempts ?? []).map((attempt) => <AttemptStream key={attempt.id} attempt={attempt} />)}<div className="session-detail-actions"><span>{session.attempts?.length ?? 0} attempt{session.attempts?.length === 1 ? "" : "s"}</span>{active && <button className="button button-danger-secondary" onClick={onCancel}><StopCircle size={14} /> Cancel session</button>}{(session.state === "failed" || session.state === "cancelled") && <button className="button button-secondary" onClick={onRetry}><RotateCcw size={14} /> Retry session</button>}</div></div>}</details>;
}

export function AttemptStream({ attempt }: { attempt: Attempt }) {
  const active = attempt.state === "preparing" || attempt.state === "running";
  const client = useQueryClient();
  const eventKey = ["attempt-events", attempt.id] as const;
  const query = useQuery({
    queryKey: eventKey,
    queryFn: () => loadAttemptEvents(attempt.id, client.getQueryData<AttemptEvent[]>(eventKey) ?? []),
    refetchInterval: active ? 2_000 : false,
  });
  const wasActive = useRef(active);
  const refetch = query.refetch;
  useEffect(() => {
    const becameTerminal = wasActive.current && !active;
    wasActive.current = active;
    if (becameTerminal) void refetch();
  }, [active, refetch]);
  const events = (query.data ?? []).flatMap((event) => {
    const summary = eventSummary(event);
    return summary ? [{ event, summary }] : [];
  });
  return <section className="attempt-stream"><header><strong>Attempt {attempt.attempt_number}</strong><StatusBadge state={attempt.state} /></header>{query.error && <InlineError error={query.error} />}{query.isPending ? <p className="quiet-empty">Loading attempt output…</p> : events.length === 0 ? <p className="quiet-empty">No runtime output recorded.</p> : <ol className="attempt-events">{events.map(({ event, summary }) => <li key={event.sequence}><span><strong>{summary.label}</strong><time dateTime={event.server_time}>{new Date(event.server_time).toLocaleTimeString()}</time></span><p>{summary.text}</p></li>)}</ol>}</section>;
}

async function loadAttemptEvents(attemptID: string, current: AttemptEvent[]): Promise<AttemptEvent[]> {
  const events = [...current];
  let after = events.at(-1)?.sequence ?? -1;
  for (;;) {
    const requestedAfter = after;
    const page = await api.events(attemptID, after);
    for (const event of page.events) {
      if (event.sequence > after) {
        events.push(event);
        after = event.sequence;
      }
    }
    if (!page.has_more) return events;
    if (page.next_after <= requestedAfter) throw new Error("Attempt event pagination did not advance.");
    after = page.next_after;
  }
}
