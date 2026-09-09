import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { Drafts, DraftsView } from "./Drafts";
import type { Run } from "./types";

describe("Drafts", () => {
  it("lists draft Work and approves a row", async () => {
    const approve = vi.fn();

    render(<Drafts drafts={[
      { id: "work-1", name: "Add a farewell function", repository: "github.com/example/scratch" },
    ]} onApprove={approve} />);

    expect(screen.getByText("Add a farewell function")).toBeVisible();
    expect(screen.getByText("github.com/example/scratch")).toBeVisible();

    await userEvent.click(screen.getByRole("button", { name: "Approve Add a farewell function" }));
    expect(approve).toHaveBeenCalledWith("work-1");
  });

  // Every row this view can show was created by admission, and admission has
  // to uniquify tasks.name. Rendering that raw name puts an internal hash in
  // front of the human standing at the single approval gate.
  it("shows the submitted name rather than the deduplicated Task name", async () => {
    vi.spyOn(api, "draftRuns").mockResolvedValue([draftRun()]);
    const client = testClient();

    render(<QueryClientProvider client={client}><DraftsView /></QueryClientProvider>);

    expect(await screen.findByText("Add a farewell function")).toBeVisible();
    expect(screen.queryByText(/3d9c1a77/)).toBeNull();
  });

  // Only admission records a name distinct from the stored one, so an empty
  // submitted name is the ordinary case rather than a fault, and the Task
  // name is then the only name there is.
  it("falls back to the Task name when no submitted name was recorded", async () => {
    const run = draftRun();
    vi.spyOn(api, "draftRuns").mockResolvedValue([
      { ...run, task: { ...run.task, name: "Rotate the signing key", submitted_name: "" } },
    ]);
    const client = testClient();

    render(<QueryClientProvider client={client}><DraftsView /></QueryClientProvider>);

    expect(await screen.findByRole("button", { name: "Approve Rotate the signing key" })).toBeVisible();
  });

  it("says so when nothing is waiting for approval", () => {
    render(<Drafts drafts={[]} onApprove={vi.fn()} />);

    expect(screen.getByText(/No drafts waiting for approval/)).toBeVisible();
  });

  it("records the cockpit as the approving channel", async () => {
    vi.spyOn(api, "draftRuns").mockResolvedValue([draftRun()]);
    const approve = vi.spyOn(api, "approveWork").mockResolvedValue({} as never);
    const client = testClient();

    render(<QueryClientProvider client={client}><DraftsView /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Approve Add a farewell function" }));

    // INV-10 exists to produce a truthful approved_by record. The cockpit has
    // no operator identity, so it names itself rather than a human it never
    // authenticated.
    await waitFor(() => expect(approve).toHaveBeenCalledWith("work-1", "cockpit"));
  });

  // A second click lands on a row whose approval already succeeded, so the
  // server answers 409 "only draft Work can be approved" and the view reports
  // a failure under a row that was in fact approved.
  it("refuses a second approval while the first is still in flight", async () => {
    vi.spyOn(api, "draftRuns").mockResolvedValue([draftRun()]);
    let settle = () => {};
    const approve = vi.spyOn(api, "approveWork").mockImplementation(
      () => new Promise((resolve) => { settle = () => resolve({} as never); }),
    );
    const client = testClient();

    render(<QueryClientProvider client={client}><DraftsView /></QueryClientProvider>);
    const button = await screen.findByRole("button", { name: "Approve Add a farewell function" });
    await userEvent.click(button);

    await waitFor(() => expect(button).toBeDisabled());
    await userEvent.click(button);
    expect(approve).toHaveBeenCalledTimes(1);

    settle();
  });

  it("drops an approved row on the next refresh", async () => {
    const drafts = vi.spyOn(api, "draftRuns").mockResolvedValue([draftRun()]);
    vi.spyOn(api, "approveWork").mockImplementation(async () => {
      drafts.mockResolvedValue([]);
      return {} as never;
    });
    const client = testClient();

    render(<QueryClientProvider client={client}><DraftsView /></QueryClientProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Approve Add a farewell function" }));

    expect(await screen.findByText(/No drafts waiting for approval/)).toBeVisible();
  });
});

function testClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function draftRun(): Run {
  return {
    id: "run-draft",
    task_id: "task-1",
    task: {
      id: "task-1",
      // Admission uniquifies tasks.name because tasks.name_key is UNIQUE, so
      // this suffix is on every row the Drafts view can ever show.
      name: "Add a farewell function (3d9c1a77)",
      submitted_name: "Add a farewell function",
      prompt: "Done when farewell('world') returns Goodbye, world!",
      runtime: "claude-code",
      timeout_seconds: 3600,
      concurrency_limit: 1,
      generation: 1,
      repositories: [],
    },
    execution: {
      profile_id: "persistent-auto",
      profile_version: 1,
      backend: "persistent",
      runtime: "claude-code",
      provider: "worker",
      model: "worker-default",
      timeout_seconds: 3600,
      resource_class: "worker",
      commit_resolution_policy: "resolve_per_attempt",
    },
    targets: [{ id: "work-1", repository_identity: "github.com/example/scratch" }],
    source: "cockpit",
    state: "draft",
    needs_attention: false,
    session_count: 1,
    succeeded_count: 0,
    ready_count: 0,
    needs_input_count: 0,
    no_change_count: 0,
    failed_count: 0,
    cancelled_count: 0,
    active_count: 1,
    admitted_at: "2026-08-24T12:00:00Z",
    updated_at: "2026-08-24T12:00:00Z",
  };
}
