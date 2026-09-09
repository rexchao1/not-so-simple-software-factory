import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { api } from "./api";
import { App } from "./App";
import { RoadmapView, WaitingView } from "./Roadmap";
import type { LoadedRoadmap, RoadmapCheckpoint, RoadmapProject } from "./types";

function checkpoint(overrides: Partial<RoadmapCheckpoint> = {}): RoadmapCheckpoint {
  return {
    number: 1, title: "One payer brings itself up live", summary: "twenty logins on the first day",
    status: "built", planned: true, stones: [], pebbles: [], passes: [], cost_usd: 0, pass_rounds: 0, ...overrides,
  };
}

const driver = { ordinal: 1, slug: "01-live-driver", title: "Rebuild the live driver", summary: "The runner loses the driver on a resume.", state: "running" as const, work_id: "w-1" };
const publish = { ordinal: 2, slug: "02-publish", title: "Publish to the dashboard", state: "succeeded" as const, pull_request_url: "https://example.test/pr/2" };

function project(overrides: Partial<RoadmapProject> = {}): RoadmapProject {
  return {
    project: "payer", title: "a new payer onboards itself",
    statement: "Give it a new payer portal and it works, unattended.",
    checkpoints: [
      checkpoint(),
      checkpoint({
        number: 2, title: "The dashboard finishes a parked payer", status: "review", cost_usd: 3, pass_rounds: 2,
        stones: [
          { id: "B1", title: "Bring the driver back", statement: "The driver survives a resume.", pebbles: [driver], state: "working" },
          { id: "B2", title: "Show it on the dashboard", pebbles: [publish], state: "done" },
        ],
        pebbles: [driver, publish],
        passes: [
          { at: "2026-09-04T07:43:04Z", mode: "critique", round: 1, cost_usd: 2.25, outcome: "succeeded", work_id: "w-c1" },
          { at: "2026-09-04T07:48:46Z", mode: "critique", round: 2, cost_usd: 0.75, outcome: "succeeded", work_id: "w-c2" },
        ],
      }),
      checkpoint({ number: 3, title: "A payer's profile is data", status: "planned", planned: false, summary: "" }),
    ],
    cost_usd: 4.5, built_count: 1, ...overrides,
  };
}

function roadmap(overrides: Partial<LoadedRoadmap> = {}): LoadedRoadmap {
  return {
    configured: true,
    projects: [project()],
    waiting: [{
      project: "payer", number: 2, title: "The dashboard finishes a parked payer",
      status: "review", reason: "The plan is written and waiting for your answers.",
      action: "Review the plan", cost_usd: 3, pass_rounds: 2,
    }],
    read_at: "2026-09-05T09:00:00Z",
    ...overrides,
  };
}

function renderRoadmap(props: Partial<Parameters<typeof RoadmapView>[0]> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><RoadmapView
    onProject={() => {}} onView={() => {}} onWaiting={() => {}} onWork={() => {}} {...props}
  /></QueryClientProvider>);
}

it("lands on one card per project rather than every checkpoint at once", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap();

  const card = await screen.findByRole("button", { name: /a new payer onboards itself/ });
  expect(within(card).getByText("1 of 3 built")).toBeInTheDocument();
  expect(within(card).getByText("$4.50 planned")).toBeInTheDocument();
  expect(within(card).getByText("1 waiting on you")).toBeInTheDocument();
  // The individual checkpoint titles stay inside the project, so the landing
  // page cannot grow with them.
  expect(screen.queryByText("The dashboard finishes a parked payer")).not.toBeInTheDocument();
});

it("opens a project onto a horizontal checkpoint bar and its first unbuilt rung", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer" });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getAllByRole("button")).toHaveLength(3);
  const detail = screen.getByLabelText("Details");
  expect(within(detail).getByRole("heading", { name: "The dashboard finishes a parked payer" })).toBeInTheDocument();
  expect(within(detail).getByText("Waiting on you")).toBeInTheDocument();
  expect(within(detail).getByText("$3.00")).toBeInTheDocument();
});

it("shows the stones big, coloured by what the factory is doing with them", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 2 });

  const working = await screen.findByRole("button", { name: /Bring the driver back/ });
  expect(working.closest("section")).toHaveClass("stone-working");
  expect(within(working).getByText("Being built")).toBeInTheDocument();
  expect(within(working).getByText("0/1 done")).toBeInTheDocument();
  const done = screen.getByRole("button", { name: /Show it on the dashboard/ });
  expect(done.closest("section")).toHaveClass("stone-done");
  // Pebbles stay hidden until their stone is opened.
  expect(screen.queryByText("Rebuild the live driver")).not.toBeInTheDocument();
});

it("drops the pebbles under a stone and reads one on the right", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  const opened = vi.fn();
  renderRoadmap({ project: "payer", checkpoint: 2, onWork: opened });

  await userEvent.click(await screen.findByRole("button", { name: /Bring the driver back/ }));
  const pebble = screen.getByRole("button", { name: /Rebuild the live driver/ });
  await userEvent.click(pebble);

  const detail = screen.getByLabelText("Details");
  expect(within(detail).getByRole("heading", { name: "Rebuild the live driver" })).toBeInTheDocument();
  expect(within(detail).getByText("The runner loses the driver on a resume.")).toBeInTheDocument();
  expect(within(detail).getByText("01-live-driver")).toBeInTheDocument();
  await userEvent.click(within(detail).getByRole("button", { name: "Open the run" }));
  expect(opened).toHaveBeenCalledWith("w-1");
});

// A pebble built by hand, outside the factory, is not a Work row the page can
// open, but it is still finished: it shows the terminal state and, when the
// recorded ref is a URL, a link to it.
it("shows a pebble built outside the factory as finished, with its ref linked", async () => {
  const byHand = { ordinal: 3, slug: "03-migration", title: "Run the one-off migration", state: "built" as const, built_ref: "https://example.test/pr/9" };
  const live = project({
    checkpoints: [
      checkpoint(),
      checkpoint({
        number: 2, title: "The dashboard finishes a parked payer", status: "frozen",
        stones: [{ id: "B3", title: "Migrate the old rows", pebbles: [byHand], state: "done" }],
        pebbles: [byHand],
      }),
    ],
  });
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap({ projects: [live] }));
  renderRoadmap({ project: "payer", checkpoint: 2 });

  const stone = await screen.findByRole("button", { name: /Migrate the old rows/ });
  expect(stone.closest("section")).toHaveClass("stone-done");
  expect(within(stone).getByText("1/1 done")).toBeInTheDocument();

  await userEvent.click(stone);
  await userEvent.click(screen.getByRole("button", { name: /Run the one-off migration/ }));

  const detail = screen.getByLabelText("Details");
  expect(within(detail).getByText("built")).toBeInTheDocument();
  const link = within(detail).getByRole("link", { name: /Built as/ });
  expect(link).toHaveAttribute("href", "https://example.test/pr/9");
});

it("says a checkpoint has nothing to show rather than an empty stage", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 3 });

  expect(await screen.findByText("Still a line on the route. Nothing has been drafted for it yet.")).toBeInTheDocument();
});

it("keeps built checkpoints on the roadmap", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  renderRoadmap({ project: "payer", checkpoint: 1 });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getByText("One payer brings itself up live")).toBeInTheDocument();
  expect(within(screen.getByLabelText("Details")).getByText("Built")).toBeInTheDocument();
});

// There is nothing to configure any more: planning lives in the factory's own
// database. An unreadable roadmap is the server failing to read it, and the
// page says that rather than sending anyone to look for a setting.
it("says planning could not be read when the server cannot read it", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue({ configured: false, projects: [], waiting: [], read_at: "" });
  renderRoadmap();

  expect(await screen.findByText("Planning could not be read")).toBeInTheDocument();
});

it("lists what is waiting with the action to take", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const opened = vi.fn();
  render(<QueryClientProvider client={client}><WaitingView onProject={opened} /></QueryClientProvider>);

  const card = await screen.findByRole("button", { name: /The dashboard finishes a parked payer/ });
  expect(within(card).getByText("The plan is written and waiting for your answers.")).toBeInTheDocument();
  await userEvent.click(card);
  expect(opened).toHaveBeenCalledWith("payer", 2);
});

it("rings one red bell in the top bar and routes into a project", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap());
  vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
  vi.spyOn(api, "workers").mockResolvedValue([]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);

  const bell = await screen.findByLabelText("1 thing waiting for you");
  expect(bell).toHaveTextContent("1");
  expect(bell).toHaveClass("ringing");
  await userEvent.click(screen.getByRole("button", { name: "Roadmap" }));
  await userEvent.click(await screen.findByRole("button", { name: /a new payer onboards itself/ }));
  expect(window.location.pathname).toBe("/roadmap/payer");
  await userEvent.click(bell);
  expect(window.location.pathname).toBe("/waiting");
});

it("has no Pipelines page left to navigate to", async () => {
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap({ waiting: [] }));
  vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
  vi.spyOn(api, "workers").mockResolvedValue([]);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><App /></QueryClientProvider>);

  expect(await screen.findByRole("button", { name: "Drafts" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Pipelines" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Planning" })).toBeInTheDocument();
});

it("says on the checkpoint bar which checkpoint a pass is turning on", async () => {
  const live = project({
    checkpoints: [
      checkpoint(),
      checkpoint({ number: 2, title: "The dashboard finishes a parked payer", status: "review",
        live: { mode: "critique", round: 3, model: "claude-opus-5", started: "2026-09-05T08:58:00Z", work_id: "w-live" } }),
    ],
  });
  vi.spyOn(api, "roadmap").mockResolvedValue(roadmap({ projects: [live] }));
  renderRoadmap({ project: "payer" });

  const bar = await screen.findByRole("navigation", { name: "Checkpoints" });
  expect(within(bar).getByText("Critiquing \u00b7 round 3")).toBeInTheDocument();
  // The status word is what the plan says; the badge is what is happening. The
  // one that is not moving keeps its word.
  expect(within(bar).getByText("Built")).toBeInTheDocument();
  expect(within(bar).queryByText("Waiting on you")).not.toBeInTheDocument();
});
