"use client";

import React from "react";
import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";

interface HiveHealth {
  ok: boolean;
}

export function EpicTree() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";

  const { data, isPending, isError } = useQuery<HiveHealth>({
    queryKey: ["hive", "health", wsId],
    queryFn: async () => {
      const res = await fetch("/api/plugins/hive/healthz", {
        credentials: "include",
        headers: { "X-Workspace-ID": wsId },
      });
      if (!res.ok) throw new Error(`hive healthz ${res.status}`);
      return res.json() as Promise<HiveHealth>;
    },
    enabled: !!wsId,
  });

  if (isPending) {
    return <div className="p-6 text-muted-foreground text-sm">Loading epics…</div>;
  }

  if (isError) {
    return <div className="p-6 text-destructive text-sm">Failed to load epics.</div>;
  }

  return (
    <div className="flex flex-col gap-4 p-6">
      <h1 className="text-xl font-semibold">Epics</h1>
      <p className="text-sm text-muted-foreground">
        {data?.ok ? "Hive plugin connected. No epics yet." : "Hive plugin unavailable."}
      </p>
    </div>
  );
}
