import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Clock3, Coins, EyeOff, Gauge, Play, Timer, TrendingUp, Users } from "lucide-react";
import type { ReactNode } from "react";
import { api } from "./api";
import { duration, timeAgo, timeUntil } from "./format";
import type { OverviewCost } from "./types";
import { ErrorState, LoadingState, StatusBadge, ViewHeader } from "./ui";

export function OverviewView({ onRun, onTask, onWork }: { onRun: (id: string) => void; onTask: (id: string) => void; onWork: (id: string) => void }) {
  const query = useQuery({ queryKey: ["overview"], queryFn: api.overview, refetchInterval: 10_000 });
  if (query.isPending) return <LoadingState label="Loading overview" />;
  if (query.isError || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const overview = query.data;
  return <div className="page task-overview">
    <ViewHeader title="Overview" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    <section className="overview-facts" aria-label="Factory status">
      <Fact icon={<Play size={15} />} label="Active runs" value={overview.active_runs} />
      <Fact icon={<AlertTriangle size={15} />} label="Needs attention" value={overview.needs_attention} tone={overview.needs_attention ? "danger" : undefined} />
      <Fact icon={<CheckCircle2 size={15} />} label="Completed · 24h" value={overview.completed_last_24h} />
      <Fact icon={<Users size={15} />} label="Workers online" value={`${overview.workers_online}/${overview.workers_total}`} />
    </section>
    <CostPanel cost={overview.cost} onWork={onWork} />
    <section aria-labelledby="run-performance-title">
      <div className="overview-section-heading">
        <div><span className="eyebrow">LAST 24 HOURS</span><h2 id="run-performance-title">Run performance</h2></div>
        <span>{overview.run_metrics.completed_runs} completed</span>
      </div>
      <div className="overview-facts overview-run-metrics">
        <Fact icon={<Gauge size={15} />} label="Runs" value={overview.run_metrics.total_runs} />
        <Fact icon={<CheckCircle2 size={15} />} label="Completion rate" value={formatRate(overview.run_metrics.completion_rate)} />
        <Fact icon={<Clock3 size={15} />} label="Average queue time" value={formatSeconds(overview.run_metrics.average_queue_time_seconds)} />
        <Fact icon={<Timer size={15} />} label="Average cycle time" value={formatSeconds(overview.run_metrics.average_cycle_time_seconds)} />
      </div>
    </section>
    <div className="overview-columns">
      <section className="panel clean-panel">
        <div className="panel-heading"><div><span className="eyebrow">RECENT</span><h2>Runs</h2></div><span>{overview.recent_runs?.length ?? 0}</span></div>
        <div className="overview-list">
          {(overview.recent_runs ?? []).map((run) => <button key={run.id} className="overview-row" onClick={() => onRun(run.id)}>
            <span className="overview-row-main"><strong>{run.task.name}</strong><small>{run.source} · {run.session_count} session{run.session_count === 1 ? "" : "s"}</small></span>
            <span className="overview-row-meta"><StatusBadge state={run.state} /><small>{run.terminal_at ? duration(run.admitted_at, run.terminal_at) : timeAgo(run.admitted_at)}</small></span>
          </button>)}
          {!overview.recent_runs?.length && <p className="quiet-empty">Run a Task to see Runs here.</p>}
        </div>
      </section>
      <section className="panel clean-panel">
        <div className="panel-heading"><div><span className="eyebrow">NEXT</span><h2>Scheduled Tasks</h2></div><Clock3 size={15} /></div>
        <div className="overview-list">
          {(overview.upcoming_tasks ?? []).map((task) => <button key={task.id} className="overview-row" onClick={() => onTask(task.id)}>
            <span className="overview-row-main"><strong>{task.name}</strong><small>{task.schedule.cron} · {task.schedule.timezone}</small></span>
            <span className="overview-row-meta"><StatusBadge state={task.schedule.health_status} /><small>{task.schedule.next_due_at ? timeUntil(task.schedule.next_due_at) : "Not scheduled"}</small></span>
          </button>)}
          {!overview.upcoming_tasks?.length && <p className="quiet-empty">No scheduled Tasks.</p>}
        </div>
      </section>
    </div>
  </div>;
}

function Fact({ icon, label, value, tone }: { icon: ReactNode; label: string; value: string | number; tone?: string }) {
  return <div className={`overview-fact ${tone ?? ""}`}><span>{icon}{label}</span><strong>{value}</strong></div>;
}

function money(value: number | undefined): string {
  return value === undefined ? "Unavailable" : `$${value.toFixed(2)}`;
}

// The panel answers four questions: what has this cost in total, what is it
// costing lately, what does one Work item cost, and how much of that Factory
// cannot see. Every figure but the trailing window is lifetime, because a
// day-scoped total resets before an operator has necessarily looked at it.
function CostPanel({ cost, onWork }: { cost: OverviewCost; onWork: (id: string) => void }) {
  const byModel = cost.by_model ?? [];
  const total = cost.measured_work + cost.unavailable_work;
  if (!total) return null;
  return <section aria-labelledby="cost-title">
    <div className="overview-section-heading">
      <div><span className="eyebrow">ALL WORK</span><h2 id="cost-title">Reported cost</h2></div>
      <span>{cost.measured_work} of {total} measured</span>
    </div>
    <div className="overview-facts overview-run-metrics">
      <Fact icon={<Gauge size={15} />} label="Total" value={money(cost.total_usd)} />
      <Fact icon={<TrendingUp size={15} />} label={`Last ${cost.recent_days} days`} value={money(cost.recent_usd)} />
      <Fact icon={<Coins size={15} />} label="Average per Work" value={money(cost.average_usd)} />
      <Fact icon={<EyeOff size={15} />} label="Cost unavailable" value={cost.unavailable_work} />
    </div>
    {cost.highest_work_id && <p className="cost-note">
      Dearest Work: <button className="link-button" onClick={() => onWork(cost.highest_work_id!)}>{cost.highest_work_name || cost.highest_work_id.slice(0, 8)}</button> at {money(cost.highest_usd)}
    </p>}
    {byModel.length > 0 && <div className="cost-split cost-by-model">
      <div className="cost-row head"><span>Model</span><span className="n">Attempts</span><span className="n">Cost</span></div>
      {byModel.map((entry) => <div className="cost-row" key={entry.model}>
        <span>{entry.model}</span><span className="n">{entry.attempts}</span><span className="n">${entry.cost_usd.toFixed(2)}</span>
      </div>)}
    </div>}
    {cost.unavailable_work > 0 && <p className="cost-note">{cost.unavailable_work} terminal Work item{cost.unavailable_work === 1 ? "" : "s"} ran on a runtime that reports no cost, so these figures are partial.</p>}
  </section>;
}

function formatRate(value: number | null): string {
  if (value === null) return "No data";
  return new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 1 }).format(value);
}

function formatSeconds(value: number | null): string {
  if (value === null) return "No data";
  const seconds = Math.max(0, Math.round(value));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
