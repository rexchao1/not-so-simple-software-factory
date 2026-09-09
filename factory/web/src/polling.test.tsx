import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useVisibleInterval } from "./polling";

function setVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, value });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useVisibleInterval", () => {
  it("pauses polling while hidden and restores it when visible", () => {
    setVisibility("visible");
    const { result } = renderHook(() => useVisibleInterval(5_000));
    expect(result.current).toBe(5_000);

    act(() => setVisibility("hidden"));
    expect(result.current).toBe(false);

    act(() => setVisibility("visible"));
    expect(result.current).toBe(5_000);
  });
});
