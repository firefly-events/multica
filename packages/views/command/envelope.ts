export type SourceHealth = {
  ok: boolean;
  stub?: boolean;
};

export type Envelope = {
  v: string | number;
  ts: string;
  sources: {
    ops: SourceHealth;
    usage: SourceHealth;
    agents: SourceHealth;
    clients: SourceHealth;
  };
  ops: {
    stuck: Array<{ name: string }>;
    zombies: {
      opencode?: number | null;
      claude?: number | null;
      daemonCap?: number | null;
    };
    burn: {
      totalTokens?: number | null;
      costUSD?: number | null;
    };
    errors: string[];
  };
  usage: {
    claudePct?: number | null;
    sessionPct?: number | null;
    resetAt?: string | null;
    gemini: {
      rpdUsed?: number | null;
      rpdLimit?: number | null;
    };
    tokens: {
      inputTokens?: number | null;
      outputTokens?: number | null;
      totalTokens?: number | null;
      costUSD?: number | null;
    };
  };
  agents: {
    nodes: Array<{
      id: string;
      label: string;
      kind: string;
      status: string;
    }>;
    edges: unknown[];
  };
  clients: Array<{
    name: string;
    status: string;
    stage?: string | number | null;
    stageName?: string | null;
    blocked?: string | null;
  }>;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function hasSourceHealth(value: unknown): value is SourceHealth {
  return isRecord(value) && typeof value.ok === "boolean";
}

export function isEnvelope(value: unknown): value is Envelope {
  if (!isRecord(value) || !isRecord(value.sources)) return false;
  return (
    typeof value.ts === "string" &&
    (typeof value.v === "string" || typeof value.v === "number") &&
    hasSourceHealth(value.sources.ops) &&
    hasSourceHealth(value.sources.usage) &&
    hasSourceHealth(value.sources.agents) &&
    hasSourceHealth(value.sources.clients) &&
    isRecord(value.ops) &&
    isRecord(value.usage) &&
    isRecord(value.agents) &&
    Array.isArray(value.clients)
  );
}
