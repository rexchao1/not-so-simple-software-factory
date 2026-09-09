import { describe, expect, it } from "vitest";
import { eventSummary, timeUntil } from "./format";
import type { AttemptEvent } from "./types";

function event(payload: unknown, kind = "codex"): AttemptEvent {
  return { sequence: 0, kind, payload, server_time: "2026-07-31T12:00:00Z" };
}

describe("eventSummary", () => {
  it("renders Codex assistant messages without their event envelope", () => {
    expect(eventSummary(event({
      type: "item.completed",
      item: { id: "item-1", type: "agent_message", text: "The tests pass." },
    }))).toEqual({ label: "Assistant", text: "The tests pass." });
  });

  it("summarizes Codex commands without exposing captured output", () => {
    const summary = eventSummary(event({
      type: "item.completed",
      item: {
        type: "command_execution",
        command: "go test ./...",
        aggregated_output: "large and unreadable output",
        exit_code: 0,
      },
    }));

    expect(summary).toEqual({ label: "Command", text: "Succeeded: go test ./..." });
    expect(summary?.text).not.toContain("unreadable output");
  });

  it("reports failed Codex tool calls and file changes as errors", () => {
    expect(eventSummary(event({
      type: "item.completed",
      item: { type: "mcp_tool_call", tool: "js", status: "failed", error: "private detail" },
    }))).toEqual({ label: "Tool error", text: "js failed" });
    expect(eventSummary(event({
      type: "item.completed",
      item: { type: "file_change", status: "failed" },
    }))).toEqual({ label: "File error", text: "File changes failed" });
  });

  it("renders Claude text and tool use from a nested assistant message", () => {
    expect(eventSummary(event({
      type: "assistant",
      session_id: "secret-session",
      message: {
        model: "claude-test",
        content: [
          { type: "text", text: "I found the failure." },
          { type: "tool_use", name: "Bash", input: { command: "go test ./internal/controlplane" } },
        ],
      },
    }, "claude-code"))).toEqual({
      label: "Assistant",
      text: "I found the failure.\n\nUsing Bash: go test ./internal/controlplane",
    });
  });

  it("summarizes Claude tool results without showing their contents", () => {
    const summary = eventSummary(event({
      type: "user",
      tool_use_result: { stdout: "private command output" },
      message: {
        content: [{ type: "tool_result", content: "private command output", is_error: false }],
      },
    }, "claude-code"));

    expect(summary).toEqual({ label: "Tool", text: "Tool call completed" });
    expect(summary?.text).not.toContain("private command output");
  });

  it("reports failed Claude terminal results as errors", () => {
    expect(eventSummary(event({
      type: "result",
      is_error: true,
      result: "The release check failed.",
    }, "claude-code"))).toEqual({ label: "Error", text: "The release check failed." });
    expect(eventSummary(event({
      type: "result",
      is_error: true,
      result: "",
    }, "claude-code"))).toEqual({
      label: "Error",
      text: "Claude Code reported a terminal error",
    });
  });

  it("preserves nested errors from failed Codex turns", () => {
    expect(eventSummary(event({
      type: "turn.failed",
      error: { message: "The model provider rejected the request." },
    }))).toEqual({ label: "Error", text: "The model provider rejected the request." });
  });

  it("hides lifecycle and telemetry events", () => {
    expect(eventSummary(event({ type: "thread.started", thread_id: "thread-1" }))).toBeNull();
    expect(eventSummary(event({ type: "system", subtype: "thinking_tokens" }, "claude-code"))).toBeNull();
  });

  it("omits large truncated structured output", () => {
    expect(eventSummary(event({
      stream: "stdout",
      text: '{"large":"runtime envelope',
      truncated: true,
    }))).toEqual({ label: "Output", text: "Large structured runtime output omitted" });
  });

  it("never serializes an unknown object as raw JSON", () => {
    const summary = eventSummary(event({ unfamiliar: { deeply: "nested" } }));

    expect(summary).toEqual({ label: "Runtime", text: "Runtime update" });
    expect(summary?.text).not.toContain("unfamiliar");
  });

  it("preserves existing string and top-level message events", () => {
    expect(eventSummary(event("Plain progress", "progress"))).toEqual({
      label: "Progress",
      text: "Plain progress",
    });
    expect(eventSummary(event({ message: "Task completed" }, "progress"))).toEqual({
      label: "Progress",
      text: "Task completed",
    });
  });
});

describe("timeUntil", () => {
  it("formats upcoming instants as future times", () => {
    const now = Date.parse("2026-08-11T12:00:00Z");
    expect(timeUntil("2026-08-11T12:05:00Z", now)).toBe("in 5m");
    expect(timeUntil("2026-08-12T12:00:00Z", now)).toBe("in 1d");
    expect(timeUntil("2026-08-11T11:55:00Z", now)).toBe("5m ago");
  });

  it("derives each unit from the raw remaining time instead of the rounded one", () => {
    const now = Date.parse("2026-08-11T12:00:00Z");
    expect(timeUntil("2026-08-11T13:01:00Z", now)).toBe("in 1h");
    expect(timeUntil("2026-08-12T11:01:00Z", now)).toBe("in 23h");
  });
});
