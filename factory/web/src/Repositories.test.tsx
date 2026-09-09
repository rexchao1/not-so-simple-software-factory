import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RepositoryDelivery } from "./Repositories";
import type { DeliveryMode, ManagedRepository } from "./types";

function repositoryFixture(delivery: DeliveryMode): ManagedRepository {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    remote_identity: "github.com/octocat/factory-scratch",
    enabled: true,
    default_delivery: delivery,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
}

describe("repository auto-merge control", () => {
  it("requires confirmation before enabling auto-merge", async () => {
    const setDelivery = vi.fn();
    render(<RepositoryDelivery repository={repositoryFixture("pr")} onSetDelivery={setDelivery} />);

    await userEvent.click(screen.getByRole("button", { name: /enable auto-merge/i }));
    expect(setDelivery).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /yes, merge automatically/i }));
    expect(setDelivery).toHaveBeenCalledWith("pr+automerge");
  });

  it("does not require confirmation to turn it back off", async () => {
    // Friction belongs on the step that removes a human, not on the one that
    // puts them back.
    const setDelivery = vi.fn();
    render(<RepositoryDelivery repository={repositoryFixture("pr+automerge")} onSetDelivery={setDelivery} />);
    await userEvent.click(screen.getByRole("button", { name: /turn off auto-merge/i }));
    expect(setDelivery).toHaveBeenCalledWith("pr");
  });

  it("refuses a second submit while the first is in flight", async () => {
    // The control latches on its own rather than relying on the pending prop.
    // The parent only learns a request is in flight after the mutation starts,
    // so a second click landing in that gap would submit twice.
    let settle: () => void = () => {};
    const setDelivery = vi.fn(() => new Promise<void>((resolve) => { settle = resolve; }));
    render(<RepositoryDelivery repository={repositoryFixture("pr")} onSetDelivery={setDelivery} />);

    await userEvent.click(screen.getByRole("button", { name: /enable auto-merge/i }));
    await userEvent.click(screen.getByRole("button", { name: /yes, merge automatically/i }));
    await userEvent.click(screen.getByRole("button", { name: /yes, merge automatically/i }));
    expect(setDelivery).toHaveBeenCalledTimes(1);
    settle();
  });

  it("says plainly what auto-merge will do", async () => {
    render(<RepositoryDelivery repository={repositoryFixture("pr")} onSetDelivery={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: /enable auto-merge/i }));
    expect(screen.getByText(/without asking again/i)).toBeInTheDocument();
  });

  it("offers no auto-merge control for a branch-delivery project", async () => {
    // Auto-merge is about pull requests. A project that delivers a branch has
    // nothing to merge, and offering the control there would suggest it does.
    render(<RepositoryDelivery repository={repositoryFixture("branch")} onSetDelivery={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /auto-merge/i })).toBeNull();
  });

  it("names the three conditions rather than promising an unconditional merge", async () => {
    // The control is the only place a human is told what auto-merge does not
    // do. INV-8 merges on three conditions and the confirmation says so.
    render(<RepositoryDelivery repository={repositoryFixture("pr")} onSetDelivery={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: /enable auto-merge/i }));
    const warning = screen.getByRole("alert");
    expect(warning.textContent).toMatch(/verif/i);
    expect(warning.textContent).toMatch(/approve/i);
    expect(warning.textContent).toMatch(/check/i);
  });
});
