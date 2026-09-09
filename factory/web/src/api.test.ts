import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import type { Run, RunPage } from "./types";

afterEach(() => vi.unstubAllGlobals());

describe("api.draftRuns", () => {
  // Mocking api.draftRuns itself would leave the query string untested, and
  // the query string is the whole contract: without state=draft the server
  // answers with every Run in the system and the approval view offers an
  // Approve control for work that was never waiting on the gate.
  it("asks the server only for draft Runs", async () => {
    const fetchMock = stubFetch(() => ({ runs: [draftRun("work-1")] }));

    const runs = await api.draftRuns();

    expect(runs).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const url = requestedURL(fetchMock, 0);
    expect(url.pathname).toBe("/api/v1/runs");
    expect(url.searchParams.get("state")).toBe("draft");
    expect(url.searchParams.get("limit")).toBe("200");
  });

  it("follows the cursor so a draft past the first page stays reachable", async () => {
    const fetchMock = stubFetch((url) => url.searchParams.get("cursor") === "page-2"
      ? { runs: [draftRun("work-2")] }
      : { runs: [draftRun("work-1")], next_cursor: "page-2" });

    const runs = await api.draftRuns();

    expect(runs.map((run) => run.targets?.[0].id)).toEqual(["work-1", "work-2"]);
    expect(requestedURL(fetchMock, 1).searchParams.get("state")).toBe("draft");
  });

  it("stops instead of wedging when the server stops advancing the cursor", async () => {
    const fetchMock = stubFetch(() => ({ runs: [draftRun("work-1")], next_cursor: "stuck" }));

    const runs = await api.draftRuns();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(runs).toHaveLength(2);
  });

  it("bounds the walk when every page hands back a fresh cursor", async () => {
    let issued = 0;
    const fetchMock = stubFetch(() => ({ runs: [], next_cursor: `page-${++issued}` }));

    await api.draftRuns();

    expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(25);
    expect(fetchMock.mock.calls.length).toBeGreaterThan(1);
  });
});

function stubFetch(page: (url: URL) => RunPage) {
  const fetchMock = vi.fn(async (input: string) => {
    const body = JSON.stringify(page(new URL(input, "http://localhost")));
    return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestedURL(fetchMock: { mock: { calls: unknown[][] } }, call: number): URL {
  return new URL(String(fetchMock.mock.calls[call][0]), "http://localhost");
}

function draftRun(workID: string): Run {
  return {
    id: `run-${workID}`,
    task_id: "task-1",
    task: {
      id: "task-1",
      name: "Add a farewell function",
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
    targets: [{ id: workID, repository_identity: "github.com/example/scratch" }],
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
