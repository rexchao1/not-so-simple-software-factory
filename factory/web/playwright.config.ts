import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: { timeout: 8_000 },
  reporter: [["list"], ["html", { open: "never" }]],
  outputDir: "test-results/artifacts",
  use: {
    baseURL: "http://127.0.0.1:17437",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 1000 } },
    },
  ],
  webServer: [
    {
      command: "node e2e/server.mjs",
      url: "http://127.0.0.1:17437/healthz",
      reuseExistingServer: false,
      timeout: 120_000,
    },
    {
      command: "node e2e/site-server.mjs",
      url: "http://127.0.0.1:17438",
      reuseExistingServer: false,
      timeout: 10_000,
    },
  ],
});
