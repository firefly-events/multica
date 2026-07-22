"use client";

/* eslint-disable i18next/no-literal-string */

import { useState } from "react";
import { CheckCircle, Clock, ShieldCheck, XCircle } from "lucide-react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useAuthStore } from "@multica/core/auth";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

// The approval engine HTTP server runs locally on a fixed port.
// Configurable at deploy time via window.__APPROVAL_API__ (injectable from
// the Electron preload or a server-side config endpoint).
const API_BASE: string =
  (typeof window !== "undefined" &&
    (window as Window & { __APPROVAL_API__?: string }).__APPROVAL_API__) ||
  "http://127.0.0.1:7841";

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

type Mode = "human-gate" | "agent-quorum" | "multi-agent-vote";

interface PendingApproval {
  id: string;
  actionType: string;
  actionContext: Record<string, unknown>;
  requestedAt: string;
  requestedBy: string;
  status: "pending" | "resolved";
  mode: Mode;
}

interface VerdictEntry {
  identity: string;
  approve: boolean;
  reasoning: string;
  hardVeto?: boolean;
}

interface AuditRecord {
  id: string;
  approvalId: string;
  actionType: string;
  method: "dashboard-click" | "quorum" | "vote";
  decision: { allowed: boolean; reason: string; message: string };
  approverIdentity: string;
  timestamp: string;
  verdicts: VerdictEntry[];
  passCheck: boolean;
}

async function fetchJson<T>(path: string): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json() as Promise<T>;
}

async function submitApproval(body: {
  approvalId: string;
  approve: boolean;
  note: string;
  approverIdentity: string;
}): Promise<AuditRecord> {
  const res = await fetch(`${API_BASE}/api/approvals/submit`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error((err as { error?: string }).error ?? `${res.status}`);
  }
  return res.json() as Promise<AuditRecord>;
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function ModeLabel({ mode }: { mode: Mode }) {
  const { t } = useT("approvals");
  const labels: Record<Mode, string> = {
    "human-gate": t(($) => $.mode["human-gate"]),
    "agent-quorum": t(($) => $.mode["agent-quorum"]),
    "multi-agent-vote": t(($) => $.mode["multi-agent-vote"]),
  };
  return (
    <span className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
      {labels[mode] ?? mode}
    </span>
  );
}

function RelativeTime({ iso }: { iso: string }) {
  const date = new Date(iso);
  const now = Date.now();
  const diffMs = now - date.getTime();
  const diffMin = Math.floor(diffMs / 60_000);
  if (diffMin < 1) return <span className="text-xs text-muted-foreground">just now</span>;
  if (diffMin < 60)
    return (
      <span className="text-xs text-muted-foreground">{diffMin}m ago</span>
    );
  const diffH = Math.floor(diffMin / 60);
  if (diffH < 24)
    return (
      <span className="text-xs text-muted-foreground">{diffH}h ago</span>
    );
  return (
    <span className="text-xs text-muted-foreground">
      {date.toLocaleDateString()}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Approve / Reject dialog (human-gate only)
// ---------------------------------------------------------------------------

interface ActionDialogProps {
  approval: PendingApproval;
  intent: "approve" | "reject";
  defaultApproverName: string;
  onClose: () => void;
  onSubmitted: () => void;
}

function ActionDialog({
  approval,
  intent,
  defaultApproverName,
  onClose,
  onSubmitted,
}: ActionDialogProps) {
  const { t } = useT("approvals");
  const qc = useQueryClient();
  const [approverName, setApproverName] = useState(defaultApproverName);
  const [note, setNote] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      submitApproval({
        approvalId: approval.id,
        approve: intent === "approve",
        note,
        approverIdentity: approverName.trim() || "dashboard-user",
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["approvals", "pending"] });
      qc.invalidateQueries({ queryKey: ["approvals", "audit"] });
      onSubmitted();
      onClose();
    },
  });

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>
            {intent === "approve"
              ? t(($) => $.action_dialog.title_approve)
              : t(($) => $.action_dialog.title_reject)}
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            <span className="font-mono font-semibold text-foreground">
              {approval.actionType}
            </span>
            {" · "}
            <ModeLabel mode={approval.mode} />
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-medium">
              {t(($) => $.action_dialog.approver_label)}
            </label>
            <Input
              value={approverName}
              onChange={(e) => setApproverName(e.target.value)}
              placeholder={t(($) => $.action_dialog.approver_placeholder)}
              className="h-8 text-sm"
            />
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-medium">
              {t(($) => $.action_dialog.note_label)}
            </label>
            <Textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder={t(($) => $.action_dialog.note_placeholder)}
              className="min-h-[72px] resize-none text-sm"
            />
          </div>

          {mutation.isError && (
            <p className="text-xs text-destructive">
              {t(($) => $.error.submit)}{" "}
              {mutation.error instanceof Error ? mutation.error.message : ""}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onClose}
            disabled={mutation.isPending}
          >
            {t(($) => $.action_dialog.cancel)}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={intent === "approve" ? "default" : "destructive"}
            disabled={mutation.isPending || !approverName.trim()}
            onClick={() => mutation.mutate()}
          >
            {intent === "approve"
              ? t(($) => $.action_dialog.confirm_approve)
              : t(($) => $.action_dialog.confirm_reject)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Pending list
// ---------------------------------------------------------------------------

function PendingTab({ userName }: { userName: string }) {
  const { t } = useT("approvals");
  const [dialog, setDialog] = useState<{
    approval: PendingApproval;
    intent: "approve" | "reject";
  } | null>(null);

  const { data = [], isLoading, isError, refetch } = useQuery<PendingApproval[]>({
    queryKey: ["approvals", "pending"],
    queryFn: () =>
      fetchJson<PendingApproval[]>("/api/approvals/pending?status=pending"),
    refetchInterval: 15_000,
  });

  if (isLoading) {
    return (
      <div className="space-y-2 p-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-md" />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <XCircle className="h-7 w-7 text-destructive/60" />
        <p className="text-sm text-muted-foreground">{t(($) => $.error.load)}</p>
        <Button type="button" variant="outline" size="sm" onClick={() => refetch()}>
          {t(($) => $.error.retry)}
        </Button>
      </div>
    );
  }

  if (data.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
          <ShieldCheck className="h-6 w-6 text-muted-foreground" />
        </div>
        <p className="text-sm font-medium">{t(($) => $.empty.pending_title)}</p>
        <p className="max-w-xs text-xs text-muted-foreground">
          {t(($) => $.empty.pending_body)}
        </p>
      </div>
    );
  }

  return (
    <>
      <div className="divide-y">
        {data.map((ap) => (
          <div key={ap.id} className="flex items-start gap-4 px-5 py-4">
            <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-100 text-amber-600 dark:bg-amber-900/30 dark:text-amber-400">
              <Clock className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm font-semibold">
                  {ap.actionType}
                </span>
                <ModeLabel mode={ap.mode} />
                <Badge variant="secondary" className="text-[10px]">
                  {t(($) => $.badge.pending)}
                </Badge>
              </div>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t(($) => $.table.requested_by)}: {ap.requestedBy}
                {" · "}
                <RelativeTime iso={ap.requestedAt} />
              </p>
              {Object.keys(ap.actionContext).length > 0 && (
                <pre className="mt-2 max-h-20 overflow-y-auto rounded-md bg-muted px-2 py-1.5 text-[10px] leading-relaxed text-muted-foreground">
                  {JSON.stringify(ap.actionContext, null, 2)}
                </pre>
              )}
            </div>
            {ap.mode === "human-gate" && (
              <div className="flex shrink-0 gap-2">
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  onClick={() => setDialog({ approval: ap, intent: "reject" })}
                >
                  {t(($) => $.action_dialog.confirm_reject)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  onClick={() => setDialog({ approval: ap, intent: "approve" })}
                >
                  {t(($) => $.action_dialog.confirm_approve)}
                </Button>
              </div>
            )}
          </div>
        ))}
      </div>

      {dialog && (
        <ActionDialog
          approval={dialog.approval}
          intent={dialog.intent}
          defaultApproverName={userName}
          onClose={() => setDialog(null)}
          onSubmitted={() => setDialog(null)}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Audit history list
// ---------------------------------------------------------------------------

function HistoryTab() {
  const { t } = useT("approvals");

  const { data = [], isLoading, isError, refetch } = useQuery<AuditRecord[]>({
    queryKey: ["approvals", "audit"],
    queryFn: () => fetchJson<AuditRecord[]>("/api/approvals/audit-records"),
    refetchInterval: 30_000,
  });

  if (isLoading) {
    return (
      <div className="space-y-2 p-4">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-md" />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <XCircle className="h-7 w-7 text-destructive/60" />
        <p className="text-sm text-muted-foreground">{t(($) => $.error.load)}</p>
        <Button type="button" variant="outline" size="sm" onClick={() => refetch()}>
          {t(($) => $.error.retry)}
        </Button>
      </div>
    );
  }

  if (data.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
          <ShieldCheck className="h-6 w-6 text-muted-foreground" />
        </div>
        <p className="text-sm font-medium">{t(($) => $.empty.history_title)}</p>
        <p className="max-w-xs text-xs text-muted-foreground">
          {t(($) => $.empty.history_body)}
        </p>
      </div>
    );
  }

  return (
    <div className="divide-y">
      {data.map((rec) => (
        <div key={rec.id} className="flex items-start gap-4 px-5 py-4">
          <div
            className={`mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full ${
              rec.decision.allowed
                ? "bg-green-100 text-green-600 dark:bg-green-900/30 dark:text-green-400"
                : "bg-red-100 text-red-600 dark:bg-red-900/30 dark:text-red-400"
            }`}
          >
            {rec.decision.allowed ? (
              <CheckCircle className="h-4 w-4" />
            ) : (
              <XCircle className="h-4 w-4" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm font-semibold">
                {rec.actionType}
              </span>
              <Badge
                variant={rec.decision.allowed ? "default" : "destructive"}
                className="text-[10px]"
              >
                {rec.decision.allowed
                  ? t(($) => $.decision.allowed)
                  : t(($) => $.decision.denied)}
              </Badge>
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t(($) => $.table.approver)}: {rec.approverIdentity}
              {" · "}
              <RelativeTime iso={rec.timestamp} />
            </p>
            {rec.verdicts.length > 0 && (
              <div className="mt-2 space-y-1">
                {rec.verdicts.map((v, i) => (
                  <div
                    key={i}
                    className="flex items-start gap-2 rounded-md bg-muted/40 px-2 py-1.5 text-xs"
                  >
                    <span
                      className={`mt-0.5 shrink-0 font-semibold ${
                        v.approve ? "text-green-600" : "text-red-600"
                      }`}
                    >
                      {v.approve ? "✓" : "✗"}
                    </span>
                    <span className="font-mono text-muted-foreground">
                      {v.identity}
                    </span>
                    {v.hardVeto && (
                      <Badge variant="destructive" className="text-[9px] px-1">
                        hard-veto
                      </Badge>
                    )}
                    {v.reasoning && (
                      <span className="text-muted-foreground">
                        — {v.reasoning}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab strip
// ---------------------------------------------------------------------------

type Tab = "pending" | "history";

function TabStrip({
  active,
  onSelect,
  pendingCount,
}: {
  active: Tab;
  onSelect: (t: Tab) => void;
  pendingCount: number | null;
}) {
  const { t } = useT("approvals");
  return (
    <div className="flex shrink-0 gap-0.5 border-b px-5 pb-0">
      {(["pending", "history"] as Tab[]).map((tab) => (
        <button
          key={tab}
          type="button"
          onClick={() => onSelect(tab)}
          className={`relative flex items-center gap-1.5 px-3 py-2.5 text-sm transition-colors ${
            active === tab
              ? "border-b-2 border-foreground font-medium text-foreground"
              : "text-muted-foreground hover:text-foreground"
          }`}
        >
          {tab === "pending" ? t(($) => $.tabs.pending) : t(($) => $.tabs.history)}
          {tab === "pending" && pendingCount !== null && pendingCount > 0 && (
            <span className="min-w-[18px] rounded-full bg-brand px-1 text-center text-[10px] font-semibold text-white">
              {pendingCount > 99 ? "99+" : pendingCount}
            </span>
          )}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ApprovalsPage() {
  const { t } = useT("approvals");
  const [tab, setTab] = useState<Tab>("pending");
  const userName = useAuthStore((s) => s.user?.name ?? "");

  const { data: pendingData } = useQuery<PendingApproval[]>({
    queryKey: ["approvals", "pending"],
    queryFn: () =>
      fetchJson<PendingApproval[]>("/api/approvals/pending?status=pending"),
    refetchInterval: 15_000,
  });

  const pendingCount = pendingData?.length ?? null;

  return (
    <div className="flex h-full flex-col">
      <PageHeader className="justify-between px-5">
        <div className="flex items-center gap-2">
          <ShieldCheck className="h-4 w-4 shrink-0 text-muted-foreground" />
          <h1 className="text-sm font-medium">{t(($) => $.page.title)}</h1>
          {pendingCount !== null && pendingCount > 0 && (
            <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
              {pendingCount} pending
            </span>
          )}
        </div>
        <p className="hidden text-xs text-muted-foreground md:block">
          {t(($) => $.page.tagline)}
        </p>
      </PageHeader>

      <TabStrip active={tab} onSelect={setTab} pendingCount={pendingCount} />

      <div className="flex-1 overflow-y-auto">
        {tab === "pending" ? (
          <PendingTab userName={userName} />
        ) : (
          <HistoryTab />
        )}
      </div>
    </div>
  );
}
