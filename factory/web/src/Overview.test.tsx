import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { OverviewView } from "./Overview";
import type { Overview } from "./types";

const overview: Overview = {
  active_runs: 1,
  needs_attention: 0,
  completed_last_24h: 2,
  cost: {
    total_usd: 1.25,
    measured_work: 4,
    unavailable_work: 2,
    average_usd: 0.3125,
    highest_usd: 0.62,
    highest_work_id: "work-dear",
    highest_work_name: "Add cursor pagination",
    recent_usd: 0.4,
    recent_days: 7,
    by_model: [{ model: "sonnet", cost_usd: 1.25, attempts: 4 }],
  },
  workers_online: 1,
  workers_total: 1,
  run_metrics: {
    window: "24h",
    total_runs: 4,
    completed_runs: 2,
    completion_rate: 0.5,
    average_queue_time_seconds: 18,
    average_cycle_time_seconds: 3725,
  },
  recent_runs: [],
  upcoming_tasks: [],
  generated_at: "2026-08-13T12:00:00Z",
};

describe("OverviewView", () => {
  it("shows run performance from the overview API", async () => {
    vi.spyOn(api, "overview").mockResolvedValue(overview);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Run performance" });
    expect(within(section).getByText("2 completed")).toBeVisible();
    expect(metricValue(section, "Runs")).toBe("4");
    expect(metricValue(section, "Completion rate")).toBe("50%");
    expect(metricValue(section, "Average queue time")).toBe("18s");
    expect(metricValue(section, "Average cycle time")).toBe("1h 2m");
  });

  it("uses clear empty states for rates and durations", async () => {
    vi.spyOn(api, "overview").mockResolvedValue({
      ...overview,
      run_metrics: {
        ...overview.run_metrics,
        total_runs: 0,
        completed_runs: 0,
        completion_rate: null,
        average_queue_time_seconds: null,
        average_cycle_time_seconds: null,
      },
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Run performance" });
    expect(metricValue(section, "Runs")).toBe("0");
    expect(within(section).getAllByText("No data")).toHaveLength(3);
  });

  it("says how much of its spend it could not see", async () => {
    vi.spyOn(api, "overview").mockResolvedValue(overview);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Reported cost" });
    expect(metricValue(section, "Total")).toBe("$1.25");
    expect(metricValue(section, "Last 7 days")).toBe("$0.40");
    expect(metricValue(section, "Average per Work")).toBe("$0.31");
    expect(metricValue(section, "Cost unavailable")).toBe("2");
    expect(within(section).getByText(/these figures are partial/)).toBeVisible();
    expect(within(section).getByText("Add cursor pagination")).toBeVisible();
  });

  // The dearest Work is the one an operator opens when a total looks wrong, so
  // it has to be reachable rather than merely named.
  it("opens the dearest Work item from the cost panel", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "overview").mockResolvedValue(overview);
    const onWork = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={onWork} />
    </QueryClientProvider>);

    await user.click(await screen.findByRole("button", { name: "Add cursor pagination" }));
    expect(onWork).toHaveBeenCalledWith("work-dear");
  });

  // A factory where no runtime reported anything must not read as free.
  it("reports unmeasured spend as unavailable, not as zero", async () => {
    vi.spyOn(api, "overview").mockResolvedValue({
      ...overview,
      cost: { measured_work: 0, unavailable_work: 3, recent_days: 7 },
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Reported cost" });
    expect(metricValue(section, "Total")).toBe("Unavailable");
    expect(metricValue(section, "Average per Work")).toBe("Unavailable");
    expect(within(section).queryByText("$0.00")).toBeNull();
  });

  // A measured zero is a fact and prints as one.
  it("shows a measured zero as a figure", async () => {
    vi.spyOn(api, "overview").mockResolvedValue({
      ...overview,
      cost: { total_usd: 0, measured_work: 2, unavailable_work: 0, average_usd: 0, recent_usd: 0, recent_days: 7 },
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} onWork={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Reported cost" });
    expect(metricValue(section, "Total")).toBe("$0.00");
    expect(within(section).queryByText(/these figures are partial/)).toBeNull();
  });
});

function metricValue(section: HTMLElement, label: string): string | null {
  return within(section).getByText(label).parentElement?.querySelector("strong")?.textContent ?? null;
}
