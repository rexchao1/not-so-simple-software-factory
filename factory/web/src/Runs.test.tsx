import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { RunDetailView } from "./Runs";
import type { AttemptEvent, RunDetail, Run } from "./types";

const headRun = run("run-head", "Current Run", "running");

describe("Run detail", () => {
  it("uses the same successful Session total in Run detail stats", async () => {
    const detail = runDetail("succeeded");
    detail.run = {
      ...run("run-completed", "Review complete", "succeeded"),
      session_count: 2,
      succeeded_count: 0,
      ready_count: 1,
      no_change_count: 1,
      terminal_at: "2026-08-11T12:05:00Z",
    };
    vi.spyOn(api, "run").mockResolvedValue(detail);
    const client = testClient();

    render(<QueryClientProvider client={client}><RunDetailView id={detail.run.id} onBack={() => undefined} /></QueryClientProvider>);

    const summary = await screen.findByText("Completed");
    expect(summary.parentElement).toHaveTextContent("Completed2");
  });

  it("polls active attempt output after the last cached event", async () => {
    const eventZero: AttemptEvent = {
      sequence: 0,
      kind: "progress",
      payload: "First update",
      server_time: "2026-08-11T12:00:00Z",
    };
    const eventOne: AttemptEvent = {
      sequence: 1,
      kind: "progress",
      payload: "Second update",
      server_time: "2026-08-11T12:00:01Z",
    };
    vi.spyOn(api, "run").mockResolvedValue(runDetail());
    const events = vi.spyOn(api, "events").mockImplementation(async (_attemptID, after) => after < 0 ? {
      events: [eventZero], next_after: 0, has_more: false,
    } : {
      events: after === 0 ? [eventOne] : [], next_after: Math.max(after, 1), has_more: false,
    });
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByText("github.com/example/factory"));
    expect(await screen.findByText("First update")).toBeVisible();
    expect(events).toHaveBeenLastCalledWith("attempt-1", -1);

    await client.refetchQueries({ queryKey: ["attempt-events", "attempt-1"] });

    expect(await screen.findByText("Second update")).toBeVisible();
    expect(events).toHaveBeenLastCalledWith("attempt-1", 0);
  });

  it("fetches final attempt output once when an attempt becomes terminal", async () => {
    const eventZero: AttemptEvent = {
      sequence: 0,
      kind: "progress",
      payload: "Still working",
      server_time: "2026-08-11T12:00:00Z",
    };
    const finalEvent: AttemptEvent = {
      sequence: 1,
      kind: "result",
      payload: "Finished",
      server_time: "2026-08-11T12:00:01Z",
    };
    let attemptState: "running" | "succeeded" = "running";
    vi.spyOn(api, "run").mockImplementation(async () => runDetail(attemptState));
    const events = vi.spyOn(api, "events").mockImplementation(async (_attemptID, after) => after < 0 ? {
      events: [eventZero], next_after: 0, has_more: false,
    } : {
      events: after === 0 ? [finalEvent] : [], next_after: Math.max(after, 1), has_more: false,
    });
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    await userEvent.click(await screen.findByText("github.com/example/factory"));
    expect(await screen.findByText("Still working")).toBeVisible();

    attemptState = "succeeded";
    await client.refetchQueries({ queryKey: ["runs", headRun.id] });

    expect(await screen.findByText("Finished")).toBeVisible();
    expect(events).toHaveBeenCalledTimes(2);
    expect(events).toHaveBeenLastCalledWith("attempt-1", 0);
  });

  it("shows the frozen execution destination in Run detail", async () => {
    const cloud = runDetail();
    cloud.run = {
      ...cloud.run,
      execution: {
        profile_id: "profile-cloud-1",
        profile_version: 1,
        backend: "fake_cloud_run",
        runtime: "codex",
        provider: "openrouter",
        model: "deepseek/test",
        timeout_seconds: 7200,
        resource_class: "standard",
        commit_resolution_policy: "frozen_commit",
      },
    };
    vi.spyOn(api, "run").mockResolvedValue(cloud);
    const client = testClient();
    render(<QueryClientProvider client={client}><RunDetailView id={headRun.id} onBack={() => undefined} /></QueryClientProvider>);

    expect(await screen.findByText(/Cloud Run · openrouter \/ deepseek\/test/)).toBeVisible();
  });
});

function testClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function run(id: string, name: string, state: Run["state"]): Run {
  return {
    id,
    task_id: "task-1",
    task: {
      id: "task-1",
      name,
      prompt: "Review.",
      runtime: "codex",
      timeout_seconds: 7200,
      concurrency_limit: 10,
      generation: 1,
      repositories: [],
    },
    execution: {
      profile_id: "persistent-auto",
      profile_version: 1,
      backend: "persistent",
      runtime: "codex",
      provider: "worker",
      model: "worker-default",
      timeout_seconds: 7200,
      resource_class: "worker",
      commit_resolution_policy: "resolve_per_attempt",
    },
    source: "manual",
    state,
    needs_attention: false,
    session_count: 1,
    succeeded_count: state === "succeeded" ? 1 : 0,
    ready_count: 0,
    needs_input_count: 0,
    no_change_count: 0,
    failed_count: 0,
    cancelled_count: 0,
    active_count: state === "succeeded" ? 0 : 1,
    admitted_at: "2026-08-11T12:00:00Z",
    updated_at: "2026-08-11T12:00:00Z",
  };
}

function runDetail(attemptState: "running" | "succeeded" = "running"): RunDetail {
  return {
    run: headRun,
    sessions: [{
      id: "session-1",
      run_id: headRun.id,
      repository_id: "repository-1",
      repository_identity: "github.com/example/factory",
      required_runtime: "codex",
      execution: headRun.execution,
      timeout_seconds: 7200,
      state: "running",
      assigned_worker_id: "worker-1",
      cancellation_requested: false,
      retry_may_repeat_effects: false,
      admitted_at: "2026-08-11T12:00:00Z",
      started_at: "2026-08-11T12:00:00Z",
      attempts: [{
        id: "attempt-1",
        execution_id: "execution-1",
        worker_id: "worker-1",
        attempt_number: 1,
        state: attemptState,
        lease_expires_at: "2026-08-11T12:01:00Z",
        started_at: "2026-08-11T12:00:00Z",
        created_at: "2026-08-11T12:00:00Z",
      }],
    }],
  };
}
