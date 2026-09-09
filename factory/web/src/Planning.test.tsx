import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { PlanningView } from "./Planning";
import type {
  LoadedRoadmap, PlanningCheckpoint, RoadmapCheckpoint, RoadmapLivePass, RoadmapWaiting,
} from "./types";

function checkpoint(overrides: Partial<RoadmapCheckpoint> = {}): RoadmapCheckpoint {
  return {
    number: 1, title: "One payer brings itself up live", summary: "twenty logins on the first day",
    status: "planned", planned: false, stones: [], pebbles: [], passes: [], cost_usd: 0, pass_rounds: 0, ...overrides,
  };
}

// The waiting list is the server's, derived on every read. The page reads it
// rather than working the rule out again, so a fixture that leaves it out is a
// fixture where nothing is waiting, whatever the statuses say.
function waitingEntry(overrides: Partial<RoadmapWaiting> = {}): RoadmapWaiting {
  return {
    project: "payer", number: 2, title: "The dashboard finishes a parked payer",
    status: "review", reason: "The plan is written and waiting for your answers.",
    action: "Review the plan", cost_usd: 3, pass_rounds: 2, ...overrides,
  };
}

function roadmap(
  checkpoints: RoadmapCheckpoint[],
  waiting: RoadmapWaiting[] = [],
  live: RoadmapLivePass | null = null,
): LoadedRoadmap {
  return {
    configured: true,
    projects: [{ project: "payer", title: "a new payer onboards itself", checkpoints, live, cost_usd: 4.5, built_count: 0 }],
    waiting,
    read_at: "2026-09-05T09:00:00Z",
  };
}

// The critique Work item that has been submitted and has not come back. It is
// a Work row, so it carries the id the page sends the operator to.
function running(overrides: Partial<RoadmapLivePass> = {}): RoadmapLivePass {
  return {
    mode: "critique", round: 2, model: "claude-opus-5",
    started: "2026-09-05T08:58:00Z", work_id: "w_live", ...overrides,
  };
}

function plan(overrides: Partial<PlanningCheckpoint> = {}): PlanningCheckpoint {
  return {
    project: "payer", number: 2, title: "The dashboard finishes a parked payer",
    status: "review", body: "# Checkpoint 2\n\nThe office fills one field into Settings.",
    created_at: "2026-09-04T07:00:00Z", updated_at: "2026-09-05T08:00:00Z", ...overrides,
  };
}

function renderPlanning(data: LoadedRoadmap, checkpointPlan: PlanningCheckpoint = plan()) {
  vi.spyOn(api, "roadmap").mockResolvedValue(data);
  vi.spyOn(api, "planningCheckpoint").mockResolvedValue(checkpointPlan);
  const onProject = vi.fn();
  const onWork = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><PlanningView onProject={onProject} onWork={onWork} /></QueryClientProvider>);
  return { onProject, onWork };
}

const reviewing = checkpoint({
  number: 2, title: "The dashboard finishes a parked payer", status: "review", planned: true,
  live: running(), cost_usd: 3, pass_rounds: 2,
  passes: [
    { at: "2026-09-04T07:43:04Z", mode: "critique", round: 1, model: "claude-opus-5", cost_usd: 2.25, duration_ms: 92000, outcome: "succeeded", work_id: "w_round1" },
    { at: "2026-09-04T07:48:46Z", mode: "critique", round: 2, model: "claude-opus-5", cost_usd: 0.75, outcome: "running", work_id: "w_live" },
  ],
});

it("shows the checkpoint being critiqued, its rail, and every round it has had", async () => {
  renderPlanning(roadmap([reviewing]));

  expect(await screen.findByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  expect(screen.getByRole("img", { name: "A critique is running" })).toBeInTheDocument();
  expect(screen.getByText("$3.00 · 2 rounds")).toBeInTheDocument();
  // The critique turning right now is the stop it is standing on, not the
  // Review its status still says.
  const rail = screen.getByRole("list", { name: "Planning stages" });
  expect(within(rail).getByText("Critique").closest("li")).toHaveClass("here");
  expect(screen.getAllByText("Critiquing · round 2").length).toBeGreaterThan(0);

  // Round, model, cost, how long it took, and how it ended.
  const rounds = screen.getByRole("table", { name: "Critique rounds" });
  expect(within(rounds).getByText("Round 1")).toBeInTheDocument();
  expect(within(rounds).getAllByText("claude-opus-5").length).toBe(2);
  expect(within(rounds).getByText("$2.25")).toBeInTheDocument();
  expect(within(rounds).getByText("1m 32s")).toBeInTheDocument();
});

it("sends the live pass to the Work it is running as", async () => {
  const { onWork } = renderPlanning(roadmap([reviewing]));

  const links = await screen.findAllByRole("button", { name: "Critiquing · round 2" });
  await userEvent.click(links[0]);
  expect(onWork).toHaveBeenCalledWith("w_live");
});

it("sends a finished round to the Work that ran it", async () => {
  const { onWork } = renderPlanning(roadmap([reviewing]));

  const rounds = await screen.findByRole("table", { name: "Critique rounds" });
  await userEvent.click(within(rounds).getAllByRole("button", { name: "Open the Work" })[0]);
  expect(onWork).toHaveBeenCalledWith("w_round1");
});

it("keeps a checkpoint on the page while a critique is turning on it", async () => {
  renderPlanning(roadmap([checkpoint({
    number: 2, title: "The dashboard finishes a parked payer", status: "frozen", planned: true,
    live: running({ round: 1 }),
    pebbles: [{ ordinal: 1, slug: "01-a", title: "A pebble" }],
  })]));

  // Frozen with pebbles is normally finished planning. A critique turning on
  // it says otherwise, and the running Work is the thing that cannot be out of
  // date.
  expect(await screen.findByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  const rail = screen.getByRole("list", { name: "Planning stages" });
  expect(within(rail).getByText("Critique").closest("li")).toHaveClass("here");
});

it("does not call a checkpoint live on the strength of its status word", async () => {
  renderPlanning(roadmap([checkpoint({ number: 2, title: "In review, nothing running", status: "review", planned: true })]));

  expect(await screen.findByRole("heading", { name: "In review, nothing running" })).toBeInTheDocument();
  expect(screen.queryByRole("img", { name: "A critique is running" })).not.toBeInTheDocument();
});

it("counts one running critique once, not again for its project", async () => {
  // The project carries the same live pass its checkpoint does. Counting both
  // would tell the human two critics were running when one is.
  renderPlanning(roadmap([reviewing], [], running()));

  expect(await screen.findByText("1 critique running")).toBeInTheDocument();
  expect(screen.queryByText("2 critiques running")).not.toBeInTheDocument();
});

it("leaves a checkpoint that is only a line on the route off the page", async () => {
  renderPlanning(roadmap([checkpoint()]));

  expect(await screen.findByText("Nothing is being planned")).toBeInTheDocument();
});

it("leaves a frozen checkpoint that already has pebbles off the page", async () => {
  renderPlanning(roadmap([checkpoint({
    number: 2, status: "frozen", planned: true,
    pebbles: [{ ordinal: 1, slug: "01-a", title: "A pebble" }],
  })]));

  expect(await screen.findByText("Nothing is being planned")).toBeInTheDocument();
});

it("says nothing has been planned when there is no project at all", async () => {
  renderPlanning({ configured: true, projects: [], waiting: [], read_at: "2026-09-05T09:00:00Z" });

  expect(await screen.findByText("Nothing has been planned yet")).toBeInTheDocument();
});

it("takes what is waiting from the server's list rather than the status word", async () => {
  // Both are in review. Only one is on the waiting list, because the other has
  // answers saved. The page reads the list the bell counts, so the two agree.
  const answered = checkpoint({ number: 3, title: "A payer's profile is data", status: "review", planned: true });
  renderPlanning(roadmap([answered, reviewing], [waitingEntry()]));

  expect(await screen.findByText("Review the plan")).toBeInTheDocument();
  const answeredChip = screen.getByText("3. A payer's profile is data").closest("button");
  expect(answeredChip).not.toHaveClass("stalled");
  const waitingChip = screen.getByText("2. The dashboard finishes a parked payer").closest("button");
  expect(waitingChip).toHaveClass("stalled");
});

it("puts what is actually turning first and switches between them", async () => {
  const stuck = checkpoint({ number: 3, title: "A payer's profile is data", status: "fog", planned: true });
  renderPlanning(roadmap([stuck, reviewing], [waitingEntry({
    number: 3, title: "A payer's profile is data", status: "fog",
    reason: "Drafting stopped on questions it could not answer from the repository.",
    action: "Answer the questions",
  })]));

  const strip = (await screen.findByText("2. The dashboard finishes a parked payer")).closest("button");
  expect(strip).toHaveClass("active");

  await userEvent.click(screen.getByText("3. A payer's profile is data"));
  expect(screen.getByRole("heading", { name: "A payer's profile is data" })).toBeInTheDocument();
  expect(screen.getByText("Answer the questions")).toBeInTheDocument();
});

it("shows the PRD and the saved answers, and offers no way to change them", async () => {
  renderPlanning(roadmap([reviewing]), plan({ answers: "Q1: rotate weekly. Q2: no SSO." }));

  expect(await screen.findByText(/The office fills one field into Settings/)).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "What you said at review" })).toBeInTheDocument();
  expect(screen.getByText("Q1: rotate weekly. Q2: no SSO.")).toBeInTheDocument();
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /save|edit|answer the plan/i })).not.toBeInTheDocument();
});

it("leaves out the answers block when nothing was said", async () => {
  renderPlanning(roadmap([reviewing]));

  expect(await screen.findByRole("heading", { name: "The plan" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "What you said at review" })).not.toBeInTheDocument();
});

it("hands a checkpoint back to the roadmap", async () => {
  const { onProject } = renderPlanning(roadmap([reviewing]));

  await userEvent.click(await screen.findByRole("button", { name: "Open on the roadmap" }));
  expect(onProject).toHaveBeenCalledWith("payer", 2);
});
