import { useInfiniteQuery } from "@tanstack/react-query";
import { AlertCircle, Bot, CheckCircle2, CircleDot, Clock3, Columns3, GitBranch, GitMerge, Rows3, TerminalSquare } from "lucide-react";
import { useState } from "react";
import { api } from "./api";
import { runtimeLabel, timeAgo } from "./format";
import type { WorkItem } from "./types";
import { boardColumns, costLabel, costSummary, repositoryName, repositoryTabs, stageLabel, workerLabel, workInColumn } from "./work-format";
import { EmptyState, ErrorState, LoadingState, StaleBanner, StatusBadge, ViewHeader } from "./ui";

export type WorkViewMode = "table" | "board";

// A Run can span several repositories, so the board lists Work items rather
// than Runs: filtering to one repository must not leave a card that describes
// work in two others.
export function WorkView({ mode, onMode, onWork }: {
  mode: WorkViewMode;
  onMode: (mode: WorkViewMode) => void;
  onWork: (id: string) => void;
}) {
  const [repositoryFilter, setRepositoryFilter] = useState("all");
  // Hidden by default: the board is for build work, and a planning pass has
  // its own, more detailed page. Fetched as "all" regardless, so toggling the
  // control is a client-side filter rather than a second round trip, and the
  // toolbar can always show how many are hidden.
  const [showPlanning, setShowPlanning] = useState(false);
  // Paged rather than fetched whole: the board must stay bounded however much
  // Work the factory has run.
  //
  // A refetch of an infinite query refetches every page it holds, so polling
  // while ten pages are loaded would issue ten requests every five seconds and
  // grow with each Load more. The newest page is the live one; older Work
  // rarely changes. So the poll runs while the board shows only that page, and
  // once history is loaded the operator is browsing rather than monitoring and
  // refreshes on demand. ViewHeader always shows how stale the view is.
  const query = useInfiniteQuery({
    queryKey: ["work"],
    queryFn: ({ pageParam }) => api.work({ cursor: pageParam, limit: 100, planning: "all" }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    refetchInterval: (value) => ((value.state.data?.pages.length ?? 1) > 1 ? false : 5_000),
  });
  if (query.isPending) return <LoadingState label="Loading Work" />;
  if (query.isError && !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  const fetched = (query.data?.pages ?? []).flatMap((page) => page.work);
  const planningCount = fetched.filter((item) => item.planning).length;
  const all = showPlanning ? fetched : fetched.filter((item) => !item.planning);
  const repositories = repositoryTabs(all);
  const items = repositoryFilter === "all" ? all : all.filter((item) => item.repository_id === repositoryFilter);
  const summary = costSummary(items);
  const paused = (query.data?.pages.length ?? 1) > 1;
  return <div className="page page-run">
    <ViewHeader title="Work" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    {query.isError && <StaleBanner error={query.error} />}
    <div className="view-toolbar"><p>Every repository’s work, from queue to completion.</p><ViewSwitch mode={mode} onMode={onMode} /></div>
    <div className="work-toolbar">
      {repositories.length > 1 && <div className="repository-tabs" role="tablist" aria-label="Repository">
        <button role="tab" aria-selected={repositoryFilter === "all"} onClick={() => setRepositoryFilter("all")}>All repositories <b>{all.length}</b></button>
        {repositories.map((repository) => <button key={repository.id} role="tab" aria-selected={repositoryFilter === repository.id} onClick={() => setRepositoryFilter(repository.id)}>
          {repositoryName(repository.identity)} <b>{repository.count}</b>
        </button>)}
      </div>}
      {planningCount > 0 && <button className="work-planning-toggle" aria-pressed={showPlanning} onClick={() => setShowPlanning((value) => !value)}>
        {showPlanning ? "Hide planning" : "Show planning"} <b>{planningCount}</b>
      </button>}
      {summary && <span className="work-cost-summary">{summary}</span>}
    </div>
    {!items.length
      ? <EmptyState icon={<Rows3 size={22} />} title="No work yet" description="Run a Task now or wait for its next schedule." />
      : mode === "table" ? <WorkTable items={items} onWork={onWork} /> : <WorkBoard items={items} onWork={onWork} />}
    {(query.hasNextPage || paused) && <div className="load-more-row">
      {query.hasNextPage && <button className="button button-secondary" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
        {query.isFetchingNextPage ? "Loading…" : "Load more Work"}
      </button>}
      {paused && <p className="poll-paused">Showing history. Live updates are paused; use Refresh above.</p>}
    </div>}
  </div>;
}

function BoardIcon({ column }: { column: string }) {
  if (column === "attention") return <AlertCircle size={15} />;
  if (column === "running") return <CircleDot size={15} />;
  if (column === "queued") return <Clock3 size={15} />;
  return <CheckCircle2 size={15} />;
}

function WorkBoard({ items, onWork }: { items: WorkItem[]; onWork: (id: string) => void }) {
  return <>
    <section className="work-summary" aria-label="Work summary">
      {boardColumns.map((column) => <div className={`work-summary-item work-state-${column.key}`} key={column.key}>
        <BoardIcon column={column.key} /><span>{column.label}</span><strong>{items.filter((item) => workInColumn(item, column.key)).length}</strong>
      </div>)}
    </section>
    <div className="work-board">
      {boardColumns.map((column) => {
        const values = items.filter((item) => workInColumn(item, column.key));
        return <section className={`work-column work-state-${column.key}`} key={column.key} aria-labelledby={`work-column-${column.key}`}>
          <header><span className="work-column-icon"><BoardIcon column={column.key} /></span><span><strong id={`work-column-${column.key}`}>{column.label}</strong><small>{column.hint}</small></span><b>{values.length}</b></header>
          <div className="work-column-list">
            {values.map((item) => <WorkCard key={item.id} item={item} onClick={() => onWork(item.id)} />)}
            {!values.length && <p className="work-column-empty">Nothing here</p>}
          </div>
        </section>;
      })}
    </div>
  </>;
}

function terminal(item: WorkItem): boolean {
  return item.terminal_at !== undefined;
}

function WorkCard({ item, onClick }: { item: WorkItem; onClick: () => void }) {
  const percent = item.stage_count ? Math.round((item.completed_stage_count / item.stage_count) * 100) : 0;
  return <button className={`work-card work-card-${item.state}`} onClick={onClick}
    aria-label={`${item.task_name} in ${repositoryName(item.repository_identity)}, ${item.state}`}>
    <span className="work-card-top"><StatusBadge state={item.state} />{item.planning && <span className="work-chip work-chip-planning">Planning</span>}<small>{timeAgo(item.updated_at)}</small></span>
    <strong>{item.task_name}</strong>
    {item.brief && <span className="work-card-brief">{briefPreview(item.brief)}</span>}
    <span className="work-card-tags">
      <span className="work-chip"><GitBranch size={11} />{repositoryName(item.repository_identity)}</span>
      {item.runtime && <span className="work-chip"><TerminalSquare size={11} />{runtimeLabel(item.runtime)}{item.current_stage?.model ? ` · ${item.current_stage.model}` : ""}</span>}
      {workerLabel(item) && !terminal(item) && <span className="work-chip" title={item.assigned_worker_id}><Bot size={11} />{workerLabel(item)}</span>}
      {terminal(item) && item.stage_count > 0 && <span className="work-chip"><GitMerge size={11} />{item.completed_stage_count}/{item.stage_count} stages</span>}
    </span>
    {terminal(item)
      ? <VerificationLine item={item} />
      : <span className="work-card-now"><span className="bar"><i style={{ width: `${percent}%` }} /></span><small>{stageLabel(item)}</small></span>}
    <span className="work-card-foot">
      <span>{item.attempt_count > 1 ? `${item.attempt_count} attempts` : item.source.replace("_", " ")}</span>
      <span className={item.reported_cost_usd === undefined ? "cost na" : "cost"}>{costLabel(item)}</span>
    </span>
  </button>;
}

// VerificationLine is the terminal card's one line of outcome. It counts
// checks, never tests: Factory knows how a command exited, not how many cases
// it contained.
function VerificationLine({ item }: { item: WorkItem }) {
  const verification = item.verification;
  if (verification && verification.recorded_checks > 0) {
    const failed = verification.failed > 0;
    return <span className="work-card-verify">
      <span className={failed ? "bad" : "ok"}>{failed ? "✕" : "✓"}</span>
      {failed
        ? `${verification.failed} of ${verification.recorded_checks} checks failed`
        : `${verification.passed} check${verification.passed === 1 ? "" : "s"} passed`}
    </span>;
  }
  if (item.failure_reason) return <span className="work-card-verify"><span className="bad">✕</span>{item.failure_reason}</span>;
  return <span className="work-card-verify quiet">No recorded checks</span>;
}

function briefPreview(brief: NonNullable<WorkItem["brief"]>): string {
  return [brief.context, brief.why, brief.risk, brief.work].filter(Boolean).join(" · ");
}

function ViewSwitch({ mode, onMode }: { mode: WorkViewMode; onMode: (mode: WorkViewMode) => void }) {
  return <div className="run-view-switcher" aria-label="Work view">
    <button aria-pressed={mode === "board"} onClick={() => onMode("board")}><Columns3 size={14} /> Board</button>
    <button aria-pressed={mode === "table"} onClick={() => onMode("table")}><Rows3 size={14} /> Table</button>
  </div>;
}

function WorkTable({ items, onWork }: { items: WorkItem[]; onWork: (id: string) => void }) {
  return <div className="work-table panel">
    <div className="work-table-row work-table-head"><span>Work</span><span>Repository</span><span>Stage</span><span>State</span><span>Cost</span><span>Updated</span></div>
    {items.map((item) => <button className="work-table-row" key={item.id} onClick={() => onWork(item.id)}>
      <span className="work-table-title"><strong>{item.task_name}</strong>{item.planning && <span className="work-chip work-chip-planning">Planning</span>}<small>{item.id.slice(0, 8)}</small></span>
      <span className="work-table-repo">{repositoryName(item.repository_identity)}</span>
      <span className="work-table-stage">{stageLabel(item)}</span>
      <span><StatusBadge state={item.state} /></span>
      <span className={item.reported_cost_usd === undefined ? "cost na" : "cost"}>{item.reported_cost_usd === undefined ? "—" : costLabel(item)}</span>
      <span className="work-table-time">{timeAgo(item.updated_at)}</span>
    </button>)}
  </div>;
}
