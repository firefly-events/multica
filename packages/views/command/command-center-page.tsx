"use client";

import { useEffect, useState } from "react";
import { Radar, Wifi, WifiOff, AlertCircle } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@multica/ui/components/ui/card";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@multica/ui/components/ui/tabs";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { PageHeader } from "../layout/page-header";
import type { Envelope, SourceHealth } from "@claud-ometer/envelope";
import { isEnvelope } from "@claud-ometer/envelope";

const CLAUD_OMETER_BASE = "http://localhost:3005";
const POLL_INTERVAL_MS = 15_000;

async function fetchEnvelope(): Promise<Envelope> {
  const r = await fetch(`${CLAUD_OMETER_BASE}/api/command`);
  if (!r.ok) throw new Error(`Claud-ometer returned ${r.status}`);
  const json: unknown = await r.json();
  if (!isEnvelope(json)) throw new Error("Response is not a valid Envelope");
  return json;
}

function useCommandEnvelope() {
  const [envelope, setEnvelope] = useState<Envelope | null>(null);
  const [live, setLive] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // SWR-style polling fallback
  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const env = await fetchEnvelope();
        if (!cancelled) {
          setEnvelope(env);
          setError(null);
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : "Unreachable");
      }
    };
    void poll();
    const id = setInterval(() => void poll(), POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  // SSE primary transport
  useEffect(() => {
    const es = new EventSource(`${CLAUD_OMETER_BASE}/api/command/stream`);
    es.addEventListener("envelope", (e) => {
      try {
        const parsed: unknown = JSON.parse((e as MessageEvent).data);
        if (isEnvelope(parsed)) {
          setLive(true);
          setEnvelope(parsed);
          setError(null);
        }
      } catch {
        // malformed frame — polling still covers us
      }
    });
    es.onerror = () => setLive(false);
    return () => es.close();
  }, []);

  return { envelope, live, error };
}

function HealthBadge({ health }: { health: SourceHealth }) {
  if (health.stub) return <Badge variant="secondary" className="text-[10px]">stub</Badge>;
  if (!health.ok) return <Badge variant="destructive" className="text-[10px]">error</Badge>;
  return (
    <Badge className="bg-emerald-500/15 text-emerald-500 border-transparent text-[10px]">live</Badge>
  );
}

const fmt = (n: number | null | undefined) => (n == null ? "—" : n.toLocaleString());

function OpsTab({ envelope }: { envelope: Envelope }) {
  const { ops } = envelope;
  return (
    <div className="space-y-4 py-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span>Workers & Ops</span>
            <HealthBadge health={envelope.sources.ops} />
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm text-muted-foreground">
          <p>
            Stuck agents:{" "}
            <span className="text-foreground font-medium">{ops.stuck.length}</span>
            {ops.stuck.length > 0 && ` — ${ops.stuck.map((s) => s.name).join(", ")}`}
          </p>
          <p>
            Workers — opencode:{" "}
            <span className="text-foreground">{fmt(ops.zombies.opencode)}</span>{" "}
            · claude:{" "}
            <span className="text-foreground">{fmt(ops.zombies.claude)}</span>{" "}
            · cap: <span className="text-foreground">{fmt(ops.zombies.daemonCap)}</span>
          </p>
          <p>
            Burn today:{" "}
            <span className="text-foreground">{fmt(ops.burn.totalTokens)}</span> tokens
            {ops.burn.costUSD != null && (
              <> · <span className="text-foreground">${ops.burn.costUSD.toFixed(2)}</span></>
            )}
          </p>
        </CardContent>
      </Card>
      {ops.errors.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">Recent Errors</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-xs font-mono text-muted-foreground">
              {ops.errors.map((line, i) => (
                <li key={i} className="truncate">{line}</li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function UsageTab({ envelope }: { envelope: Envelope }) {
  const { usage } = envelope;
  return (
    <div className="space-y-4 py-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span>Claude Limits</span>
            <HealthBadge health={envelope.sources.usage} />
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm text-muted-foreground">
          <p>
            Weekly sub:{" "}
            <span className="text-foreground font-medium">
              {usage.claudePct == null ? "—" : `${usage.claudePct}%`}
            </span>
          </p>
          <p>
            Session window:{" "}
            <span className="text-foreground font-medium">
              {usage.sessionPct == null ? "—" : `${usage.sessionPct}%`}
            </span>
          </p>
          <p>
            Resets:{" "}
            <span className="text-foreground">
              {usage.resetAt ? new Date(usage.resetAt).toLocaleString() : "—"}
            </span>
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">Gemini Quota</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>
            RPD: <span className="text-foreground">{fmt(usage.gemini.rpdUsed)}</span>{" "}
            / <span className="text-foreground">{fmt(usage.gemini.rpdLimit)}</span>
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">Today&apos;s Tokens</CardTitle>
        </CardHeader>
        <CardContent className="space-y-1 text-sm text-muted-foreground">
          <p>
            Input: <span className="text-foreground">{fmt(usage.tokens.inputTokens)}</span>
          </p>
          <p>
            Output: <span className="text-foreground">{fmt(usage.tokens.outputTokens)}</span>
          </p>
          <p>
            Total: <span className="text-foreground">{fmt(usage.tokens.totalTokens)}</span>
            {usage.tokens.costUSD != null && (
              <> · <span className="text-foreground">${usage.tokens.costUSD.toFixed(2)}</span></>
            )}
          </p>
        </CardContent>
      </Card>
    </div>
  );
}

function AgentsTab({ envelope }: { envelope: Envelope }) {
  const { agents } = envelope;
  const working = agents.nodes.filter((n) => n.status === "working" || n.status === "in_progress");
  return (
    <div className="space-y-4 py-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span>Agent Graph</span>
            <HealthBadge health={envelope.sources.agents} />
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm text-muted-foreground">
          <p>
            Nodes: <span className="text-foreground font-medium">{agents.nodes.length}</span>{" "}
            · Edges: <span className="text-foreground">{agents.edges.length}</span>
          </p>
          {working.length > 0 && (
            <p>
              Active:{" "}
              <span className="text-foreground">{working.map((n) => n.label).join(", ")}</span>
            </p>
          )}
        </CardContent>
      </Card>
      {agents.nodes.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">All Nodes</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-xs text-muted-foreground">
              {agents.nodes.map((n) => (
                <li key={n.id} className="flex items-center gap-2">
                  <span className="text-foreground font-medium">{n.label}</span>
                  <span className="text-muted-foreground/60">{n.kind}</span>
                  <Badge variant="outline" className="text-[10px] ml-auto">{n.status}</Badge>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function ClientsTab({ envelope }: { envelope: Envelope }) {
  return (
    <div className="space-y-4 py-4">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span>Client Engagements</span>
            <HealthBadge health={envelope.sources.clients} />
          </CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          {envelope.clients.length === 0 ? (
            <p>No client engagements in the feed yet.</p>
          ) : (
            <ul className="space-y-2">
              {envelope.clients.map((c) => (
                <li key={c.name} className="flex flex-col gap-0.5">
                  <div className="flex items-center gap-2">
                    <span className="text-foreground font-medium">{c.name}</span>
                    <Badge variant="outline" className="text-[10px]">{c.status}</Badge>
                  </div>
                  <p className="text-xs">
                    Stage {c.stage ?? "?"} — {c.stageName ?? "n/a"}
                    {c.blocked ? ` · blocked: ${c.blocked}` : ""}
                  </p>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export function CommandCenterPage() {
  const { envelope, live, error } = useCommandEnvelope();

  return (
    <div className="flex flex-col h-full">
      <PageHeader>
        <div className="flex flex-1 items-center justify-between">
          <div className="flex items-center gap-2">
            <Radar className="h-4 w-4 text-primary" />
            <span className="text-sm font-medium">Command Center</span>
            {envelope && (
              <Badge variant="outline" className="text-[10px]">
                v{envelope.v}
              </Badge>
            )}
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            {error ? (
              <>
                <AlertCircle className="h-3.5 w-3.5 text-destructive" />
                <span className="text-destructive">Claud-ometer offline</span>
              </>
            ) : live ? (
              <>
                <Wifi className="h-3.5 w-3.5 text-emerald-500" />
                <span>SSE live</span>
              </>
            ) : (
              <>
                <WifiOff className="h-3.5 w-3.5 text-amber-500" />
                <span>polling</span>
              </>
            )}
            {envelope && (
              <span>· {new Date(envelope.ts).toLocaleTimeString()}</span>
            )}
          </div>
        </div>
      </PageHeader>

      <div className="flex-1 overflow-auto p-4">
        {error && !envelope && (
          <div className="flex flex-col items-center gap-3 py-20 text-sm text-muted-foreground">
            <AlertCircle className="h-8 w-8 text-muted-foreground/40" />
            <p>Claud-ometer is not reachable at <code>localhost:3005</code></p>
            <p className="text-xs">
              Start it with <code>cd ~/Code/Claud-ometer && npm run dev</code>
            </p>
          </div>
        )}

        {!envelope && !error && (
          <div className="space-y-4">
            <Skeleton className="h-32 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        )}

        {envelope && (
          <Tabs defaultValue="ops">
            <TabsList>
              <TabsTrigger value="ops">Ops</TabsTrigger>
              <TabsTrigger value="usage">Usage</TabsTrigger>
              <TabsTrigger value="agents">Agents</TabsTrigger>
              <TabsTrigger value="clients">Clients</TabsTrigger>
            </TabsList>
            <TabsContent value="ops">
              <OpsTab envelope={envelope} />
            </TabsContent>
            <TabsContent value="usage">
              <UsageTab envelope={envelope} />
            </TabsContent>
            <TabsContent value="agents">
              <AgentsTab envelope={envelope} />
            </TabsContent>
            <TabsContent value="clients">
              <ClientsTab envelope={envelope} />
            </TabsContent>
          </Tabs>
        )}
      </div>
    </div>
  );
}
