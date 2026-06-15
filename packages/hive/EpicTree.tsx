"use client";

import React from "react";
import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { issueListOptions } from "@multica/core/issues/queries";
import { HiveHeader } from "./HiveHeader";

interface HiveHealth {
  ok: boolean;
}

export function EpicTree() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const p = useWorkspacePaths();

  // Plugin connectivity banner (separate from epic data).
  const { data: health } = useQuery<HiveHealth>({
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

  // Epics are sourced from the workspace's Multica issues. Top-level issues
  // (no parent) are treated as epics; sub-issues nest beneath their parent.
  const {
    data: issues = [],
    isPending,
    isError,
  } = useQuery({ ...issueListOptions(wsId), enabled: !!wsId });

  const epics = issues.filter((i) => !i.parent_issue_id);
  const childrenByParent = new Map<string, typeof issues>();
  for (const issue of issues) {
    if (!issue.parent_issue_id) continue;
    const list = childrenByParent.get(issue.parent_issue_id) ?? [];
    list.push(issue);
    childrenByParent.set(issue.parent_issue_id, list);
  }

  const row = (issue: (typeof issues)[number], nested: boolean) => (
    <a
      key={issue.id}
      href={p.issueDetail(issue.id)}
      className={`flex items-center gap-2 rounded-md px-2 ${
        nested ? "py-1" : "py-1.5"
      } hover:bg-muted/50`}
    >
      <span className="font-mono text-xs text-muted-foreground">
        {issue.identifier}
      </span>
      <span className="truncate text-sm">{issue.title}</span>
      <span className="ml-auto shrink-0 text-xs text-muted-foreground">
        {issue.status}
      </span>
    </a>
  );

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <HiveHeader
        title="Epics"
        right={
          <span className="text-xs text-muted-foreground">
            {health?.ok ? "Hive plugin connected" : "Hive plugin unavailable"}
          </span>
        }
      />

      <div className="flex-1 overflow-y-auto p-6">
        {isPending ? (
          <p className="text-sm text-muted-foreground">Loading epics…</p>
        ) : isError ? (
          <p className="text-sm text-destructive">Failed to load epics.</p>
        ) : epics.length === 0 ? (
          <p className="text-sm text-muted-foreground">No epics yet.</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {epics.map((epic) => {
              const children = childrenByParent.get(epic.id) ?? [];
              return (
                <li key={epic.id} className="flex flex-col gap-1">
                  {row(epic, false)}
                  {children.length > 0 && (
                    <ul className="ml-4 flex flex-col gap-1 border-l pl-3">
                      {children.map((child) => (
                        <li key={child.id}>{row(child, true)}</li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
