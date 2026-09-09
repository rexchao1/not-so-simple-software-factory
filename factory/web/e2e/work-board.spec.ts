import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

// These run against the real Go control plane and a real Worker started by
// e2e/server.mjs, so they prove the Work board against the actual API rather
// than a mock of it.

const api = "http://127.0.0.1:17437/api/v1";

test.setTimeout(120_000);

// The Worker advertises its repositories after it registers, so nothing can be
// admitted until the control plane has seen them.
test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const response = await request.get(`${api}/repositories`);
    if (!response.ok()) return 0;
    const body = await response.json() as { repositories?: unknown[] };
    return body.repositories?.length ?? 0;
  }, { timeout: 60_000 }).toBeGreaterThan(1);
});

async function repositoryID(request: APIRequestContext, name: string): Promise<string> {
  const response = await request.get(`${api}/repositories`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json() as { repositories: Array<{ id: string; remote_identity: string }> | null };
  const match = (body.repositories ?? []).find((repository) => repository.remote_identity.includes(name));
  expect(match, `no managed repository matching ${name}`).toBeTruthy();
  return match!.id;
}

// admitMultiRepositoryWork creates one Task across two repositories, which is
// the case the board exists to split: one Run, two independent Work items.
async function admitMultiRepositoryWork(request: APIRequestContext, name: string, prompt: string) {
  const factory = await repositoryID(request, "factory-demo");
  const handbook = await repositoryID(request, "handbook-demo");
  const task = await request.post(`${api}/tasks`, {
    data: {
      name, prompt, runtime: "codex", timeout_seconds: 600, concurrency_limit: 2,
      repository_ids: [factory, handbook], schedule: { enabled: false },
    },
  });
  expect(task.ok(), await task.text()).toBeTruthy();
  const taskID = (await task.json() as { id: string }).id;
  const run = await request.post(`${api}/tasks/${taskID}/run`, {
    data: { request_key: `e2e-${name.replace(/\W+/g, "-")}-${Date.now()}` },
  });
  expect(run.ok(), await run.text()).toBeTruthy();
  return { taskID, factory, handbook };
}

async function openWorkBoard(page: Page) {
  await page.goto("/work");
  await expect(page.getByRole("heading", { name: "Work", level: 1 })).toBeVisible();
}

test.describe("Work board", () => {
  test("splits one multi-repository Run into a card per repository", async ({ page, request }) => {
    const { factory } = await admitMultiRepositoryWork(request, "Board split proof", "Do the work.");
    await openWorkBoard(page);

    const cards = page.getByRole("button", { name: /Board split proof/ });
    await expect(cards).toHaveCount(2, { timeout: 20_000 });

    // Each card names exactly one repository, so a card can never describe
    // work in a repository the operator did not ask about.
    await expect(page.getByRole("button", { name: /Board split proof in factory-demo/ })).toHaveCount(1);
    await expect(page.getByRole("button", { name: /Board split proof in handbook-demo/ })).toHaveCount(1);

    // Asserted on this Task's own cards rather than the board total: these
    // specs share one control plane, so any global count depends on what else
    // has run.
    const tabs = page.getByRole("tablist", { name: "Repository" });
    await tabs.getByRole("tab", { name: /^factory-demo/ }).click();
    await expect(page.getByRole("button", { name: /Board split proof/ })).toHaveCount(1);
    await expect(page.getByRole("button", { name: /Board split proof in handbook-demo/ })).toHaveCount(0);
    expect(factory).toBeTruthy();
  });

  test("opens a Work item on its brief and keeps raw output under Evidence", async ({ page, request }) => {
    await admitMultiRepositoryWork(request, "Detail proof", "Do the work.");
    await openWorkBoard(page);

    await page.getByRole("button", { name: /Detail proof in factory-demo/ }).first().click();
    await expect(page).toHaveURL(/\/work\/[0-9a-f-]{36}$/);
    await expect(page.getByRole("heading", { name: "Detail proof", level: 1 })).toBeVisible();

    // Brief is the default: the page opens on what this is and where it
    // stands, never on a wall of logs.
    await expect(page.getByRole("tab", { name: "Brief" })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("heading", { name: "Where it stands" })).toBeVisible();

    await page.getByRole("tab", { name: "Evidence" }).click();
    await expect(page.getByText("Identifiers and timestamps")).toBeVisible();

    await page.getByRole("tab", { name: /Stages/ }).click();
    await expect(page.getByRole("list", { name: "Pipeline stages" })).toBeVisible();
  });

  test("navigates from a Work item to its parent Run", async ({ page, request }) => {
    await admitMultiRepositoryWork(request, "Parent run proof", "Do the work.");
    await openWorkBoard(page);
    await page.getByRole("button", { name: /Parent run proof in factory-demo/ }).first().click();

    await page.getByRole("button", { name: /^[0-9a-f]{8}$/ }).click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]{36}$/);
    await expect(page.getByRole("heading", { name: "Parent run proof", level: 1 })).toBeVisible();
  });

  test("cost is never shown as zero when no runtime reported it", async ({ page, request }) => {
    // The fake Codex runtime reports no cost at all, which is the case that
    // must read as unavailable rather than free.
    await admitMultiRepositoryWork(request, "Cost honesty proof", "Do the work.");
    await openWorkBoard(page);
    await expect(page.getByRole("button", { name: /Cost honesty proof/ }).first()).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText("Cost unavailable").first()).toBeVisible();
    await expect(page.getByText("$0.00")).toHaveCount(0);
  });
});

test.describe("Factory pause", () => {
  test.afterEach(async ({ request }) => {
    await request.put(`${api}/settings/pause`, { data: { paused: false } });
  });

  test("confirms, blocks admission, and resumes", async ({ page, request }) => {
    await page.goto("/overview");
    await page.getByRole("button", { name: "Pause" }).click();

    const dialog = page.getByRole("dialog", { name: "Pause Factory?" });
    await expect(dialog).toContainText("Active attempts continue to completion.");
    await expect(dialog).toContainText("No new Work will be admitted.");
    await dialog.getByRole("button", { name: "Pause Factory" }).click();

    const banner = page.getByRole("status", { name: "Factory pause" });
    await expect(banner).toContainText("Factory is paused.");

    // The API must refuse new admission for as long as the banner is up.
    const repository = await repositoryID(request, "factory-demo");
    const task = await request.post(`${api}/tasks`, {
      data: {
        name: "Paused admission proof", prompt: "Do the work.", runtime: "codex",
        timeout_seconds: 600, concurrency_limit: 1,
        repository_ids: [repository], schedule: { enabled: false },
      },
    });
    expect(task.ok()).toBeTruthy();
    const taskID = (await task.json() as { id: string }).id;
    const blocked = await request.post(`${api}/tasks/${taskID}/run`, {
      data: { request_key: `e2e-paused-${Date.now()}` },
    });
    expect(blocked.status()).toBe(409);
    expect((await blocked.json() as { error: { code: string } }).error.code).toBe("factory_paused");

    await page.getByRole("button", { name: "Resume" }).click();
    await expect(page.getByRole("status", { name: "Factory pause" })).toHaveCount(0);

    const admitted = await request.post(`${api}/tasks/${taskID}/run`, {
      data: { request_key: `e2e-resumed-${Date.now()}` },
    });
    expect(admitted.ok(), await admitted.text()).toBeTruthy();
  });
});
