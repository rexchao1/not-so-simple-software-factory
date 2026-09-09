import { Bell, Bot, Boxes, Columns3, Compass, Gauge, GitBranch, Map as MapIcon, Menu, Pause as PauseIcon, Plus, Repeat2, Stamp, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import { DraftsView } from "./Drafts";
import { RepositoriesView, RepositoryDetail } from "./Repositories";
import { OverviewView } from "./Overview";
import { TasksView } from "./Tasks";
import { PlanningView } from "./Planning";
import { RoadmapView, WaitingView } from "./Roadmap";
import { RunDetailView } from "./Runs";
import { WorkView, type WorkViewMode } from "./Work";
import { WorkDetailView } from "./WorkDetail";
import { WorkersView, WorkerDetail } from "./Workers";
import { useVisibleInterval } from "./polling";
import { timeAgo } from "./format";
import { InlineError } from "./ui";

type Route =
  | { page: "overview" }
  | { page: "tasks"; id?: string; create?: boolean }
  | { page: "roadmap"; project?: string; checkpoint?: number }
  | { page: "planning" }
  | { page: "waiting" }
  | { page: "drafts" }
  | { page: "work"; mode: WorkViewMode }
  | { page: "work-detail"; id: string; mode: WorkViewMode }
  | { page: "run-detail"; id: string; mode: WorkViewMode }
  | { page: "workers" }
  | { page: "worker"; id: string }
  | { page: "repositories" }
  | { page: "repository"; id: string };

function readRoute(): Route {
  const parts = window.location.pathname.split("/").filter(Boolean);
  const search = new URLSearchParams(window.location.search);
  const mode = workMode(search.get("view"));
  if (parts[0] === "tasks") return { page: "tasks", id: parts[1], create: search.get("new") === "true" };
  if (parts[0] === "planning") return { page: "planning" };
  if (parts[0] === "roadmap") {
    const checkpoint = Number(search.get("c"));
    return {
      page: "roadmap",
      project: parts[1] ? decodeURIComponent(parts[1]) : undefined,
      checkpoint: Number.isInteger(checkpoint) && checkpoint > 0 ? checkpoint : undefined,
    };
  }
  if (parts[0] === "waiting") return { page: "waiting" };
  if (parts[0] === "drafts") return { page: "drafts" };
  // /work/<id> opens a Work item and /runs/<id> its parent Run. Both ids are
  // UUIDs, so the path is what distinguishes them. A Work id that does not
  // resolve falls back to the Run page, which keeps older links working.
  if (parts[0] === "work" && parts[1]) return { page: "work-detail", id: parts[1], mode };
  if (parts[0] === "runs" && parts[1]) return { page: "run-detail", id: parts[1], mode };
  if (parts[0] === "work" || parts[0] === "runs") return { page: "work", mode };
  if (parts[0] === "overview") return { page: "overview" };
  if (parts[0] === "workers" && parts[1]) return { page: "worker", id: parts[1] };
  if (parts[0] === "workers") return { page: "workers" };
  if (parts[0] === "repositories" && parts[1]) return { page: "repository", id: parts[1] };
  if (parts[0] === "repositories") return { page: "repositories" };
  return { page: "work", mode: "board" };
}

function workMode(value: string | null): WorkViewMode {
  return value === "list" || value === "table" ? "table" : "board";
}

function routePath(route: Route): string {
  switch (route.page) {
    case "tasks": return `/tasks${route.id ? `/${route.id}` : ""}${route.create ? "?new=true" : ""}`;
    case "planning": return "/planning";
    case "roadmap": {
      if (!route.project) return "/roadmap";
      const search = route.checkpoint ? `?c=${route.checkpoint}` : "";
      return `/roadmap/${encodeURIComponent(route.project)}${search}`;
    }
    case "waiting": return "/waiting";
    case "drafts": return "/drafts";
    case "work": return `/work${route.mode === "board" ? "" : `?view=${route.mode}`}`;
    case "work-detail": return `/work/${route.id}${route.mode === "board" ? "" : `?view=${route.mode}`}`;
    case "run-detail": return `/runs/${route.id}${route.mode === "board" ? "" : `?view=${route.mode}`}`;
    case "workers": return "/workers";
    case "worker": return `/workers/${route.id}`;
    case "repositories": return "/repositories";
    case "repository": return `/repositories/${route.id}`;
    default: return "/overview";
  }
}

export function App() {
  const [route, setRoute] = useState<Route>(readRoute);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [pauseOpen, setPauseOpen] = useState(false);
  const client = useQueryClient();
  const pause = useQuery({ queryKey: ["factory-pause"], queryFn: api.factoryPause, refetchInterval: 10_000 });
  const setPause = useMutation({ mutationFn: api.setFactoryPause, onSuccess: () => void client.invalidateQueries({ queryKey: ["factory-pause"] }) });
  const workerInterval = useVisibleInterval(10_000);
  const workers = useQuery({ queryKey: ["workers"], queryFn: api.workers, refetchInterval: workerInterval });
  // The sidebar badge is the only reason the shell knows about the roadmap.
  // It shares the Roadmap view's query key, so opening the page costs nothing
  // and the badge and the page can never disagree.
  const roadmap = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 30_000 });
  const waiting = roadmap.data?.waiting.length ?? 0;
  useEffect(() => {
    const onPopState = () => setRoute(readRoute());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const navigate = (next: Route) => {
    window.history.pushState({}, "", routePath(next));
    setRoute(next);
    setMobileNavOpen(false);
    window.scrollTo({ top: 0, behavior: "instant" });
  };
  const activeWorkMode = route.page === "work" || route.page === "work-detail" || route.page === "run-detail" ? route.mode : "board";
  return <div className="app-shell">
    <aside className={`sidebar ${mobileNavOpen ? "sidebar-open" : ""}`}>
      <div className="brand"><div className="brand-mark" aria-hidden="true"><Boxes size={18} strokeWidth={2.2} /></div><div><span className="brand-name">Factory</span><span className="brand-subtitle">control plane</span></div></div>
      <nav aria-label="Primary navigation">
        <div className="nav-section" role="group" aria-labelledby="work-nav-label">
          <div className="nav-section-label" id="work-nav-label">Work</div>
          <div className="nav-items">
          <Nav active={route.page === "work" || route.page === "work-detail" || route.page === "run-detail"} icon={<Columns3 size={17} />} label="Work" onClick={() => navigate({ page: "work", mode: activeWorkMode })} />
          <Nav active={route.page === "drafts"} icon={<Stamp size={17} />} label="Drafts" onClick={() => navigate({ page: "drafts" })} />
          <Nav active={route.page === "tasks"} icon={<Repeat2 size={17} />} label="Tasks" onClick={() => navigate({ page: "tasks" })} />
          <Nav active={route.page === "roadmap"} icon={<MapIcon size={17} />} label="Roadmap" onClick={() => navigate({ page: "roadmap" })} />
          <Nav active={route.page === "planning"} icon={<Compass size={17} />} label="Planning" onClick={() => navigate({ page: "planning" })} />
          <Nav active={route.page === "overview"} icon={<Gauge size={17} />} label="Overview" onClick={() => navigate({ page: "overview" })} />
          </div>
        </div>
        <div className="nav-section" role="group" aria-labelledby="infrastructure-nav-label">
          <div className="nav-section-label" id="infrastructure-nav-label">Infrastructure</div>
          <div className="nav-items">
            <Nav active={route.page === "workers" || route.page === "worker"} icon={<Bot size={17} />} label="Workers" onClick={() => navigate({ page: "workers" })} />
            <Nav active={route.page === "repositories" || route.page === "repository"} icon={<GitBranch size={17} />} label="Repositories" onClick={() => navigate({ page: "repositories" })} />
          </div>
        </div>
      </nav>
      <div className="sidebar-foot"><span className="local-dot" aria-hidden="true" /> Local control plane</div>
    </aside>
    <div className="main-shell">
      <header className="topbar"><button className="icon-button mobile-menu" aria-label="Toggle navigation" aria-expanded={mobileNavOpen} onClick={() => setMobileNavOpen((open) => !open)}>{mobileNavOpen ? <X size={19} /> : <Menu size={19} />}</button><div className="topbar-title">{pageTitle(route)}</div>{pause.data?.paused && <span className="pause-indicator"><PauseIcon size={11} /> Paused</span>}<WaitingBell count={waiting} active={route.page === "waiting"} onClick={() => navigate({ page: "waiting" })} /><button className="button button-secondary" disabled={setPause.isPending || pause.isPending} onClick={() => pause.data?.paused ? setPause.mutate({ paused: false }) : setPauseOpen(true)}>{pause.data?.paused ? "Resume" : "Pause"}</button><button className="button button-primary" onClick={() => navigate({ page: "tasks", create: true })}><Plus size={15} /> New Task</button></header>
      <main className="app-main">
        {pause.data?.paused && <div className="factory-pause-banner" role="status" aria-label="Factory pause"><strong>Factory is paused.</strong> <span>Active attempts continue; nothing new will be admitted or dispatched.</span>{pause.data.paused_at && <span className="pause-when">{timeAgo(pause.data.paused_at)}</span>}</div>}
        <InlineError error={setPause.error} />
        {route.page === "overview" && <OverviewView onRun={(id) => navigate({ page: "run-detail", id, mode: "board" })} onTask={(id) => navigate({ page: "tasks", id })} onWork={(id) => navigate({ page: "work-detail", id, mode: "board" })} />}
        {route.page === "tasks" && <TasksView key={`${route.id ?? "list"}:${route.create ?? false}`} initialID={route.id} createOpen={route.create} onRun={(id) => navigate({ page: "run-detail", id, mode: "board" })} />}
        {route.page === "roadmap" && <RoadmapView
          project={route.project}
          checkpoint={route.checkpoint}
          onProject={(project) => navigate(project ? { page: "roadmap", project } : { page: "roadmap" })}
          onView={(checkpoint) => navigate({ ...route, checkpoint })}
          onWaiting={() => navigate({ page: "waiting" })}
          onWork={(id) => navigate({ page: "work-detail", id, mode: "board" })}
        />}
        {route.page === "planning" && <PlanningView
          onProject={(project, checkpoint) => navigate({ page: "roadmap", project, checkpoint })}
          onWork={(id) => navigate({ page: "work-detail", id, mode: "board" })}
        />}
        {route.page === "waiting" && <WaitingView onProject={(project, checkpoint) => navigate({ page: "roadmap", project, checkpoint })} />}
        {route.page === "drafts" && <DraftsView />}
        {route.page === "work" && <WorkView mode={route.mode} onMode={(mode) => navigate({ page: "work", mode })} onWork={(id) => navigate({ page: "work-detail", id, mode: route.mode })} />}
        {route.page === "work-detail" && <WorkDetailView key={route.id} id={route.id} onBack={() => navigate({ page: "work", mode: route.mode })} onRun={(runID) => navigate({ page: "run-detail", id: runID, mode: route.mode })} onWork={(workID) => navigate({ page: "work-detail", id: workID, mode: route.mode })} onMissing={(id) => navigate({ page: "run-detail", id, mode: route.mode })} />}
        {route.page === "run-detail" && <RunDetailView id={route.id} onBack={() => navigate({ page: "work", mode: route.mode })} />}
        {route.page === "workers" && <WorkersView workers={workers.data} pending={workers.isPending} error={workers.error} fetching={workers.isFetching} updatedAt={workers.dataUpdatedAt} onWorker={(id) => navigate({ page: "worker", id })} onRefresh={() => void workers.refetch()} />}
        {route.page === "worker" && <WorkerDetail id={route.id} legacyReadOnly onBack={() => navigate({ page: "workers" })} onDelegate={() => {}} />}
        {route.page === "repositories" && <RepositoriesView onRepository={(id) => navigate({ page: "repository", id })} />}
        {route.page === "repository" && <RepositoryDetail id={route.id} onBack={() => navigate({ page: "repositories" })} />}
      </main>
    </div>
    {mobileNavOpen && <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMobileNavOpen(false)} />}
    {pauseOpen && <PauseDialog pending={setPause.isPending} onClose={() => setPauseOpen(false)} onConfirm={() => setPause.mutate({ paused: true }, { onSuccess: () => setPauseOpen(false) })} />}
  </div>;
}

// PauseDialog states the three consequences before the operator commits.
// Pausing stops every admission path and every dispatch across the whole
// factory, so it is the one control in the top bar that should not act on a
// single click.
function PauseDialog({ pending, onClose, onConfirm }: { pending: boolean; onClose: () => void; onConfirm: () => void }) {
  return <div className="modal-layer" role="presentation">
    <button className="modal-scrim" aria-label="Close pause dialog" onClick={onClose} />
    <section className="modal pause-modal" role="dialog" aria-modal="true" aria-labelledby="pause-title">
      <form onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
        <header className="modal-header"><div><h2 id="pause-title">Pause Factory?</h2></div><button type="button" className="icon-button" aria-label="Close" onClick={onClose}><X size={17} /></button></header>
        <div className="modal-body">
          <ul className="pause-consequences">
            <li>Active attempts continue to completion.</li>
            <li>No new Work will be admitted.</li>
            <li>Queued and blocked Work will not be dispatched.</li>
          </ul>
        </div>
        <div className="modal-footer"><button type="button" className="button button-secondary" onClick={onClose}>Cancel</button><button type="submit" className="button button-primary" autoFocus disabled={pending}>{pending ? "Pausing…" : "Pause Factory"}</button></div>
      </form>
    </section>
  </div>;
}

function Nav({ active, icon, label, onClick }: { active: boolean; icon: ReactNode; label: string; onClick: () => void }) {
  return <button className={`nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={onClick}>{icon}{label}</button>;
}

// One bell, red when something is actually waiting. It is the only alarm in
// the cockpit, so it stays quiet, and grey, when there is nothing to say.
function WaitingBell({ count, active, onClick }: { count: number; active: boolean; onClick: () => void }) {
  const label = count === 0 ? "Nothing is waiting for you" : count === 1 ? "1 thing waiting for you" : `${count} things waiting for you`;
  return <button className={`waiting-bell ${count > 0 ? "ringing" : ""} ${active ? "active" : ""}`} aria-label={label} title={label} onClick={onClick}>
    <Bell size={17} />
    {count > 0 && <span className="waiting-bell-count">{count > 99 ? "99+" : count}</span>}
  </button>;
}

function pageTitle(route: Route): string {
  if (route.page === "work-detail") return "Work detail";
  if (route.page === "run-detail") return "Run detail";
  if (route.page === "worker") return "Worker detail";
  if (route.page === "repository") return "Repository detail";
  if (route.page === "waiting") return "Waiting for you";
  return route.page[0].toUpperCase() + route.page.slice(1);
}
