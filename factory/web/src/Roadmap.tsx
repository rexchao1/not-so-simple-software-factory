import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ArrowLeft, ChevronRight, CircleCheck, ExternalLink, Inbox, Map as MapIcon } from "lucide-react";
import { api } from "./api";
import { liveLabel } from "./format";
import type {
  CheckpointStatus, LoadedRoadmap, RoadmapCheckpoint, RoadmapLivePass,
  RoadmapPebble, RoadmapProject, RoadmapStone, StoneState,
} from "./types";
import { EmptyState, ErrorState, LoadingState, ViewHeader } from "./ui";

// A checkpoint reads as one of five words, the five the server will send. The
// cockpit's own state palette has no vocabulary for planning, so these get
// their own, and the order here is the order a checkpoint moves through.
const statusLabels: Record<CheckpointStatus, string> = {
  planned: "Planned",
  review: "Waiting on you",
  fog: "Stuck on questions",
  frozen: "Frozen",
  built: "Built",
};

// What a stone's colour means. These are rolled up from the factory's own
// Work rows, so they say what is happening rather than what was planned.
const stoneLabels: Record<StoneState, string> = {
  planned: "Not started",
  part: "Partly built",
  working: "Being built",
  failed: "Needs a look",
  done: "Built",
};

// A checkpoint is live when a critique is turning on it right now. That comes
// from the Work row running the pass, not from the status word: the status
// says what the plan is, the running Work says whether anyone is touching it.
// Every surface that can show a checkpoint shows this differently, because it
// is the single thing the human asked to see at a glance.
const isLive = (checkpoint: { live?: RoadmapLivePass | null }) => Boolean(checkpoint.live);

const money = (value: number) => `$${value.toFixed(2)}`;

const checkpointsOf = (project: RoadmapProject) => project.checkpoints ?? [];
const stonesOf = (checkpoint: RoadmapCheckpoint) => checkpoint.stones ?? [];
const pebblesOf = (stone: RoadmapStone) => stone.pebbles ?? [];

export function RoadmapView({ project, checkpoint, onProject, onView, onWaiting, onWork }: {
  project?: string;
  checkpoint?: number;
  onProject: (project: string | null) => void;
  onView: (checkpoint: number) => void;
  onWaiting: () => void;
  onWork: (id: string) => void;
}) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 15_000 });
  if (query.isPending) return <LoadingState label="Loading Roadmap" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  const header = <ViewHeader title="Roadmap" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />;
  if (!roadmap.configured) return <div className="page">{header}<UnreadableRoadmap /></div>;
  const selected = project ? roadmap.projects.find((entry) => entry.project === project) : undefined;
  if (project && !selected) {
    return <div className="page">{header}<EmptyState icon={<MapIcon size={22} />} title="Nothing planned for that" description={`Nothing on the roadmap is planning ${project}.`} action={<button className="button button-secondary" onClick={() => onProject(null)}>Back to the roadmap</button>} /></div>;
  }
  return <div className="page roadmap-page">
    {header}
    {selected
      ? <ProjectDetail project={selected} checkpoint={checkpoint} onBack={() => onProject(null)} onView={onView} onWork={onWork} />
      : <ProjectGrid roadmap={roadmap} onProject={onProject} onWaiting={onWaiting} />}
  </div>;
}

// Planning lives in the factory's own database, so there is nothing to point
// at and no way to have the factory without it. This is the reading the server
// could not take, not a setting anyone forgot.
export function UnreadableRoadmap() {
  return <EmptyState
    icon={<MapIcon size={22} />}
    title="Planning could not be read"
    description="The factory could not read its planning state. Refresh in a moment, and check the server if it keeps saying this."
  />;
}

// The landing is one card per project, not a list of every checkpoint under
// every project. Checkpoints and pebbles keep arriving, and a page that grows
// with them stops being readable long before it stops fitting.
function ProjectGrid({ roadmap, onProject, onWaiting }: { roadmap: LoadedRoadmap; onProject: (project: string) => void; onWaiting: () => void }) {
  const waiting = roadmap.waiting;
  if (!roadmap.projects.length) {
    return <EmptyState icon={<MapIcon size={22} />} title="Nothing planned yet" description="No project has been planned. A project appears here the moment the light factory saves its route." />;
  }
  return <>
    <div className="view-toolbar">
      <p>Everything being planned, and how far along its route it is.</p>
      {waiting.length > 0 && <button className="button button-secondary" onClick={onWaiting}><Inbox size={15} /> {waiting.length} waiting on you</button>}
    </div>
    <div className="boulder-grid">{roadmap.projects.map((project) => {
      const checkpoints = checkpointsOf(project);
      const live = checkpoints.filter(isLive).length + (project.live ? 1 : 0);
      const needsYou = waiting.filter((entry) => entry.project === project.project).length;
      return <button className="boulder-card" key={project.project} onClick={() => onProject(project.project)}>
        <span className="boulder-card-head">
          <strong>{project.title}</strong>
          <ChevronRight size={16} className="boulder-card-chevron" />
        </span>
        {project.statement && <span className="boulder-statement">{project.statement}</span>}
        <span className="boulder-chain" aria-label={`${checkpoints.length} checkpoints, ${project.built_count} built`}>
          {checkpoints.map((entry) => <i key={entry.number} className={`chain-pip status-plan-${entry.status}`} title={`${entry.number}. ${entry.title}`} />)}
        </span>
        <span className="boulder-card-foot">
          <span>{project.built_count} of {checkpoints.length} built</span>
          <span className="mono">{money(project.cost_usd)} planned</span>
          {live > 0 && <LiveBadge label={live === 1 ? "Agent working" : `${live} agents working`} />}
          {needsYou > 0 && <span className="waiting-pill">{needsYou} waiting on you</span>}
        </span>
      </button>;
    })}</div>
  </>;
}

function ProjectDetail({ project, checkpoint, onBack, onView, onWork }: {
  project: RoadmapProject;
  checkpoint?: number;
  onBack: () => void;
  onView: (checkpoint: number) => void;
  onWork: (id: string) => void;
}) {
  const checkpoints = checkpointsOf(project);
  const active = checkpoints.find((entry) => entry.number === checkpoint) ?? defaultCheckpoint(checkpoints);
  return <>
    <button className="back-link" onClick={onBack}><ArrowLeft size={15} /> All projects</button>
    <header className="boulder-heading">
      <div className="boulder-heading-body">
        <h2>{project.title}</h2>
        {project.statement && <Statement text={project.statement} />}
      </div>
      <span className="boulder-heading-meta mono">{project.built_count}/{checkpoints.length} built · {money(project.cost_usd)} planned</span>
    </header>
    {checkpoints.length === 0
      ? <div className="quiet-empty">This route has no checkpoints yet.</div>
      : <>
        <nav className="checkpoint-bar" aria-label="Checkpoints">
          {checkpoints.map((entry) => <button
            key={entry.number}
            className={`checkpoint-tab status-plan-${entry.status} ${entry.number === active?.number ? "active" : ""}`}
            aria-current={entry.number === active?.number ? "true" : undefined}
            onClick={() => onView(entry.number)}
          >
            <span className="checkpoint-tab-number">{entry.status === "built" ? <CircleCheck size={13} /> : entry.number}</span>
            <span className="checkpoint-tab-title">{entry.title}</span>
            <span className="checkpoint-tab-status">{isLive(entry) ? <LiveBadge label={liveLabel(entry.live)} /> : statusLabels[entry.status]}</span>
          </button>)}
        </nav>
        {active && <CheckpointStage key={active.number} checkpoint={active} onWork={onWork} />}
      </>}
  </>;
}

// Opening a project should land on the checkpoint that is actually moving.
// The first unbuilt one is that checkpoint; a fully built route falls back to
// its last rung rather than to nothing.
function defaultCheckpoint(checkpoints: RoadmapCheckpoint[]): RoadmapCheckpoint | undefined {
  return checkpoints.find((entry) => entry.status !== "built") ?? checkpoints[checkpoints.length - 1];
}

// A project statement is the whole of what the human asked for, and it runs to
// paragraphs. It stays available in full, and stays out of the way until asked
// for, because the page above it is the thing being read.
function Statement({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 220;
  return <div className="boulder-statement-block">
    <p className={long && !open ? "clamped" : ""}>{text}</p>
    {long && <button className="statement-toggle" onClick={() => setOpen((current) => !current)}>{open ? "Show less" : "Show more"}</button>}
  </div>;
}

// The stage is the chosen checkpoint, big: its stones on the left and one
// detail box on the right. The right box reads the checkpoint until a pebble
// is picked, so there is always something in it and never two things.
function CheckpointStage({ checkpoint, onWork }: { checkpoint: RoadmapCheckpoint; onWork: (id: string) => void }) {
  const stones = stonesOf(checkpoint);
  const [openStone, setOpenStone] = useState<string | null>(null);
  const [openPebble, setOpenPebble] = useState<string | null>(null);
  // A refetch can retire the pebble that is open. Resolving the selection every
  // render rather than storing the pebble means a stale slug simply falls back
  // to the checkpoint, and the right box is never empty.
  const pebble = stones.flatMap(pebblesOf).find((entry) => entry.slug === openPebble);
  return <div className="checkpoint-stage">
    <div className="stage-stones">
      {stones.length === 0
        ? <p className="quiet-empty">{checkpoint.planned
          ? checkpoint.status === "frozen"
            ? "Frozen and ready to be split into pebbles."
            : "No pebbles yet. A checkpoint is split once it is frozen."
          : "Still a line on the route. Nothing has been drafted for it yet."}</p>
        : stones.map((stone) => <StoneBox
          key={stone.id}
          stone={stone}
          open={openStone === stone.id}
          openPebble={openPebble}
          onToggle={() => setOpenStone((current) => current === stone.id ? null : stone.id)}
          onPebble={(slug) => setOpenPebble((current) => current === slug ? null : slug)}
        />)}
    </div>
    <aside className="stage-detail" aria-label="Details">
      {pebble
        ? <PebbleDetail pebble={pebble} onBack={() => setOpenPebble(null)} onWork={onWork} />
        : <CheckpointDetail checkpoint={checkpoint} />}
    </aside>
  </div>;
}

// One stone, as big as the page allows, coloured by what the factory is
// actually doing with it. Clicking drops its pebbles in underneath.
function StoneBox({ stone, open, openPebble, onToggle, onPebble }: {
  stone: RoadmapStone;
  open: boolean;
  openPebble: string | null;
  onToggle: () => void;
  onPebble: (slug: string) => void;
}) {
  const pebbles = pebblesOf(stone);
  const done = pebbles.filter((pebble) => pebble.state === "succeeded" || pebble.state === "no-change" || pebble.state === "built").length;
  return <section className={`stone-box stone-${stone.state} ${open ? "open" : ""}`}>
    <button className="stone-box-head" aria-expanded={open} onClick={onToggle}>
      <span className="stone-box-title">
        <ChevronRight size={18} className="stone-box-chevron" />
        <strong>{stone.title}</strong>
      </span>
      {stone.statement && <span className="stone-box-statement">{stone.statement}</span>}
      <span className="stone-box-foot">
        <span className="stone-state-pill">{stoneLabels[stone.state]}</span>
        <span className="mono">{done}/{pebbles.length} done</span>
      </span>
    </button>
    {open && <ol className="pebble-drop">{pebbles.map((pebble, index) => <li key={pebble.slug}>
      <button
        className={`pebble-row pebble-${pebble.state || "none"} ${openPebble === pebble.slug ? "active" : ""}`}
        aria-pressed={openPebble === pebble.slug}
        onClick={() => onPebble(pebble.slug)}
      >
        <span className="pebble-ordinal">{index + 1}</span>
        <span className="pebble-row-title">{pebble.title}</span>
        {pebble.state && <span className={`pebble-state state-${pebble.state}`}>{pebble.state}</span>}
      </button>
    </li>)}</ol>}
  </section>;
}

function CheckpointDetail({ checkpoint }: { checkpoint: RoadmapCheckpoint }) {
  const pebbles = checkpoint.pebbles ?? [];
  const stones = stonesOf(checkpoint);
  return <>
    <header className="stage-detail-head">
      <span className="eyebrow">Checkpoint {checkpoint.number}</span>
      <h3>{checkpoint.title}</h3>
      <span className={`plan-badge status-plan-${checkpoint.status}`}><span className="status-dot" />{statusLabels[checkpoint.status]}</span>
      {checkpoint.live && <LiveBadge label={liveLabel(checkpoint.live)} />}
    </header>
    {checkpoint.summary && <p className="stage-detail-body">{checkpoint.summary}</p>}
    <dl className="stage-facts">
      <div><dt>Planning cost</dt><dd className="mono">{money(checkpoint.cost_usd)}</dd></div>
      <div><dt>Planning passes</dt><dd className="mono">{checkpoint.pass_rounds}</dd></div>
      <div><dt>Stones</dt><dd className="mono">{stones.length}</dd></div>
      <div><dt>Pebbles</dt><dd className="mono">{pebbles.length}</dd></div>
    </dl>
    {stones.length > 0 && <p className="stage-detail-hint">Pick a stone on the left to see its pebbles, then a pebble to read it here.</p>}
  </>;
}

function PebbleDetail({ pebble, onBack, onWork }: { pebble: RoadmapPebble; onBack: () => void; onWork: (id: string) => void }) {
  return <>
    <button className="back-link" onClick={onBack}><ArrowLeft size={15} /> Back to the checkpoint</button>
    <header className="stage-detail-head">
      <span className="eyebrow">Pebble {pebble.ordinal}</span>
      <h3>{pebble.title}</h3>
      {pebble.state && <span className={`pebble-state state-${pebble.state}`}>{pebble.state}</span>}
    </header>
    {pebble.summary
      ? <p className="stage-detail-body">{pebble.summary}</p>
      : <p className="quiet-empty">This task has no opening paragraph to show.</p>}
    <dl className="stage-facts">
      <div><dt>Slug</dt><dd className="mono">{pebble.slug}</dd></div>
      {!pebble.state && <div><dt>Factory</dt><dd>Not submitted yet</dd></div>}
    </dl>
    <div className="stage-detail-actions">
      {pebble.work_id && <button className="button button-secondary" onClick={() => onWork(pebble.work_id as string)}>Open the run</button>}
      {pebble.pull_request_url && <a className="button button-secondary" href={pebble.pull_request_url} target="_blank" rel="noreferrer">Pull request <ExternalLink size={14} /></a>}
      {!pebble.pull_request_url && pebble.built_ref && (isURL(pebble.built_ref)
        ? <a className="button button-secondary" href={pebble.built_ref} target="_blank" rel="noreferrer">Built as <ExternalLink size={14} /></a>
        : <span className="mono">Built as {pebble.built_ref}</span>)}
    </div>
  </>;
}

// A ref recorded for a pebble built by hand is either a commit sha or a pull
// request URL. Only the URL is worth a link; a sha is read, not opened.
function isURL(value: string): boolean {
  return /^https?:\/\//i.test(value);
}

export function LiveBadge({ label }: { label: string }) {
  return <span className="live-badge"><span className="live-dot" aria-hidden="true" />{label}</span>;
}

// Waiting is its own page because it answers one question, and answering it
// should never mean opening a project to find out.
export function WaitingView({ onProject }: { onProject: (project: string, checkpoint: number) => void }) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 15_000 });
  if (query.isPending) return <LoadingState label="Loading what is waiting" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  return <div className="page">
    <ViewHeader title="Waiting for you" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />
    {!roadmap.configured ? <UnreadableRoadmap />
      : !roadmap.waiting.length
        ? <EmptyState icon={<CircleCheck size={22} />} title="Nothing is waiting on you" description="Every checkpoint being planned is either moving on its own or already built." />
        : <div className="waiting-list">{roadmap.waiting.map((entry) => <button className="waiting-card" key={`${entry.project}-${entry.number}`} onClick={() => onProject(entry.project, entry.number)}>
          <span className="waiting-card-head">
            <span className="boulder-id">{entry.number}</span>
            <strong>{entry.title}</strong>
            <span className={`plan-badge status-plan-${entry.status}`}><span className="status-dot" />{statusLabels[entry.status]}</span>
          </span>
          <span className="waiting-reason">{entry.reason}</span>
          <span className="waiting-card-foot">
            <span className="waiting-action">{entry.action}<ChevronRight size={14} /></span>
            <span className="mono">{entry.project} · {money(entry.cost_usd)} planned</span>
          </span>
        </button>)}</div>}
  </div>;
}
