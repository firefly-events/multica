import { describe, expect, it } from "vitest";
import { isEnvelope, type Envelope } from "./envelope";

function validEnvelope(): Envelope {
  return {
    v: 1,
    ts: "2026-07-22T13:00:00Z",
    sources: {
      ops: { ok: true },
      usage: { ok: true },
      agents: { ok: true },
      clients: { ok: true },
    },
    ops: {
      stuck: [{ name: "task-1" }],
      zombies: {
        opencode: 0,
        claude: null,
        daemonCap: 2,
      },
      burn: {
        totalTokens: 1234,
        costUSD: 0.42,
      },
      errors: [],
    },
    usage: {
      claudePct: 25,
      sessionPct: null,
      resetAt: null,
      gemini: {
        rpdUsed: 1,
        rpdLimit: 100,
      },
      tokens: {
        inputTokens: 100,
        outputTokens: 50,
        totalTokens: 150,
        costUSD: 0.01,
      },
    },
    agents: {
      nodes: [
        {
          id: "agent-1",
          label: "Agent 1",
          kind: "agent",
          status: "running",
        },
      ],
      edges: [],
    },
    clients: [
      {
        name: "codex",
        status: "connected",
        stage: "active",
        stageName: "Active",
        blocked: null,
      },
    ],
  };
}

describe("isEnvelope", () => {
  it("accepts a fully valid command-center envelope", () => {
    expect(isEnvelope(validEnvelope())).toBe(true);
  });

  it.each([
    ["missing sources", () => ({ ...validEnvelope(), sources: undefined })],
    ["missing ts", () => ({ ...validEnvelope(), ts: undefined })],
    ["wrong-typed ts", () => ({ ...validEnvelope(), ts: 123 })],
    ["wrong-typed v", () => ({ ...validEnvelope(), v: true })],
    [
      "source entry missing ok",
      () => ({
        ...validEnvelope(),
        sources: { ...validEnvelope().sources, ops: {} },
      }),
    ],
    ["clients is null", () => ({ ...validEnvelope(), clients: null })],
    ["clients is an object", () => ({ ...validEnvelope(), clients: {} })],
  ])("rejects malformed envelope data: %s", (_name, makeValue) => {
    expect(isEnvelope(makeValue())).toBe(false);
  });

  it.each([null, undefined, "envelope", []])(
    "rejects non-object top-level values: %s",
    (value) => {
      expect(isEnvelope(value)).toBe(false);
    },
  );
});
