import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api, APIError } from "./api";
import { WorkDetailView } from "./WorkDetail";
import type { Session, StageRun, VerificationCheck, WorkDetail } from "./types";

function stage(overrides: Partial<StageRun> = {}): StageRun {
  return { position: 0, name: "Implement", kind: "agent", state: "succeeded", ...overrides };
}

function workDetail(overrides: Partial<WorkDetail> = {}): WorkDetail {
  const work: Session = {
    id: "work-1",
    run_id: "run-1",
    repository_id: "repository-factory",
    repository_identity: "github.com/example/factory",
    required_runtime: "claude-code",
    execution: {
      profile_id: "persistent-auto", profile_version: 1, backend: "persistent",
      runtime: "claude-code", provider: "worker", model: "worker-default",
      timeout_seconds: 3600, resource_class: "worker", commit_resolution_policy: "resolve_per_attempt",
    },
    timeout_seconds: 3600,
    state: "running",
    cancellation_requested: false,
    retry_may_repeat_effects: false,
    admitted_at: "2026-09-04T12:00:00Z",
    started_at: "2026-09-04T12:01:00Z",
    assigned_worker_id: "11111111-1111-4111-8111-111111111111",
    stages: [
      stage({ position: 0, name: "Implement", result: "Guarded the claim lease.", cost_usd: 0.12 }),
      stage({ position: 1, name: "Review", state: "running", model: "sonnet" }),
    ],
    ...overrides.work,
  };
  return {
    work,
    run_id: "run-1",
    task_id: "task-1",
    task_name: "Fix worker queue lease guard",
    worker_name: "midnight-otter",
    source: "orchestrator",
    verification: { recorded_checks: 0, passed: 0, failed: 0, unknown: 0 },
    cost: { unavailable_stages: 0 },
    needs_attention: false,
    updated_at: "2026-09-04T12:06:00Z",
    ...overrides,
  };
}

function renderDetail(detail: WorkDetail) {
  vi.spyOn(api, "workDetail").mockResolvedValue(detail);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onRun = vi.fn();
  const onWork = vi.fn();
  const onMissing = vi.fn();
  render(<QueryClientProvider client={client}>
    <WorkDetailView id="work-1" onBack={() => undefined} onRun={onRun} onWork={onWork} onMissing={onMissing} />
  </QueryClientProvider>);
  return { onRun, onWork, onMissing };
}

describe("Work detail", () => {
  // Acceptance criterion: opening a Work item never begins with a wall of logs.
  it("opens on the brief, not on raw output", async () => {
    renderDetail(workDetail({
      brief: { context: "Worker claim routing", why: "Queued work is stalled", risk: "High" },
    }));
    const brief = await screen.findByRole("tab", { name: "Brief" });
    expect(brief).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Worker claim routing")).toBeVisible();
    // The implement stage's raw result is not on screen until Evidence is opened.
    expect(screen.queryByText("Guarded the claim lease.")).toBeNull();
  });

  // Factory does not manufacture a brief for Work that arrived without one.
  it("shows operational facts alone when there is no brief", async () => {
    renderDetail(workDetail());
    expect(await screen.findByRole("heading", { name: "Where it stands" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Brief" })).toBeNull();
  });

  // A UUID tells an operator nothing, so the readable name is what shows.
  it("names the active Worker and stage", async () => {
    renderDetail(workDetail());
    expect(await screen.findByText("midnight-otter")).toBeVisible();
    expect(screen.queryByText("11111111-1111-4111-8111-111111111111")).toBeNull();
    expect(screen.getByText("1 / 2 · Review")).toBeVisible();
  });

  // A Worker row that has gone missing still leaves a correlatable id.
  it("falls back to a short Worker id when no name is known", async () => {
    renderDetail(workDetail({ worker_name: undefined }));
    expect(await screen.findByText("11111111")).toBeVisible();
  });

  it("links to the parent Run and its sibling Work", async () => {
    const user = userEvent.setup();
    const { onRun, onWork } = renderDetail(workDetail({
      siblings: [{ id: "work-2", repository_identity: "github.com/example/site", state: "queued" }],
    }));
    await user.click(await screen.findByRole("button", { name: "run-1" }));
    expect(onRun).toHaveBeenCalledWith("run-1");
    await user.click(screen.getByRole("button", { name: "site" }));
    expect(onWork).toHaveBeenCalledWith("work-2");
  });

  it("shows what one stage passed to the next, and what it did not", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail({
      handoffs: [{
        from_stage: 0, to_stage: 1, kind: "agent-result", from_state: "succeeded",
        summary: "Guarded the claim lease; go test passed.", truncated: false, delivered: true,
      }],
    }));
    await user.click(await screen.findByRole("tab", { name: /Stages/ }));
    // The graph names every state in words, not by colour alone.
    expect(screen.getByText("✓ Succeeded")).toBeVisible();
    expect(screen.getByText("● Running")).toBeVisible();

    await user.click(screen.getByRole("button", { name: /View evidence passed from Implement/ }));
    expect(screen.getByText("Guarded the claim lease; go test passed.")).toBeVisible();
    // Never called a conversation: stages share a worktree, not a channel.
    expect(screen.getByText("Stage handoff")).toBeVisible();
  });

  it("distinguishes a stage that never delivered from one that delivered nothing", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail({
      handoffs: [{
        from_stage: 0, to_stage: 1, kind: "agent-result", from_state: "failed",
        summary: "", truncated: false, delivered: false,
      }],
    }));
    await user.click(await screen.findByRole("tab", { name: /Stages/ }));
    await user.click(screen.getByRole("button", { name: /No evidence passed from Implement/ }));
    expect(screen.getByText(/did not complete, so no evidence reached the next stage/)).toBeVisible();
  });

  // Verification counts checks, never tests, and labels who vouches for each.
  it("reports verification without inventing a test count", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail({
      verification: {
        recorded_checks: 2, passed: 1, failed: 1, unknown: 0,
        items: [
          { name: "go test ./internal/controlplane", source: "code-stage", state: "passed" },
          { name: "npm run lint", source: "code-stage", state: "failed", detail: "exit status 1" },
        ],
      },
    }));
    await user.click(await screen.findByRole("tab", { name: "Outcome" }));
    expect(screen.getByText("2 recorded · 1 passed · 1 failed")).toBeVisible();
    expect(screen.getByText("go test ./internal/controlplane")).toBeVisible();
    expect(screen.getByText("exit status 1")).toBeVisible();
    expect(screen.getByText(/not of test cases/)).toBeVisible();
  });

  // A code stage and an agent's report can name the same command with the same
  // state. Keyed on name and state alone those two rows collide, and React
  // reconciles them wrongly when the list changes under a poll, leaving a row
  // showing another row's detail. Asserting on the first render alone would
  // not catch it: duplicate keys still render, they just reconcile badly.
  it("keeps a code-stage and agent-reported check for the same command apart across a poll", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const collide: VerificationCheck[] = [
        { name: "go test ./...", source: "code-stage", state: "failed", detail: "exit status 1" },
        { name: "go test ./...", source: "agent-reported", state: "failed", detail: "3 packages failed" },
      ];
      const verification = (items: VerificationCheck[]) => workDetail({
        verification: { recorded_checks: items.length, passed: 0, failed: items.length, unknown: 0, items },
      });
      vi.spyOn(api, "workDetail")
        .mockResolvedValueOnce(verification(collide))
        // The next poll prepends a check, which is what forces reconciliation.
        .mockResolvedValue(verification([
          { name: "npm run lint", source: "code-stage", state: "failed", detail: "eslint: 2 errors" },
          ...collide,
        ]));
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(<QueryClientProvider client={client}>
        <WorkDetailView id="work-1" onBack={() => undefined} onRun={() => undefined} onWork={() => undefined} onMissing={() => undefined} />
      </QueryClientProvider>);
      await user.click(await screen.findByRole("tab", { name: "Outcome" }));
      await vi.advanceTimersByTimeAsync(4_000);
      await screen.findByText("npm run lint");

      // Each row must still carry its own source and its own detail.
      const rows = [...document.querySelectorAll(".verify-row")].map((row) => ({
        name: row.querySelector(".name")?.textContent,
        source: row.querySelector(".src")?.textContent,
        detail: row.querySelector(".st")?.textContent,
      }));
      expect(rows).toEqual([
        { name: "npm run lint", source: "code stage", detail: "eslint: 2 errors" },
        { name: "go test ./...", source: "code stage", detail: "exit status 1" },
        { name: "go test ./...", source: "agent reported", detail: "3 packages failed" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("says nothing was verified when no code stage ran", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail());
    await user.click(await screen.findByRole("tab", { name: "Outcome" }));
    expect(screen.getByText(/recorded no code stages/)).toBeVisible();
  });

  // Cost: n/a for a stage that reaches no model, unavailable for one that ran
  // and reported nothing, and never $0.00 for either.
  it("separates cost that cannot exist from cost that was not reported", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail({
      cost: {
        total_usd: 0.12,
        unavailable_stages: 1,
        by_stage: [
          { position: 0, name: "Implement", kind: "agent", model: "sonnet", cost_usd: 0.12 },
          { position: 1, name: "Review", kind: "agent", model: "sonnet" },
          { position: 2, name: "Test", kind: "code" },
        ],
        by_attempt: [
          { attempt_number: 1, state: "failed", cost_usd: 0.44 },
          { attempt_number: 2, state: "succeeded", cost_usd: 0.12 },
        ],
      },
    }));
    await user.click(await screen.findByRole("tab", { name: "Outcome" }));
    expect(screen.getByText("n/a")).toBeVisible();
    expect(screen.getAllByText("unavailable").length).toBeGreaterThan(0);
    expect(screen.queryByText("$0.00")).toBeNull();
    expect(screen.getByText(/reported no cost, so this total is partial/)).toBeVisible();
    // A retry is not free and the failed attempt's spend stays visible.
    expect(screen.getByText("$0.44")).toBeVisible();
  });

  it("keeps raw output under Evidence, collapsed", async () => {
    const user = userEvent.setup();
    renderDetail(workDetail());
    await user.click(await screen.findByRole("tab", { name: "Evidence" }));
    const summary = screen.getByText(/Implement — raw result/);
    expect(summary).toBeVisible();
    // Collapsed by default: the text exists but the disclosure is shut.
    expect(summary.closest("details")).not.toHaveAttribute("open");
    await user.click(summary);
    expect(within(summary.closest("details")!).getByText("Guarded the claim lease.")).toBeVisible();
  });
});

// A link saved when /work/<id> meant a Run must still reach that Run rather
// than an error page. Both ids are UUIDs, so this is only knowable from the
// server's answer.
it("sends a stale /work/<run-id> link to its Run", async () => {
  vi.spyOn(api, "workDetail").mockRejectedValue(new APIError("work_not_found", "no Work matches", 404));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onMissing = vi.fn();
  render(<QueryClientProvider client={client}>
    <WorkDetailView id="run-1" onBack={() => undefined} onRun={() => undefined}
      onWork={() => undefined} onMissing={onMissing} />
  </QueryClientProvider>);
  await waitFor(() => expect(onMissing).toHaveBeenCalledWith("run-1"));
  // The operator never sees the error page on the way there.
  expect(screen.queryByRole("alert")).toBeNull();
});

// Any other failure is a real error and must still surface.
it("shows an error when the request fails for another reason", async () => {
  vi.spyOn(api, "workDetail").mockRejectedValue(new APIError("storage_unavailable", "database is down", 503));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onMissing = vi.fn();
  render(<QueryClientProvider client={client}>
    <WorkDetailView id="work-1" onBack={() => undefined} onRun={() => undefined}
      onWork={() => undefined} onMissing={onMissing} />
  </QueryClientProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
  expect(onMissing).not.toHaveBeenCalled();
});
