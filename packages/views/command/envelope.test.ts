import { describe, expect, it } from "vitest";
import { isEnvelope } from "./envelope";

const validEnvelope = {
  v: 1,
  ts: "2026-07-22T12:00:00.000Z",
  sources: {
    ops: { ok: true },
    usage: { ok: true, stub: false },
    agents: { ok: true },
    clients: { ok: true },
  },
  ops: {
    stuck: [{ name: "agent-a" }],
    zombies: { opencode: 1, claude: 2, daemonCap: null },
    burn: { totalTokens: 1000, costUSD: 0.25 },
    errors: ["one"],
  },
  usage: {
    claudePct: 42,
    sessionPct: null,
    resetAt: "2026-07-22T13:00:00.000Z",
    gemini: { rpdUsed: 1, rpdLimit: 100 },
    tokens: {
      inputTokens: 10,
      outputTokens: 20,
      totalTokens: 30,
      costUSD: 0.01,
    },
  },
  agents: {
    nodes: [{ id: "a1", label: "Agent A", kind: "agent", status: "working" }],
    edges: [{ source: "a1", target: "a2" }],
  },
  clients: [
    {
      name: "Client A",
      status: "active",
      stage: 2,
      stageName: "Build",
      blocked: null,
    },
  ],
};

describe("isEnvelope", () => {
  it("accepts the command-center envelope shape consumed by the UI", () => {
    expect(isEnvelope(validEnvelope)).toBe(true);
  });

  it("rejects malformed source health entries", () => {
    expect(
      isEnvelope({
        ...validEnvelope,
        sources: { ...validEnvelope.sources, ops: { ok: "yes" } },
      }),
    ).toBe(false);
  });

  it("rejects malformed nested data that the UI maps over", () => {
    expect(
      isEnvelope({
        ...validEnvelope,
        agents: { ...validEnvelope.agents, nodes: [{ id: "a1", label: "Agent A" }] },
      }),
    ).toBe(false);

    expect(
      isEnvelope({
        ...validEnvelope,
        ops: { ...validEnvelope.ops, errors: [123] },
      }),
    ).toBe(false);

    expect(
      isEnvelope({
        ...validEnvelope,
        clients: [{ name: "Client A", status: 200 }],
      }),
    ).toBe(false);
  });
});
