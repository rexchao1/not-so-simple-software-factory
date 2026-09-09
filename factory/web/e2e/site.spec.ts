import { expect, test, type Page } from "@playwright/test";

const siteURL = "http://127.0.0.1:17438";

function capturePageFailures(page: Page) {
  const failures: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") failures.push(`console: ${message.text()}`);
  });
  page.on("pageerror", (error) => failures.push(`page: ${error.message}`));
  page.on("requestfailed", (request) => failures.push(`request: ${request.url()} ${request.failure()?.errorText ?? "failed"}`));
  page.on("response", (response) => {
    if (response.status() >= 400) failures.push(`response: ${response.status()} ${response.url()}`);
  });
  return failures;
}

type ContrastFailure = {
  element: string;
  ratio: number;
  text: string;
};

async function findContrastFailures(page: Page) {
  return page.evaluate<ContrastFailure[]>(`(() => {
    const parseColor = (value) => {
      const match = value.match(/rgba?\\(([^)]+)\\)/);
      if (!match) return null;
      const parts = match[1].split(/[ ,/]+/).filter(Boolean).map(Number);
      return [parts[0], parts[1], parts[2], parts.length > 3 ? parts[3] : 1];
    };
    const composite = (foreground, background) => {
      const alpha = foreground[3] + background[3] * (1 - foreground[3]);
      if (alpha === 0) return [0, 0, 0, 0];
      return [
        (foreground[0] * foreground[3] + background[0] * background[3] * (1 - foreground[3])) / alpha,
        (foreground[1] * foreground[3] + background[1] * background[3] * (1 - foreground[3])) / alpha,
        (foreground[2] * foreground[3] + background[2] * background[3] * (1 - foreground[3])) / alpha,
        alpha,
      ];
    };
    const backgroundFor = (element) => {
      const layers = [];
      for (let current = element; current; current = current.parentElement) {
        const color = parseColor(getComputedStyle(current).backgroundColor);
        if (color && color[3] > 0) layers.push(color);
      }
      let background = [9, 10, 12, 1];
      for (const layer of layers.reverse()) background = composite(layer, background);
      return background;
    };
    const luminance = (color) => {
      const channels = color.slice(0, 3).map((channel) => {
        const value = channel / 255;
        return value <= 0.04045 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4);
      });
      return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
    };
    const failures = [];
    for (const element of document.querySelectorAll('body *')) {
      const text = Array.from(element.childNodes)
        .filter((node) => node.nodeType === Node.TEXT_NODE)
        .map((node) => node.textContent || '')
        .join(' ')
        .replace(/\\s+/g, ' ')
        .trim();
      if (!text) continue;
      const styles = getComputedStyle(element);
      const bounds = element.getBoundingClientRect();
      if (styles.display === 'none' || styles.visibility === 'hidden' || bounds.width === 0 || bounds.height === 0) continue;
      let foreground = parseColor(styles.color);
      if (!foreground) continue;
      const background = backgroundFor(element);
      foreground = composite(foreground, background);
      const light = luminance(foreground);
      const dark = luminance(background);
      const ratio = (Math.max(light, dark) + 0.05) / (Math.min(light, dark) + 0.05);
      if (ratio < 4.5) {
        failures.push({
          element: element.tagName.toLowerCase() + (element.className ? '.' + String(element.className).trim().replace(/\\s+/g, '.') : ''),
          ratio: Math.round(ratio * 100) / 100,
          text: text.slice(0, 80),
        });
      }
    }
    return failures;
  })()`);
}

test("public site explains Factory and exposes the main project paths", async ({ page }) => {
  const failures = capturePageFailures(page);
  await page.goto(siteURL, { waitUntil: "networkidle" });

  await expect(page).toHaveTitle("Factory | A control plane for coding agents");
  await expect(page.getByRole("heading", { level: 1, name: "Software work, under control." })).toBeVisible();
  await expect(page.getByText("Factory is a control plane for coding agents.")).toBeVisible();
  await expect(page.getByRole("link", { name: "View on GitHub" })).toHaveAttribute("href", "https://github.com/owainlewis/factory");
  await expect(page.getByRole("heading", { name: "From a saved task to inspectable code." })).toBeVisible();
  await expect(page.getByRole("heading", { name: "A small control plane. Workers do the work." })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Useful now. Still changing." })).toBeVisible();
  await expect(page.getByText("Run table, list, and Kanban")).toBeVisible();
  await expect(page.locator(".product-window")).toContainText("Blocked");
  await expect(page.locator(".product-window")).not.toContainText("Needs you");

  await page.screenshot({ path: "test-results/screenshots/site-desktop.png", fullPage: true });
  expect(failures).toEqual([]);
});

test("public site text meets WCAG AA contrast", async ({ page }) => {
  await page.goto(siteURL);
  expect(await findContrastFailures(page)).toEqual([]);
});

test("public site is keyboard accessible", async ({ page }) => {
  await page.goto(siteURL);
  await page.keyboard.press("Tab");
  const skipLink = page.getByRole("link", { name: "Skip to content" });
  await expect(skipLink).toBeFocused();
  await expect(skipLink).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(`${siteURL}/#main`);
  await expect(page.locator("main")).toBeFocused();
});

test("public site preserves the product window at intermediate widths", async ({ page }) => {
  await page.setViewportSize({ width: 1120, height: 900 });
  await page.goto(siteURL);

  const windowBounds = await page.locator(".product-window").boundingBox();
  expect(windowBounds).not.toBeNull();
  expect(windowBounds?.x ?? 0).toBeGreaterThanOrEqual(0);
  expect((windowBounds?.x ?? 0) + (windowBounds?.width ?? 0)).toBeLessThanOrEqual(1120);
  const viewport = await page.evaluate<{ width: number; scrollWidth: number }>(
    "({ width: document.documentElement.clientWidth, scrollWidth: document.documentElement.scrollWidth })",
  );
  expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.width);
});

test("public site remains usable at 390 pixels", async ({ page }) => {
  const failures = capturePageFailures(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(siteURL, { waitUntil: "networkidle" });

  await expect(page.getByRole("heading", { level: 1, name: "Software work, under control." })).toBeVisible();
  await expect(page.getByRole("link", { name: "GitHub", exact: true }).first()).toBeVisible();
  await expect(page.getByRole("link", { name: "Run it locally" })).toBeVisible();
  const viewport = await page.evaluate<{ width: number; scrollWidth: number }>(
    "({ width: document.documentElement.clientWidth, scrollWidth: document.documentElement.scrollWidth })",
  );
  expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.width);

  await page.screenshot({ path: "test-results/screenshots/site-narrow.png", fullPage: true });
  expect(failures).toEqual([]);
});
