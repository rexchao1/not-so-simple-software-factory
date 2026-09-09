import type {
  DeliveryMode,
  APIErrorBody,
  AttemptEventPage,
  ExecutionProfile,
  FactoryPause,
  ManagedRepository,
  ManagedRepositoryReadiness,
  Task,
  Overview,
  SaveTaskInput,
  Pipeline,
  PlanningCheckpoint,
  Run,
  RunDetail,
  RunPage,
  Roadmap,
  Session,
  Worker,
  WorkDetail,
  WorkPage,
} from "./types";

export class APIError extends Error {
  constructor(public code: string, message: string, public status: number) {
    super(message);
    this.name = "APIError";
  }
}

// draftRuns pages exhaustively so an old draft stays reachable, which is the
// whole point of filtering server side. The cap bounds a pathological server
// rather than any real approval queue: 25 pages of 200 is 5000 drafts.
const maxDraftPages = 25;

async function requestWithStatus<T>(path: string, init?: RequestInit): Promise<{ data: T; status: number }> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...init.headers } : init?.headers,
  });
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    try {
      body = await response.json() as APIErrorBody;
    } catch {
      // A proxy response may not be JSON. The HTTP status remains useful.
    }
    throw new APIError(
      body?.error.code ?? "request_failed",
      body?.error.message ?? `Request failed with status ${response.status}`,
      response.status,
    );
  }
  if (response.status === 204) return { data: undefined as T, status: response.status };
  return { data: await response.json() as T, status: response.status };
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  return (await requestWithStatus<T>(path, init)).data;
}

export const api = {
  overview: () => request<Overview>("/api/v1/overview"),
  // The roadmap arrives whole: every project, every checkpoint, and what is
  // waiting, in one read. It is small because it carries no PRD bodies, so
  // there is nothing to page.
  roadmap: async () => {
    const roadmap = await request<Roadmap>("/api/v1/roadmap");
    return { ...roadmap, projects: roadmap.projects ?? [], waiting: roadmap.waiting ?? [] };
  },
  // One checkpoint's PRD and saved answers, which the roadmap does not carry.
  // Read only: nothing in the cockpit writes planning state.
  planningCheckpoint: (project: string, number: number) => request<PlanningCheckpoint>(
    `/api/v1/planning/projects/${encodeURIComponent(project)}/checkpoints/${number}`,
  ),
  factoryPause: () => request<FactoryPause>("/api/v1/settings/pause"),
  setFactoryPause: (input: FactoryPause) => request<FactoryPause>("/api/v1/settings/pause", { method: "PUT", body: JSON.stringify(input) }),
  executionProfiles: async () => (await request<{ profiles: ExecutionProfile[] | null }>("/api/v1/execution-profiles")).profiles ?? [],
  pipelines: async () => (await request<{ pipelines: Pipeline[] | null }>("/api/v1/pipelines")).pipelines ?? [],
  tasks: async (includeArchived = false) => {
    const tasks: Task[] = [];
    let cursor = "";
    do {
      const query = new URLSearchParams({ limit: "200", include_archived: String(includeArchived) });
      if (cursor) query.set("cursor", cursor);
      const page = await request<{ tasks: Task[] | null; next_cursor?: string }>(`/api/v1/tasks?${query}`);
      tasks.push(...(page.tasks ?? []));
      cursor = page.next_cursor ?? "";
    } while (cursor);
    return tasks;
  },
  task: (id: string) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}`),
  createTask: (input: SaveTaskInput) => request<Task>("/api/v1/tasks", {
    method: "POST", body: JSON.stringify(input),
  }),
  updateTask: (id: string, input: SaveTaskInput) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}`, {
    method: "PUT", body: JSON.stringify(input),
  }),
  archiveTask: (id: string, archived: boolean, expectedGeneration: number) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}/archived`, {
    method: "PUT", body: JSON.stringify({ archived, expected_generation: expectedGeneration }),
  }),
  runTask: (id: string, requestKey: string, executionProfileID?: string) => request<RunDetail>(`/api/v1/tasks/${encodeURIComponent(id)}/run`, {
    method: "POST", body: JSON.stringify({ request_key: requestKey, execution_profile_id: executionProfileID }),
  }),
  discardTaskOccurrence: (id: string, pendingDueAt: string) => request<Task>(`/api/v1/tasks/${encodeURIComponent(id)}/discard-occurrence`, {
    method: "POST", body: JSON.stringify({ pending_due_at: pendingDueAt }),
  }),
  work: async (options: {
    cursor?: string; repositoryID?: string; runID?: string; states?: string[]; limit?: number;
    planning?: "exclude" | "only" | "all";
  } = {}) => {
    const query = new URLSearchParams({ limit: String(options.limit ?? 100) });
    if (options.cursor) query.set("cursor", options.cursor);
    if (options.repositoryID) query.set("repository_id", options.repositoryID);
    if (options.runID) query.set("run_id", options.runID);
    // Repeated state parameters, which is the shape the board's columns need.
    for (const state of options.states ?? []) query.append("state", state);
    if (options.planning) query.set("planning", options.planning);
    const page = await request<WorkPage>(`/api/v1/work?${query}`);
    return { work: page.work ?? [], next_cursor: page.next_cursor ?? null };
  },
  workDetail: (id: string) => request<WorkDetail>(`/api/v1/work/${encodeURIComponent(id)}`),
  retryWork: (id: string) => request<RunDetail>(`/api/v1/work/${encodeURIComponent(id)}/retry`, {
    method: "POST", body: "{}",
  }),
  runs: async (cursor = "") => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("cursor", cursor);
    const page = await request<RunPage>(`/api/v1/runs?${query}`);
    return { runs: page.runs ?? [], next_cursor: page.next_cursor ?? null };
  },
  draftRuns: async () => {
    const runs: Run[] = [];
    let cursor = "";
    // A cursor that repeats instead of advancing would otherwise spin here
    // forever, appending the same page until the tab dies. RunPage ignores a
    // cursor that decodes to admitted_at 0, so that server response exists.
    for (let requested = 0; requested < maxDraftPages; requested++) {
      const query = new URLSearchParams({ limit: "200", state: "draft" });
      if (cursor) query.set("cursor", cursor);
      const page = await request<RunPage>(`/api/v1/runs?${query}`);
      runs.push(...(page.runs ?? []));
      const next = page.next_cursor ?? "";
      if (!next || next === cursor) break;
      cursor = next;
    }
    return runs;
  },
  run: (id: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`),
  cancelRun: (id: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}/cancel`, {
    method: "POST", body: "{}",
  }),
  retrySession: (runId: string, sessionId: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(sessionId)}/retry`, {
    method: "POST", body: "{}",
  }),
  approveWork: (workId: string, actor: string) => request<Session>(`/api/v1/work/${encodeURIComponent(workId)}/approve`, {
    method: "POST", body: JSON.stringify({ actor }),
  }),
  events: async (attemptID: string, after: number): Promise<AttemptEventPage> => {
    const query = new URLSearchParams({ after: String(after), limit: "100" });
    const page = await request<{ events: AttemptEventPage["events"] | null; next_after: number; has_more: boolean }>(
      `/api/v1/attempts/${encodeURIComponent(attemptID)}/events?${query}`,
    );
    return { ...page, events: page.events ?? [] };
  },
  cancelSession: (runId: string, sessionId: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(sessionId)}/cancel`, {
    method: "POST", body: "{}",
  }),
  workers: async () => ((await request<{ workers: Worker[] | null }>("/api/v1/workers")).workers ?? []).map(normalizeWorker),
  worker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}`)),
  testWorker: async (id: string) => normalizeWorker(await request<Worker>(`/api/v1/workers/${encodeURIComponent(id)}/test`, {
    method: "POST", body: "{}",
  })),
  repositories: async () => (await request<{ repositories: ManagedRepository[] | null }>("/api/v1/repositories")).repositories ?? [],
  repository: (id: string) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}`),
  repositoryReadiness: (id: string) => request<ManagedRepositoryReadiness>(`/api/v1/repositories/${encodeURIComponent(id)}/readiness`),
  createRepository: async (remoteIdentity: string) => {
    const response = await requestWithStatus<ManagedRepository>("/api/v1/repositories", {
      method: "POST", body: JSON.stringify({ remote_identity: remoteIdentity }),
    });
    return { repository: response.data, created: response.status === 201 };
  },
  setRepositoryEnabled: (id: string, enabled: boolean) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}/enabled`, {
    method: "PUT", body: JSON.stringify({ enabled }),
  }),
  setRepositoryDefaultDelivery: (id: string, defaultDelivery: DeliveryMode) => request<ManagedRepository>(`/api/v1/repositories/${encodeURIComponent(id)}/delivery`, {
    method: "PUT", body: JSON.stringify({ default_delivery: defaultDelivery }),
  }),
};

function normalizeWorker(worker: Worker): Worker {
  const capabilities = worker.capabilities?.length ? worker.capabilities : [{
    kind: "runtime" as const,
    name: worker.runtime,
    status: worker.health === "healthy" ? "ready" as const : "unhealthy" as const,
    version: worker.runtime_version,
  }];
  return {
    ...worker,
    labels: worker.labels ?? {},
    capabilities,
    repositories: worker.repositories ?? [],
    retained_worktrees: worker.retained_worktrees ?? [],
    source_access: worker.source_access ?? [],
  };
}
