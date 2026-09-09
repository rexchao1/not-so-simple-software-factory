// Formatting shared by the Work board and the Work detail page. It lives
// apart from the components so that both files export components only, which
// is what the react-refresh rule requires.
import type { StageRun, WorkItem } from "./types";

export function repositoryName(identity: string): string {
  const parts = identity.replace(/\.git$/, "").split("/").filter(Boolean);
  return parts.at(-1) ?? identity;
}

// costLabel keeps the one rule that matters about money: a runtime that
// reported nothing is unavailable, never $0.00. Only an actual reported figure
// prints as a figure.
export function costLabel(item: Pick<WorkItem, "reported_cost_usd">): string {
  return item.reported_cost_usd === undefined ? "Cost unavailable" : `$${item.reported_cost_usd.toFixed(2)}`;
}

// stageLabel names where the Work is, in the form the card shows: how far
// through the pipeline, and which stage that is.
export function stageLabel(item: WorkItem): string {
  if (!item.stage_count) return "No pipeline stages";
  const position = `${item.completed_stage_count}/${item.stage_count}`;
  return item.current_stage ? `${position} · ${item.current_stage.name}` : position;
}

// RepositoryTab is one tab on the Work board, with the spend it can account
// for and the count of items it cannot. Both numbers matter: a total with no
// note of what it missed reads as complete when it is not.
export interface RepositoryTab {
  id: string;
  identity: string;
  count: number;
  costUSD?: number;
  unavailable: number;
}

export function repositoryTabs(items: WorkItem[]): RepositoryTab[] {
  const tabs = new Map<string, RepositoryTab>();
  for (const item of items) {
    let tab = tabs.get(item.repository_id);
    if (!tab) {
      tab = { id: item.repository_id, identity: item.repository_identity, count: 0, unavailable: 0 };
      tabs.set(item.repository_id, tab);
    }
    tab.count += 1;
    if (item.reported_cost_usd === undefined) tab.unavailable += 1;
    else tab.costUSD = (tab.costUSD ?? 0) + item.reported_cost_usd;
  }
  return [...tabs.values()];
}

// costSummary states what is known and, separately, what is not. It never
// implies a total is complete when some items reported nothing.
export function costSummary(items: WorkItem[]): string {
  const measured = items.filter((item) => item.reported_cost_usd !== undefined);
  const unavailable = items.length - measured.length;
  if (!measured.length) {
    return unavailable ? `Cost unavailable for all ${unavailable}` : "";
  }
  const total = measured.reduce((sum, item) => sum + (item.reported_cost_usd ?? 0), 0);
  const reported = `$${total.toFixed(2)} reported`;
  return unavailable ? `${reported} · ${unavailable} unavailable` : reported;
}

// workerLabel prefers the Worker's name and falls back to a short form of its
// id, because a full UUID overruns the chip and tells an operator nothing.
export function workerLabel(item: { assigned_worker_name?: string; assigned_worker_id?: string }): string {
  if (item.assigned_worker_name) return item.assigned_worker_name;
  return item.assigned_worker_id ? item.assigned_worker_id.slice(0, 8) : "";
}

export const boardColumns: Array<{ key: string; label: string; hint: string }> = [
  { key: "queued", label: "Queued", hint: "Waiting to start" },
  { key: "running", label: "Running", hint: "Agents at work" },
  { key: "attention", label: "Needs attention", hint: "Stopped on you" },
  { key: "done", label: "Done", hint: "Finished work" },
];

// Each column names the states it holds rather than falling through to a
// catch-all, so a state that belongs in no column is visibly missing instead
// of quietly appearing in two.
const columnStates: Record<string, ReadonlyArray<WorkItem["state"]>> = {
  queued: ["draft", "queued", "blocked"],
  running: ["preparing", "running"],
  done: ["ready", "succeeded", "no-change", "failed", "cancelled", "needs-input"],
};

export function workInColumn(item: WorkItem, column: string): boolean {
  // Attention takes precedence over every other column: Work stopped on a
  // person must appear exactly once, in the column an operator checks.
  if (column === "attention") return item.needs_attention;
  if (item.needs_attention) return false;
  return columnStates[column]?.includes(item.state) ?? false;
}

export function stageTokens(stage: { usage?: StageRun["usage"] }): number {
  const usage = stage.usage;
  if (!usage) return 0;
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) +
    (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
}

export function byteLabel(value: string): string {
  const bytes = new TextEncoder().encode(value).length;
  return bytes < 1024 ? `${bytes} bytes` : `${(bytes / 1024).toFixed(1)} KiB`;
}
