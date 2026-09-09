import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Compass, Sparkles } from "lucide-react";
import { api } from "./api";
import { liveLabel, timeAgo } from "./format";
import { LiveBadge, UnreadableRoadmap } from "./Roadmap";
import type {
  CheckpointStatus, LoadedRoadmap, RoadmapCheckpoint, RoadmapLivePass,
  RoadmapPass, RoadmapWaiting,
} from "./types";
import { EmptyState, ErrorState, InlineError, LoadingState, StatusBadge, ViewHeader } from "./ui";

// The six stops a checkpoint makes between an idea and a set of tasks. This is
// the light factory's own chain: the agent drafts, the critic critiques, the
// agent revises, the human reviews in lavish, says freeze, and the checkpoint
// is cut into pebbles. The stops are named once here and everything else on
// the page is placed against them.
const stages = ["Draft", "Critique", "Revise", "Review", "Freeze", "Pebbles"] as const;

const money = (value: number) => `$${value.toFixed(2)}`;

// How long a pass took, from the milliseconds the server measured. A critique
// runs for seconds or minutes, so this never needs an hour.
function took(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

// A checkpoint being planned, lifted out of its project so the page can show
// them side by side. The project is carried along because the whole point of
// this page is looking across projects at once.
interface Planning {
  project: string;
  projectTitle: string;
  checkpoint: RoadmapCheckpoint;
  passes: RoadmapPass[];
  stage: number;
  live: RoadmapLivePass | null;
  // waiting is the server's own entry for this checkpoint, not a second guess
  // at the same rule. The bell in the shell counts that list and this page
  // reads it, so the two cannot disagree about what is waiting on the human.
  waiting: RoadmapWaiting | null;
}

const keyOf = (project: string, number: number) => `${project}:${number}`;

// A checkpoint is being planned from the moment a PRD is offered for review
// until its pebbles exist. A frozen checkpoint that already has pebbles is
// finished planning even though nothing has built it yet: that is the
// Roadmap's story, not this page's. A pass turning right now overrides all of
// that, because a checkpoint being touched is being planned whatever its
// status says.
function planningOf(roadmap: LoadedRoadmap): Planning[] {
  const waitingFor = new Map(roadmap.waiting.map((entry) => [keyOf(entry.project, entry.number), entry]));
  const rows: Planning[] = [];
  for (const project of roadmap.projects) {
    for (const checkpoint of project.checkpoints ?? []) {
      const pebbles = checkpoint.pebbles ?? [];
      const passes = checkpoint.passes ?? [];
      const live = checkpoint.live ?? null;
      const waiting = waitingFor.get(keyOf(project.project, checkpoint.number)) ?? null;
      if (!live && !waiting) {
        if (checkpoint.status === "planned" || checkpoint.status === "built") continue;
        if (checkpoint.status === "frozen" && pebbles.length > 0) continue;
      }
      rows.push({
        project: project.project,
        projectTitle: project.title,
        checkpoint,
        passes,
        stage: stageOf(checkpoint.status, passes, live),
        live,
        waiting,
      });
    }
  }
  // The ones actually turning come first, then the ones waiting on the human,
  // then the rest.
  const rank = (row: Planning) => (row.live ? 0 : row.waiting ? 1 : 2);
  return rows.sort((a, b) => rank(a) - rank(b) || a.project.localeCompare(b.project) || a.checkpoint.number - b.checkpoint.number);
}

// Where on the rail a checkpoint is standing. A critique turning right now is
// the answer whenever there is one, because it is the only source that cannot
// already be out of date. Otherwise the status says it, except for the gap
// between a critique coming back and the human being asked to read the result:
// that is the agent applying findings, which is Revise.
function stageOf(status: CheckpointStatus, passes: RoadmapPass[], live?: RoadmapLivePass | null): number {
  if (live) return 1;
  if (status === "review") return 3;
  if (status === "frozen") return 4;
  if (status === "fog") return 0;
  return passes.length > 0 ? 2 : 0;
}

export function PlanningView({ onProject, onWork }: {
  onProject: (project: string, checkpoint: number) => void;
  onWork: (id: string) => void;
}) {
  const query = useQuery({ queryKey: ["roadmap"], queryFn: api.roadmap, refetchInterval: 10_000 });
  const [focus, setFocus] = useState<string | null>(null);
  if (query.isPending) return <LoadingState label="Loading Planning" />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  const roadmap = query.data;
  const header = <ViewHeader title="Planning" fetching={query.isFetching} updatedAt={query.dataUpdatedAt} onRefresh={() => void query.refetch()} />;
  if (!roadmap.configured) return <div className="page">{header}<UnreadableRoadmap /></div>;
  if (!roadmap.projects.length) {
    return <div className="page">{header}
      <EmptyState icon={<Compass size={22} />} title="Nothing has been planned yet" description="No project has been planned. The light factory saves a route and its checkpoints here as it writes them, and this page follows whichever one is moving." />
    </div>;
  }
  const rows = planningOf(roadmap);
  if (!rows.length) {
    return <div className="page">{header}
      <EmptyState icon={<Sparkles size={22} />} title="Nothing is being planned" description="Every checkpoint is either already split into pebbles or still just a line on a route. Planning shows up here the moment a critique starts or a plan is offered for review." />
      <PlanningRail stage={-1} />
    </div>;
  }
  const key = (row: Planning) => keyOf(row.project, row.checkpoint.number);
  const active = rows.find((row) => key(row) === focus) ?? rows[0];
  const liveCount = rows.filter((row) => row.live).length;
  return <div className="page planning-page">
    {header}
    <div className="view-toolbar">
      <p>What is being planned right now, and how far through the chain it is. The Roadmap shows what planning has already produced.</p>
      {liveCount > 0 && <LiveBadge label={liveCount === 1 ? "1 critique running" : `${liveCount} critiques running`} />}
    </div>
    <PlanningStage row={active} onOpen={() => onProject(active.project, active.checkpoint.number)} onWork={onWork} />
    {rows.length > 1 && <div className="planning-others">
      {rows.map((row) => <button
        key={key(row)}
        className={`planning-chip ${key(row) === key(active) ? "active" : ""} ${row.live ? "live" : ""} ${row.waiting ? "stalled" : ""}`}
        aria-current={key(row) === key(active) ? "true" : undefined}
        onClick={() => setFocus(key(row))}
      >
        <span className="planning-chip-project mono">{row.project}</span>
        <span className="planning-chip-title">{row.checkpoint.number}. {row.checkpoint.title}</span>
        <span className="planning-chip-stage">{stages[row.stage]}</span>
      </button>)}
    </div>}
  </div>;
}

// The one being watched, at full size: the rail it is standing on, the pass
// that is turning, every round it has had, and the plan itself.
function PlanningStage({ row, onOpen, onWork }: { row: Planning; onOpen: () => void; onWork: (id: string) => void }) {
  const { checkpoint, passes } = row;
  return <section className="planning-stage">
    <header className="planning-stage-head">
      <div>
        <span className="eyebrow mono">{row.project} · checkpoint {checkpoint.number}</span>
        <h2>{checkpoint.title}</h2>
        {checkpoint.summary && <p className="planning-stage-summary">{checkpoint.summary}</p>}
      </div>
      <div className="planning-stage-meta">
        {row.live && <LivePass live={row.live} onWork={onWork} />}
        {row.waiting && <span className="planning-stalled">{row.waiting.action}</span>}
        <span className="mono">{money(checkpoint.cost_usd)} · {checkpoint.pass_rounds} {checkpoint.pass_rounds === 1 ? "round" : "rounds"}</span>
        <button className="button button-secondary" onClick={onOpen}>Open on the roadmap</button>
      </div>
    </header>
    <PlanningRail stage={row.stage} live={Boolean(row.live)} />
    {row.waiting && <p className="planning-waiting-note">{row.waiting.reason}</p>}
    {row.live && <PlanningOrbit live={row.live} passes={passes} onWork={onWork} />}
    {passes.length > 0
      ? <PlanningPasses passes={passes} onWork={onWork} />
      : <p className="quiet-empty">No critique has run for this checkpoint yet.</p>}
    <CheckpointPlan project={row.project} number={checkpoint.number} planned={checkpoint.planned} />
  </section>;
}

// The rail: every stop, with the current one lit. A live checkpoint pulses on
// its stop, so the page answers "is anything happening" without being read.
function PlanningRail({ stage, live }: { stage: number; live?: boolean }) {
  return <ol className="planning-rail" aria-label="Planning stages">
    {stages.map((label, index) => <li
      key={label}
      className={`rail-stop ${index < stage ? "done" : ""} ${index === stage ? "here" : ""} ${index === stage && live ? "live" : ""}`}
      aria-current={index === stage ? "step" : undefined}
    >
      <span className="rail-dot" aria-hidden="true" />
      <span className="rail-label">{label}</span>
    </li>)}
  </ol>;
}

// The live pass, as something to open. A running critique is a Work item like
// any other, and the one thing anyone wants when they see it turning is to
// watch it, so the badge is the link. A pass with no Work id behind it is
// still drawn, because saying nothing is running would be worse than saying
// something is running and not being able to say where.
function LivePass({ live, onWork }: { live: RoadmapLivePass; onWork: (id: string) => void }) {
  const badge = <LiveBadge label={liveLabel(live)} />;
  if (!live.work_id) return badge;
  const id = live.work_id;
  return <button className="planning-link" onClick={() => onWork(id)}>{badge}</button>;
}

// A critic turning. It says nothing about how far along the pass is, because
// nothing knows that until the critic prints its findings. What it can say
// truthfully is which round is running, since when, and on which model, all of
// it from the Work row the pass is.
function PlanningOrbit({ live, passes, onWork }: {
  live: RoadmapLivePass;
  passes: RoadmapPass[];
  onWork: (id: string) => void;
}) {
  const latest = passes[passes.length - 1];
  return <div className="planning-orbit">
    <svg viewBox="0 0 260 200" role="img" aria-label="A critique is running">
      <circle className="orbit-ring" cx="130" cy="100" r="70" />
      <g className="orbit-spin">
        <circle className="orbit-beam" cx="130" cy="30" r="7" />
      </g>
      <circle className="orbit-core" cx="130" cy="100" r="30" />
      <text className="orbit-core-label" x="130" y="104" textAnchor="middle">plan</text>
      {[["Draft", 130, 24], ["Critique", 194, 138], ["Revise", 66, 138]].map(([label, x, y]) => <g key={label as string}>
        <circle className="orbit-node" cx={x as number} cy={y as number} r="6" />
        <text className="orbit-node-label" x={x as number} y={(y as number) + (label === "Draft" ? -14 : 22)} textAnchor="middle">{label}</text>
      </g>)}
    </svg>
    <div className="planning-live">
      <LivePass live={live} onWork={onWork} />
      <span className="mono">started {timeAgo(live.started)}{live.model ? ` · ${live.model}` : ""}</span>
      {latest && <span className="mono">last round {latest.round}, {timeAgo(latest.at)}</span>}
    </div>
  </div>;
}

// Every round this checkpoint has had, in the order they happened. Round,
// model, cost, how long it took and how it ended, and each row opens the Work
// it ran as: the findings themselves are on the Work and there is nowhere else
// to read them.
function PlanningPasses({ passes, onWork }: { passes: RoadmapPass[]; onWork: (id: string) => void }) {
  return <div className="planning-passes">
    <table aria-label="Critique rounds">
      <thead><tr>
        <th scope="col">Round</th>
        <th scope="col">Model</th>
        <th scope="col">Cost</th>
        <th scope="col">Took</th>
        <th scope="col">Outcome</th>
        <th scope="col"><span className="visually-hidden">Work</span></th>
      </tr></thead>
      <tbody>{passes.map((pass, index) => <tr key={`${pass.round}-${pass.at}-${index}`}>
        <td>Round {pass.round}</td>
        <td className="mono">{pass.model || "not recorded"}</td>
        <td className="mono">{money(pass.cost_usd)}</td>
        <td className="mono">{pass.duration_ms ? took(pass.duration_ms) : "not measured"}</td>
        <td>{pass.outcome ? <StatusBadge state={pass.outcome} /> : <span className="quiet-cell">unknown</span>}</td>
        <td>{pass.work_id
          ? <button className="planning-link" onClick={() => onWork(pass.work_id as string)}>Open the Work</button>
          : <span className="quiet-cell">no Work</span>}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

// The review happens in lavish, in the same session that wrote the PRD. What
// the cockpit owes is the text that was reviewed and what was said back, both
// exactly as saved and neither editable: the light factory writes planning
// state and a second writer would be a second truth.
//
// The body is a PRD in markdown and it is shown as written. The cockpit ships
// no markdown renderer, and a whole rendering dependency is a lot to carry for
// one read-only panel, so the text is preformatted rather than parsed.
function CheckpointPlan({ project, number, planned }: { project: string; number: number; planned: boolean }) {
  const query = useQuery({
    queryKey: ["planning-checkpoint", project, number],
    queryFn: () => api.planningCheckpoint(project, number),
    enabled: planned,
  });
  if (!planned) return <p className="quiet-empty">Nothing has been drafted for this checkpoint yet.</p>;
  if (query.isPending) return <p className="loading-line">Loading the plan</p>;
  if (query.isError) return <InlineError error={query.error} />;
  const body = query.data.body?.trim() ?? "";
  const answers = query.data.answers?.trim() ?? "";
  return <section className="planning-plan">
    <h3>The plan</h3>
    {body
      ? <pre className="planning-prose">{body}</pre>
      : <p className="quiet-empty">This checkpoint has a status but no saved PRD.</p>}
    {answers && <>
      <h3>What you said at review</h3>
      <pre className="planning-prose">{answers}</pre>
    </>}
  </section>;
}
