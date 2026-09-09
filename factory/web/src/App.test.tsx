import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";
import type { FactoryPause } from "./types";

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
}

describe("Factory pause control", () => {
  beforeEach(() => {
    window.history.pushState({}, "", "/overview");
    vi.spyOn(api, "workers").mockResolvedValue([]);
    vi.spyOn(api, "overview").mockRejectedValue(new Error("overview is not under test"));
  });

  it("states what pausing does before it does it", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
    const setPause = vi.spyOn(api, "setFactoryPause").mockResolvedValue({ paused: true });
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Pause" }));
    const dialog = await screen.findByRole("dialog", { name: "Pause Factory?" });
    // The three consequences are the point of the dialog: an operator must be
    // able to read what pausing does before committing to it.
    expect(dialog).toHaveTextContent("Active attempts continue to completion.");
    expect(dialog).toHaveTextContent("No new Work will be admitted.");
    expect(dialog).toHaveTextContent("Queued and blocked Work will not be dispatched.");
    expect(setPause).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: "Pause Factory" }));
    // react-query hands the mutation function a second context argument, so
    // only the request body is asserted.
    await waitFor(() => expect(setPause.mock.calls[0]?.[0]).toEqual({ paused: true }));
  });

  it("leaves Factory running when the dialog is cancelled", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: false });
    const setPause = vi.spyOn(api, "setFactoryPause").mockResolvedValue({ paused: true });
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Pause" }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(setPause).not.toHaveBeenCalled();
  });

  it("shows how long Factory has been paused", async () => {
    const paused: FactoryPause = {
      paused: true,
      paused_at: new Date(Date.now() - 90 * 60 * 1000).toISOString(),
    };
    vi.spyOn(api, "factoryPause").mockResolvedValue(paused);
    vi.spyOn(api, "setFactoryPause").mockResolvedValue(paused);
    renderApp();

    const banner = await screen.findByRole("status", { name: "Factory pause" });
    expect(banner).toHaveTextContent("Factory is paused.");
    expect(banner).toHaveTextContent("1h ago");
  });

  // Resuming is not destructive and needs no confirmation: the whole point of
  // the control is to get Work moving again quickly.
  it("resumes on a single click with no dialog", async () => {
    const user = userEvent.setup();
    vi.spyOn(api, "factoryPause").mockResolvedValue({ paused: true });
    const setPause = vi.spyOn(api, "setFactoryPause").mockResolvedValue({ paused: false });
    renderApp();

    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(setPause.mock.calls[0]?.[0]).toEqual({ paused: false }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
