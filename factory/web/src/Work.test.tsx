import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { WorkView } from "./Work";
import type { WorkItem } from "./types";

function workItem(overrides: Partial<WorkItem> = {}): WorkItem {
  return {
    id: "work-1",
    run_id: "run-1",
    task_id: "task-1",
    task_name: "Fix worker queue lease guard",
    repository_id: "repository-factory",
    repository_identity: "github.com/example/factory",
    state: "running",
    source: "orchestrator",
    needs_attention: false,
    planning: false,
    stage_count: 4,
    completed_stage_count: 2,
    attempt_count: 1,
    admitted_at: "2026-09-04T12:00:00Z",
    updated_at: "2026-09-04T12:06:00Z",
    ...overrides,
  };
}

function renderBoard(items: WorkItem[]) {
  vi.spyOn(api, "work").mockResolvedValue({ work: items, next_cursor: null });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onWork = vi.fn();
  render(<QueryClientProvider client={client}>
    <WorkView mode="board" onMode={() => undefined} onWork={onWork} />
  </QueryClientProvider>);
  return onWork;
}

describe("Work board", () => {
  // The whole point of the change: one Run across three repositories is three
  // independently clickable cards, and a repository tab shows only its own.
  it("lists a multi-repository Run as one card per repository", async () => {
    const user = userEvent.setup();
    const onWork = renderBoard([
      workItem({ id: "work-factory", repository_id: "repository-factory", repository_identity: "github.com/example/factory" }),
      workItem({ id: "work-orchestrator", repository_id: "repository-orchestrator", repository_identity: "github.com/example/chao-orchestrator" }),
      workItem({ id: "work-site", repository_id: "repository-site", repository_identity: "github.com/example/site" }),
    ]);

    const running = await screen.findByRole("region", { name: "Work summary" });
    expect(within(running).getByText("Running").parentElement).toHaveTextContent("3");

    const tabs = screen.getByRole("tablist", { name: "Repository" });
    expect(within(tabs).getByRole("tab", { name: /All repositories/ })).toHaveTextContent("3");

    await user.click(within(tabs).getByRole("tab", { name: /factory/ }));
    expect(screen.getAllByRole("button", { name: /Fix worker queue lease guard/ })).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: /in factory/ }));
    expect(onWork).toHaveBeenCalledWith("work-factory");
  });

  it("shows the stage the Work is on while it runs", async () => {
    renderBoard([workItem({ current_stage: { position: 1, name: "Review", kind: "agent", state: "running", model: "sonnet" } })]);
    expect(await screen.findByText("2/4 · Review")).toBeVisible();
  });

  // Cost honesty: a runtime that reports nothing must never read as free.
  it("labels unreported cost rather than showing zero", async () => {
    renderBoard([
      workItem({ id: "work-costed", reported_cost_usd: 0.18 }),
      workItem({ id: "work-uncosted", repository_id: "repository-two", repository_identity: "github.com/example/two" }),
    ]);
    expect(await screen.findByText("$0.18")).toBeVisible();
    expect(screen.getByText("Cost unavailable")).toBeVisible();
    expect(screen.queryByText("$0.00")).toBeNull();
  });

  // A genuine zero is a measurement and prints as one.
  it("shows a reported zero cost as a figure", async () => {
    renderBoard([workItem({ reported_cost_usd: 0 })]);
    expect(await screen.findByText("$0.00")).toBeVisible();
    expect(screen.queryByText("Cost unavailable")).toBeNull();
  });

  it("puts Work needing a person in the attention column", async () => {
    renderBoard([workItem({ state: "needs-input", needs_attention: true, current_stage: undefined })]);
    const column = await screen.findByRole("region", { name: "Needs attention" });
    expect(within(column).getByRole("button", { name: /Fix worker queue lease guard/ })).toBeVisible();
  });

  // Terminal cards lead with what was verified, not with raw output. Counts
  // are of checks, never of tests.
  it("shows failed verification on a terminal card", async () => {
    renderBoard([workItem({
      state: "failed",
      terminal_at: "2026-09-04T12:30:00Z",
      verification: { recorded_checks: 4, passed: 3, failed: 1, unknown: 0 },
    })]);
    expect(await screen.findByText("1 of 4 checks failed")).toBeVisible();
  });

  it("says so when a terminal card recorded no checks", async () => {
    renderBoard([workItem({ state: "succeeded", terminal_at: "2026-09-04T12:30:00Z" })]);
    expect(await screen.findByText("No recorded checks")).toBeVisible();
  });

  it("shows the brief preview when the orchestrator supplied one", async () => {
    renderBoard([workItem({ brief: { context: "Worker claim routing", why: "Queued work is stalled" } })]);
    expect(await screen.findByText("Worker claim routing · Queued work is stalled")).toBeVisible();
  });

  // The board's cost line reports what it knows and, separately, what it does
  // not. A total with no note of what it missed reads as complete.
  it("says how much of the board's cost it could not see", async () => {
    renderBoard([
      workItem({ id: "work-a", reported_cost_usd: 0.18 }),
      workItem({ id: "work-b", reported_cost_usd: 0.12 }),
      workItem({ id: "work-c" }),
    ]);
    expect(await screen.findByText("$0.30 reported · 1 unavailable")).toBeVisible();
  });

  it("says so when nothing on the board reported a cost", async () => {
    renderBoard([workItem({ id: "work-a" }), workItem({ id: "work-b" })]);
    expect(await screen.findByText("Cost unavailable for all 2")).toBeVisible();
  });

  // A refetch of an infinite query refetches every page it holds, so an
  // unconditional poll would issue one request per loaded page every five
  // seconds, growing with each Load more. The poll stops once the board is
  // showing history; Refresh still works and the header still shows staleness.
  it("stops polling once history is loaded, so the poll cost stays bounded", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const work = vi.spyOn(api, "work").mockImplementation(async (options = {}) => ({
        work: [workItem({ id: `work-${options.cursor || "first"}`, task_name: `Page ${options.cursor || "first"}` })],
        next_cursor: options.cursor ? null : "cursor-2",
      }));
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(<QueryClientProvider client={client}>
        <WorkView mode="table" onMode={() => undefined} onWork={() => undefined} />
      </QueryClientProvider>);
      await screen.findByText("Page first");

      // One page loaded: the board is live and polls.
      work.mockClear();
      await vi.advanceTimersByTimeAsync(11_000);
      expect(work.mock.calls.length).toBeGreaterThan(0);

      await user.click(screen.getByRole("button", { name: "Load more Work" }));
      await screen.findByText("Page cursor-2");
      expect(screen.getByText(/Live updates are paused/)).toBeVisible();

      // History loaded: no further automatic requests at all.
      work.mockClear();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(work.mock.calls.length).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  // Pagination stays bounded: the board asks for one page and offers more.
  it("loads more Work one bounded page at a time", async () => {
    const user = userEvent.setup();
    const work = vi.spyOn(api, "work")
      .mockResolvedValueOnce({ work: [workItem({ id: "work-page-1" })], next_cursor: "cursor-2" })
      .mockResolvedValueOnce({ work: [workItem({ id: "work-page-2", task_name: "Second page item" })], next_cursor: null });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <WorkView mode="board" onMode={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    await user.click(await screen.findByRole("button", { name: "Load more Work" }));
    expect(await screen.findByRole("button", { name: /Second page item/ })).toBeVisible();
    expect(work).toHaveBeenLastCalledWith({ cursor: "cursor-2", limit: 100, planning: "all" });
  });

  // Planning passes crowd a board meant for builds, and the Planning page
  // already shows them in more detail than a card can. Hidden by default,
  // visible on request, and labelled on its face when shown.
  it("hides planning work until the toolbar control is used", async () => {
    const user = userEvent.setup();
    renderBoard([
      workItem({ id: "work-build", task_name: "Build the thing" }),
      workItem({ id: "work-planning", task_name: "Critique checkpoint 2", planning: true }),
    ]);

    await screen.findByRole("button", { name: /Build the thing/ });
    expect(screen.queryByText(/Critique checkpoint 2/)).toBeNull();

    const toggle = screen.getByRole("button", { name: /Show planning/ });
    expect(toggle).toHaveTextContent("1");

    await user.click(toggle);
    const planningCard = await screen.findByRole("button", { name: /Critique checkpoint 2/ });
    expect(within(planningCard).getByText("Planning")).toBeVisible();
  });
});
