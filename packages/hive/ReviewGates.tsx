"use client";

import React, { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { hiveRequest } from "./hiveRequest";
import { HiveHeader } from "./HiveHeader";

interface ReviewGate {
  ID: string;
  WorkspaceID: string;
  EpicID: string;
  GateKey: string;
  State: string;
  Evidence: unknown;
  UpdatedBy: string;
}

type GateState = "pending" | "approved" | "rejected";

const STATE_LABELS: Record<string, string> = {
  pending: "Pending",
  approved: "Approved",
  rejected: "Rejected",
};

const STATE_CLASSES: Record<string, string> = {
  pending: "text-muted-foreground",
  approved: "text-green-600 dark:text-green-400",
  rejected: "text-destructive",
};

export function ReviewGates({ epicId }: { epicId?: string }) {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const queryClient = useQueryClient();

  const [filterEpicId, setFilterEpicId] = useState(epicId ?? "");
  const [submittedEpicId, setSubmittedEpicId] = useState(epicId ?? "");

  const { data, isPending, isError } = useQuery<ReviewGate[]>({
    queryKey: ["hive", "review-gates", wsId, submittedEpicId],
    queryFn: () =>
      hiveRequest(
        `/review-gates?workspace_id=${encodeURIComponent(wsId)}&epic_id=${encodeURIComponent(submittedEpicId)}`,
        wsId,
      ),
    enabled: !!wsId && !!submittedEpicId,
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, state }: { id: string; state: GateState }) =>
      hiveRequest(`/review-gates/${encodeURIComponent(id)}`, wsId, {
        method: "PATCH",
        body: JSON.stringify({ state, updated_by: "member" }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["hive", "review-gates", wsId, submittedEpicId],
      });
    },
  });

  return (
    <div className="flex h-full flex-col">
      <HiveHeader title="Review Gates" />
      <div className="flex flex-1 flex-col gap-6 overflow-y-auto p-6">
      <h1 className="text-xl font-semibold">Review Gates</h1>

      <form
        className="flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmittedEpicId(filterEpicId.trim());
        }}
      >
        <input
          type="text"
          placeholder="Epic ID"
          value={filterEpicId}
          onChange={(e) => setFilterEpicId(e.target.value)}
          className="h-8 w-64 rounded-md border bg-background px-3 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
        />
        <button
          type="submit"
          className="h-8 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground hover:bg-primary/90"
        >
          Load
        </button>
      </form>

      {!submittedEpicId && (
        <p className="text-sm text-muted-foreground">Enter an Epic ID to list its review gates.</p>
      )}

      {submittedEpicId && isPending && (
        <p className="text-sm text-muted-foreground">Loading gates…</p>
      )}

      {submittedEpicId && isError && (
        <p className="text-sm text-destructive">Failed to load review gates.</p>
      )}

      {submittedEpicId && !isPending && !isError && data && (
        <>
          {data.length === 0 ? (
            <p className="text-sm text-muted-foreground">No review gates for this epic.</p>
          ) : (
            <div className="flex flex-col gap-2">
              {data.map((gate) => (
                <GateRow
                  key={gate.ID}
                  gate={gate}
                  onUpdate={(state) => updateMutation.mutate({ id: gate.ID, state })}
                  isPending={updateMutation.isPending}
                />
              ))}
            </div>
          )}
        </>
      )}
      </div>
    </div>
  );
}

function GateRow({
  gate,
  onUpdate,
  isPending,
}: {
  gate: ReviewGate;
  onUpdate: (state: GateState) => void;
  isPending: boolean;
}) {
  const stateClass = STATE_CLASSES[gate.State] ?? "text-muted-foreground";

  return (
    <div className="flex items-center justify-between rounded-lg border bg-card px-4 py-3">
      <div className="flex flex-col gap-0.5">
        <span className="text-sm font-medium">{gate.GateKey}</span>
        <span className={`text-xs ${stateClass}`}>
          {STATE_LABELS[gate.State] ?? gate.State}
          {gate.UpdatedBy ? ` · ${gate.UpdatedBy}` : ""}
        </span>
      </div>
      <div className="flex items-center gap-2">
        {(["pending", "approved", "rejected"] as GateState[]).map((s) => (
          <button
            key={s}
            type="button"
            disabled={isPending || gate.State === s}
            onClick={() => onUpdate(s)}
            className={`h-7 rounded px-2.5 text-xs font-medium transition-colors disabled:opacity-50 ${
              gate.State === s
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground hover:bg-muted/80"
            }`}
          >
            {STATE_LABELS[s]}
          </button>
        ))}
      </div>
    </div>
  );
}
