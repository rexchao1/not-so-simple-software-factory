import { expect, test, type APIRequestContext } from "@playwright/test";

// A capture pass, not an assertion pass: it drives the real control plane into
// each state the Work views can be in and writes a screenshot per view, so the
// rendering can be reviewed rather than only asserted about.
//
// It is skipped unless FACTORY_CAPTURE is set, because it exists to produce
// images rather than to guard behaviour.

const api = "http://127.0.0.1:17437/api/v1";
const shots = "screenshots";

test.setTimeout(180_000);
test.skip(!process.env.FACTORY_CAPTURE, "set FACTORY_CAPTURE=1 to capture screenshots");

async function repositoryID(request: APIRequestContext, name: string): Promise<string> {
  const response = await request.get(`${api}/repositories`);
  const body = await response.json() as { repositories: Array<{ id: string; remote_identity: string }> | null };
  const match = (body.repositories ?? []).find((repository) => repository.remote_identity.includes(name));
  expect(match, `no managed repository matching ${name}`).toBeTruthy();
  return match!.id;
}

// A multi-stage pipeline, so the stage graph has edges to draw and a handoff
// to open. A single-stage pipeline exercises none of that.
async function reviewPipeline(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${api}/pipelines`, {
    data: {
      name: "Implement and review",
      stages: [
        { name: "Implement", kind: "agent", prompt: "{{ task.prompt }}" },
        { name: "Review", kind: "agent", prompt: "Review the work for {{ task.name }}." },
        { name: "Test", kind: "code", command: "git status --short" },
      ],
    },
  });
  expect(response.ok(), await response.text()).toBeTruthy();
  return (await response.json() as { id: string }).id;
}

async function admit(
  request: APIRequestContext, name: string, prompt: string, repositories: string[], pipelineID?: string,
) {
  const ids = await Promise.all(repositories.map((repository) => repositoryID(request, repository)));
  const task = await request.post(`${api}/tasks`, {
    data: {
      name, prompt, runtime: "codex", timeout_seconds: 600, concurrency_limit: 2,
      repository_ids: ids, schedule: { enabled: false }, pipeline_id: pipelineID,
    },
  });
  expect(task.ok(), await task.text()).toBeTruthy();
  const taskID = (await task.json() as { id: string }).id;
  const run = await request.post(`${api}/tasks/${taskID}/run`, {
    data: { request_key: `capture-${name.replace(/\W+/g, "-")}-${Date.now()}` },
  });
  expect(run.ok(), await run.text()).toBeTruthy();
}

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const response = await request.get(`${api}/repositories`);
    if (!response.ok()) return 0;
    const body = await response.json() as { repositories?: unknown[] };
    return body.repositories?.length ?? 0;
  }, { timeout: 60_000 }).toBeGreaterThan(1);
});

test("captures the Work views in every state", async ({ page, request }) => {
  // A spread of Work so the board shows more than one column: work that
  // succeeds, work that fails, and work that stays running.
  const pipeline = await reviewPipeline(request);
  await admit(request, "Fix worker queue lease guard", "Do the work.", ["factory-demo", "handbook-demo"], pipeline);
  await admit(request, "Normalise verification evidence", "Do the work.", ["factory-demo"]);
  await admit(request, "Add cursor pagination to the work list", "FACTORY_E2E_FAIL", ["factory-demo"]);
  await admit(request, "Publish the concise artifact templates", "FACTORY_E2E_WAIT", ["handbook-demo"]);

  await page.goto("/work");
  await expect(page.getByRole("heading", { name: "Work", level: 1 })).toBeVisible();
  // Wait until the board has settled into a mix of states rather than
  // capturing every card mid-queue.
  await expect.poll(async () => page.getByRole("button", { name: /in factory-demo|in handbook-demo/ }).count(),
    { timeout: 90_000 }).toBeGreaterThan(3);
  await page.waitForTimeout(20_000);
  await page.screenshot({ path: `${shots}/01-work-board.png`, fullPage: true });

  await page.getByRole("tab", { name: /^factory-demo/ }).click();
  await page.screenshot({ path: `${shots}/02-work-board-repository-tab.png`, fullPage: true });

  await page.getByRole("tab", { name: /All repositories/ }).click();
  await page.getByRole("button", { name: "Table" }).click();
  await page.screenshot({ path: `${shots}/03-work-table.png`, fullPage: true });

  await page.getByRole("button", { name: "Board" }).click();
  await page.getByRole("button", { name: /Fix worker queue lease guard in factory-demo/ }).first().click();
  await expect(page.getByRole("tab", { name: /Stages/ })).toContainText("3");
  await expect(page.getByRole("tab", { name: "Brief" })).toBeVisible();
  await page.screenshot({ path: `${shots}/04-work-detail-brief.png`, fullPage: true });

  await page.getByRole("tab", { name: /Stages/ }).click();
  await page.screenshot({ path: `${shots}/05-work-detail-stages.png`, fullPage: true });

  // Open a handoff edge so the panel is in the capture.
  const edge = page.locator(".handoff-edge button").first();
  if (await edge.count()) {
    await edge.click();
    await page.screenshot({ path: `${shots}/06-work-detail-handoff.png`, fullPage: true });
  }

  await page.getByRole("tab", { name: "Outcome" }).click();
  await page.screenshot({ path: `${shots}/07-work-detail-outcome.png`, fullPage: true });

  await page.getByRole("tab", { name: "Evidence" }).click();
  await page.screenshot({ path: `${shots}/08-work-detail-evidence.png`, fullPage: true });

  // A failed Work item, which is where the terminal card and outcome differ
  // most from the running one.
  await page.goto("/work");
  const failed = page.getByRole("button", { name: /Add cursor pagination/ }).first();
  if (await failed.count()) {
    await failed.click();
    await page.getByRole("tab", { name: "Outcome" }).click();
    await page.screenshot({ path: `${shots}/09-work-detail-failed-outcome.png`, fullPage: true });
  }

  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Overview", level: 1 })).toBeVisible();
  await page.screenshot({ path: `${shots}/10-overview.png`, fullPage: true });

  await page.getByRole("button", { name: "Pause" }).click();
  await expect(page.getByRole("dialog", { name: "Pause Factory?" })).toBeVisible();
  // The modal fades in over 140ms, so a screenshot taken the instant it
  // becomes visible catches it half transparent.
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${shots}/11-pause-dialog.png` });

  await page.getByRole("button", { name: "Pause Factory" }).click();
  await expect(page.getByRole("status", { name: "Factory pause" })).toBeVisible();
  await page.goto("/work");
  await page.screenshot({ path: `${shots}/12-paused-banner.png`, fullPage: true });
  await page.getByRole("button", { name: "Resume" }).click();
});
