"use client";

import React from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";

interface PersonalQueueItem {
  ID: string;
  WorkspaceID: string;
  AssigneeID: string;
  RefKind: string;
  RefID: string;
  Title: string;
  Status: string;
  Meta: unknown;
}

type QueueItemStatus = "pending" | "in_progress" | "done";

const STATUS_LABELS: Record<string, string> = {
  pending: "Pending",
  in_progress: "In Progress",
  done: "Done",
};

const STATUS_CLASSES: Record<string, string> = {
  pending: "text-muted-foreground",
  in_progress: "text-blue-600 dark:text-blue-400",
  done: "text-green-600 dark:text-green-400",
};

async function hiveRequest(path: string, wsId: string, options?: RequestInit) {
  const res = await fetch(`/api/plugins/hive${path}`, {
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      "X-Workspace-ID": wsId,
      ...options?.headers,
    },
    ...options,
  });
  if (!res.ok) throw new Error(`hive ${path} ${res.status}`);
  return res.json();
}

export function PersonalQueue() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const queryClient = useQueryClient();

  const { data, isPending, isError } = useQuery<PersonalQueueItem[]>({
    queryKey: ["hive", "personal-queue-items", wsId],
    queryFn: () =>
      hiveRequest(
        `/personal-queue-items?workspace_id=${encodeURIComponent(wsId)}`,
        wsId,
      ),
    enabled: !!wsId,
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: QueueItemStatus }) =>
      hiveRequest(`/personal-queue-items/${encodeURIComponent(id)}`, wsId, {
        method: "PATCH",
        body: JSON.stringify({ status }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["hive", "personal-queue-items", wsId],
      });
    },
  });

  return (
    <div className="flex flex-col gap-6 p-6">
      <h1 className="text-xl font-semibold">My Queue</h1>

      {isPending && (
        <p className="text-sm text-muted-foreground">Loading queue…</p>
      )}

      {isError && (
        <p className="text-sm text-destructive">Failed to load queue.</p>
      )}

      {!isPending && !isError && data && (
        <>
          {data.length === 0 ? (
            <p className="text-sm text-muted-foreground">No items in your queue.</p>
          ) : (
            <div className="flex flex-col gap-2">
              {data.map((item) => (
                <QueueItemRow
                  key={item.ID}
                  item={item}
                  onUpdate={(status) => updateMutation.mutate({ id: item.ID, status })}
                  isPending={updateMutation.isPending}
                  wsSlug={workspace?.slug ?? ""}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function QueueItemRow({
  item,
  onUpdate,
  isPending,
  wsSlug,
}: {
  item: PersonalQueueItem;
  onUpdate: (status: QueueItemStatus) => void;
  isPending: boolean;
  wsSlug: string;
}) {
  const stateClass = STATUS_CLASSES[item.Status] ?? "text-muted-foreground";
  const refHref = resolveRefHref(wsSlug, item.RefKind, item.RefID);

  return (
    <div className="flex items-center justify-between rounded-lg border bg-card px-4 py-3">
      <div className="flex flex-col gap-0.5 min-w-0 flex-1">
        {refHref ? (
          <a
            href={refHref}
            className="text-sm font-medium hover:underline truncate"
          >
            {item.Title || item.RefID || item.ID}
          </a>
        ) : (
          <span className="text-sm font-medium truncate">
            {item.Title || item.RefID || item.ID}
          </span>
        )}
        <span className={`text-xs ${stateClass}`}>
          {STATUS_LABELS[item.Status] ?? item.Status}
          {item.RefKind ? ` · ${item.RefKind}` : ""}
        </span>
      </div>
      <div className="flex items-center gap-2 ml-4 shrink-0">
        {(["pending", "in_progress", "done"] as QueueItemStatus[]).map((s) => (
          <button
            key={s}
            type="button"
            disabled={isPending || item.Status === s}
            onClick={() => onUpdate(s)}
            className={`h-7 rounded px-2.5 text-xs font-medium transition-colors disabled:opacity-50 ${
              item.Status === s
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground hover:bg-muted/80"
            }`}
          >
            {STATUS_LABELS[s]}
          </button>
        ))}
      </div>
    </div>
  );
}

function resolveRefHref(wsSlug: string, refKind: string, refID: string): string | null {
  if (!wsSlug || !refID) return null;
  if (refKind === "epic") return `/${encodeURIComponent(wsSlug)}/hive/epics`;
  if (refKind === "gate") return `/${encodeURIComponent(wsSlug)}/hive/review-gates`;
  if (refKind === "issue") return `/${encodeURIComponent(wsSlug)}/issues/${encodeURIComponent(refID)}`;
  return null;
}
