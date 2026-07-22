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

function hasOptionalNumber(value: unknown): value is number | null | undefined {
  return value == null || typeof value === "number";
}

function hasOps(value: unknown): value is Envelope["ops"] {
  if (!isRecord(value)) return false;
  if (!Array.isArray(value.stuck) || !value.stuck.every((item) => isRecord(item) && typeof item.name === "string")) {
    return false;
  }
  if (!isRecord(value.zombies) || !isRecord(value.burn) || !Array.isArray(value.errors)) {
    return false;
  }
  return (
    hasOptionalNumber(value.zombies.opencode) &&
    hasOptionalNumber(value.zombies.claude) &&
    hasOptionalNumber(value.zombies.daemonCap) &&
    hasOptionalNumber(value.burn.totalTokens) &&
    hasOptionalNumber(value.burn.costUSD) &&
    value.errors.every((line) => typeof line === "string")
  );
}

function hasUsage(value: unknown): value is Envelope["usage"] {
  if (!isRecord(value) || !isRecord(value.gemini) || !isRecord(value.tokens)) {
    return false;
  }
  return (
    hasOptionalNumber(value.claudePct) &&
    hasOptionalNumber(value.sessionPct) &&
    (value.resetAt == null || typeof value.resetAt === "string") &&
    hasOptionalNumber(value.gemini.rpdUsed) &&
    hasOptionalNumber(value.gemini.rpdLimit) &&
    hasOptionalNumber(value.tokens.inputTokens) &&
    hasOptionalNumber(value.tokens.outputTokens) &&
    hasOptionalNumber(value.tokens.totalTokens) &&
    hasOptionalNumber(value.tokens.costUSD)
  );
}

function hasAgents(value: unknown): value is Envelope["agents"] {
  if (!isRecord(value) || !Array.isArray(value.nodes) || !Array.isArray(value.edges)) {
    return false;
  }
  return value.nodes.every(
    (node) =>
      isRecord(node) &&
      typeof node.id === "string" &&
      typeof node.label === "string" &&
      typeof node.kind === "string" &&
      typeof node.status === "string",
  );
}

function hasClients(value: unknown): value is Envelope["clients"] {
  return (
    Array.isArray(value) &&
    value.every(
      (client) =>
        isRecord(client) &&
        typeof client.name === "string" &&
        typeof client.status === "string" &&
        (client.stage == null || typeof client.stage === "string" || typeof client.stage === "number") &&
        (client.stageName == null || typeof client.stageName === "string") &&
        (client.blocked == null || typeof client.blocked === "string"),
    )
  );
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
    hasOps(value.ops) &&
    hasUsage(value.usage) &&
    hasAgents(value.agents) &&
    hasClients(value.clients)
  );
}
