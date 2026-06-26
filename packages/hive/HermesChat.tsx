"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  useQuery,
  useMutation,
  useQueryClient,
  useInfiniteQuery,
  type InfiniteData,
} from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useAuthStore } from "@multica/core/auth";
import {
  useWSEvent,
  useWSReconnect,
  useWSStatus,
} from "@multica/core/realtime";
import { hiveRequest } from "./hiveRequest";
import { HiveHeader } from "./HiveHeader";
import { Markdown } from "@multica/ui/markdown";

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

interface HermesThread {
  ID: string;
  WorkspaceID: string;
  Title: string;
  CreatedBy: string;
  CreatedAt: string;
  Model?: string | null;
  TokensTotal?: number | null;
}

interface HermesMessage {
  ID: string;
  ThreadID: string;
  WorkspaceID: string;
  AuthorID: string;
  Body: string;
  CreatedAt: string;
  Role: string;
  TokensUsed?: number | null;
  ContextWindow?: number | null;
  Model?: string | null;
}

interface MessageCreatedPayload {
  thread_id: string;
  message: HermesMessage;
}

interface HermesBridgeStatus {
  bridge: {
    connected: boolean;
    stale: boolean;
    updated_at?: string;
    last_heartbeat_at?: string;
    last_event_at?: string;
    last_connect_at?: string;
    last_error?: string;
  };
  thread: {
    state: string;
    message_id?: string;
    started_at?: string;
    updated_at?: string;
    error?: string;
  };
}

const PAGE_LIMIT = 30;
const ACTIVE_REPLY_THREAD_STATES = new Set(["running", "posting"]);

function upsertMessagePage(
  old: InfiniteData<HermesMessage[]> | undefined,
  message: HermesMessage,
): InfiniteData<HermesMessage[]> {
  if (!old || old.pages.length === 0) {
    return { pages: [[message]], pageParams: [undefined] };
  }
  const firstPage = old.pages[0] ?? [];
  if (firstPage.some((m) => m.ID === message.ID)) return old;
  return {
    ...old,
    pages: [[message, ...firstPage], ...old.pages.slice(1)],
  };
}

export function HermesChat() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const userId = useAuthStore((s) => s.user?.id ?? "");
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [awaitingReplySince, setAwaitingReplySince] = useState<string | null>(null);
  const [newThreadTitle, setNewThreadTitle] = useState("");
  const [creating, setCreating] = useState(false);
  const [body, setBody] = useState("");
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const lastBridgeThreadStatusRef = useRef<string | null>(null);
  const queryClient = useQueryClient();
  const wsStatus = useWSStatus();

  const { data: threads = [], isError: threadsError } = useQuery<HermesThread[]>({
    queryKey: ["hive", "hermes-threads", wsId],
    queryFn: () =>
      hiveRequest(`/hermes-threads?workspace_id=${encodeURIComponent(wsId)}`, wsId),
    enabled: !!wsId,
  });

  useEffect(() => {
    if (!activeThreadId && threads.length > 0) {
      setActiveThreadId(threads[0]?.ID ?? null);
    }
  }, [threads, activeThreadId]);

  const {
    data: msgData,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
    isError: msgsError,
  } = useInfiniteQuery({
    queryKey: ["hive", "hermes-messages", wsId, activeThreadId ?? ""] as const,
    queryFn: ({ pageParam }: { pageParam: { before: string; before_id: string } | undefined }) => {
      const cursorParams = pageParam
        ? `&before=${encodeURIComponent(pageParam.before)}&before_id=${encodeURIComponent(pageParam.before_id)}`
        : "";
      return hiveRequest(
        `/hermes-messages?thread_id=${encodeURIComponent(activeThreadId ?? "")}&workspace_id=${encodeURIComponent(wsId)}&limit=${PAGE_LIMIT}${cursorParams}`,
        wsId,
      ) as Promise<HermesMessage[]>;
    },
    getNextPageParam: (lastPage: HermesMessage[]) => {
      if (lastPage.length < PAGE_LIMIT) return undefined;
      const last = lastPage[lastPage.length - 1];
      if (!last) return undefined;
      return { before: last.CreatedAt, before_id: last.ID };
    },
    initialPageParam: undefined as { before: string; before_id: string } | undefined,
    enabled: !!wsId && !!activeThreadId,
    refetchInterval: activeThreadId
      ? awaitingReplySince || wsStatus !== "connected"
        ? 1000
        : 3000
      : false,
    refetchIntervalInBackground: true,
  });

  const { data: bridgeStatus } = useQuery<HermesBridgeStatus>({
    queryKey: ["hive", "hermes-bridge-status", wsId, activeThreadId ?? ""],
    queryFn: () =>
      hiveRequest(
        `/hermes-bridge-status?thread_id=${encodeURIComponent(activeThreadId ?? "")}&workspace_id=${encodeURIComponent(wsId)}`,
        wsId,
      ),
    enabled: !!wsId && !!activeThreadId,
    refetchInterval: activeThreadId ? 1500 : false,
    refetchIntervalInBackground: true,
  });

  // Pages arrive newest-first. Reverse page order and each page to display oldest at top.
  const messages = useMemo<HermesMessage[]>(() => {
    if (!msgData) return [];
    return [...msgData.pages]
      .reverse()
      .flatMap((page) => [...(page ?? [])].reverse());
  }, [msgData]);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages.length]);

  useEffect(() => {
    if (!awaitingReplySince) return;
    const awaitingSince = Date.parse(awaitingReplySince);
    if (Number.isNaN(awaitingSince)) return;
    const replySeen = messages.some((msg) => {
      if (msg.AuthorID === userId) return false;
      const createdAt = Date.parse(msg.CreatedAt);
      return !Number.isNaN(createdAt) && createdAt >= awaitingSince;
    });
    if (replySeen) setAwaitingReplySince(null);
  }, [awaitingReplySince, messages, userId]);

  useEffect(() => {
    lastBridgeThreadStatusRef.current = null;
  }, [activeThreadId]);

  const handleMessageCreated = useCallback(
    (payload: unknown) => {
      const p = payload as MessageCreatedPayload;
      if (p.thread_id !== activeThreadId) return;
      queryClient.setQueryData(
        ["hive", "hermes-messages", wsId, activeThreadId ?? ""],
        (old: InfiniteData<HermesMessage[]> | undefined) => upsertMessagePage(old, p.message),
      );
    },
    [activeThreadId, queryClient, wsId],
  );

  useWSEvent("hive:message:created", handleMessageCreated);

  useWSReconnect(
    useCallback(() => {
      queryClient.invalidateQueries({ queryKey: ["hive", "hermes-threads", wsId] });
      if (activeThreadId) {
        queryClient.invalidateQueries({
          queryKey: ["hive", "hermes-messages", wsId, activeThreadId],
        });
        queryClient.invalidateQueries({
          queryKey: ["hive", "hermes-bridge-status", wsId, activeThreadId],
        });
      }
    }, [activeThreadId, queryClient, wsId]),
  );

  const createThreadMut = useMutation({
    mutationFn: (title: string) =>
      hiveRequest("/hermes-threads", wsId, {
        method: "POST",
        body: JSON.stringify({ workspace_id: wsId, title, created_by: userId }),
      }),
    onSuccess: (thread: HermesThread) => {
      queryClient.invalidateQueries({ queryKey: ["hive", "hermes-threads", wsId] });
      setActiveThreadId(thread.ID);
      setNewThreadTitle("");
      setCreating(false);
    },
  });

  const sendMut = useMutation({
    mutationFn: (text: string) =>
      hiveRequest("/hermes-messages", wsId, {
        method: "POST",
        body: JSON.stringify({
          thread_id: activeThreadId,
          workspace_id: wsId,
          author_id: userId,
          body: text,
        }),
      }),
    onSuccess: (msg: HermesMessage) => {
      setBody("");
      setAwaitingReplySince(msg.CreatedAt || new Date().toISOString());
      // Echo the sent message immediately rather than waiting on the WS
      // broadcast (which may be delayed or dropped). The WS handler dedupes
      // by ID so the same message is not appended twice.
      queryClient.setQueryData(
        ["hive", "hermes-messages", wsId, activeThreadId ?? ""],
        (old: InfiniteData<HermesMessage[]> | undefined) => upsertMessagePage(old, msg),
      );
    },
  });

  const handleSend = (e: React.FormEvent) => {
    e.preventDefault();
    const text = body.trim();
    if (!text || !activeThreadId) return;
    sendMut.mutate(text);
  };

  const handleThreadKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      const title = newThreadTitle.trim();
      if (title) createThreadMut.mutate(title);
    }
    if (e.key === "Escape") {
      setCreating(false);
      setNewThreadTitle("");
    }
  };

  const activeThread = threads.find((t) => t.ID === activeThreadId);
  const latestMessage = messages[messages.length - 1];
  const latestAssistant = useMemo(
    () => [...messages].reverse().find(
      (m) => m.Role === "assistant" && (m.TokensUsed != null || m.Model != null)
    ) ?? null,
    [messages]
  );
  const asModel = latestAssistant?.Model ?? null;
  const asTokens = latestAssistant?.TokensUsed ?? null;
  const asWindow = latestAssistant?.ContextWindow ?? null;
  const latestMessageFromUser = latestMessage?.AuthorID === userId;
  const bridgeHealthy = !!bridgeStatus?.bridge.connected && !bridgeStatus?.bridge.stale;
  const wsConnected = wsStatus === "connected";
  const threadState = bridgeStatus?.thread.state ?? "unknown";
  const bridgeThreadUpdatedAt = bridgeStatus?.thread.updated_at ?? "";
  const bridgeThreadMessageId = bridgeStatus?.thread.message_id ?? "";
  const bridgeError = bridgeStatus?.thread.error || bridgeStatus?.bridge.last_error;

  useEffect(() => {
    if (!activeThreadId) return;
    const bridgeThreadStatusKey = `${threadState}|${bridgeThreadMessageId}|${bridgeThreadUpdatedAt}`;
    if (!bridgeThreadUpdatedAt || bridgeThreadStatusKey === lastBridgeThreadStatusRef.current) return;
    lastBridgeThreadStatusRef.current = bridgeThreadStatusKey;
    if (bridgeThreadMessageId && messages.some((msg) => msg.ID === bridgeThreadMessageId)) return;
    queryClient.invalidateQueries({
      queryKey: ["hive", "hermes-messages", wsId, activeThreadId ?? ""],
    });
  }, [
    activeThreadId,
    bridgeThreadMessageId,
    bridgeThreadUpdatedAt,
    messages,
    queryClient,
    threadState,
    wsId,
  ]);

  const sendErrorText = useMemo(() => {
    if (!sendMut.isError) return null;
    const raw = sendMut.error instanceof Error ? sendMut.error.message : "Failed to send message.";
    if (raw.includes(" 401")) {
      return "Message failed to send: your Hive session is not authenticated.";
    }
    if (raw.includes(" 403")) {
      return "Message failed to send: CSRF or workspace auth was rejected.";
    }
    if (raw.includes(" 400")) {
      return "Message failed to send: thread context was invalid.";
    }
    return `Message failed to send: ${raw}`;
  }, [sendMut.error, sendMut.isError]);

  const replyStatus = useMemo(() => {
    if (!activeThreadId) return null;
    if (sendMut.isPending) {
      return { tone: "muted", text: "Sending message…" };
    }
    if (sendErrorText) {
      return { tone: "error", text: sendErrorText };
    }
    if (ACTIVE_REPLY_THREAD_STATES.has(threadState)) {
      return { tone: "active", text: "Hermes is replying…" };
    }
    if (threadState === "error") {
      return {
        tone: "error",
        text: bridgeError ? `Hermes reply failed: ${bridgeError}` : "Hermes reply failed.",
      };
    }
    if (!bridgeHealthy) {
      return {
        tone: "error",
        text: bridgeError ? `Hermes bridge offline: ${bridgeError}` : "Hermes bridge is offline.",
      };
    }
    if (awaitingReplySince && wsStatus === "reconnecting") {
      return { tone: "warning", text: "Realtime disconnected. Polling for Hermes updates…" };
    }
    if (awaitingReplySince) {
      return { tone: "warning", text: "Message posted. Waiting for Hermes to pick it up…" };
    }
    if (latestMessageFromUser) {
      return { tone: "warning", text: "Waiting for Hermes to start replying…" };
    }
    return null;
  }, [
    activeThreadId,
    awaitingReplySince,
    bridgeError,
    bridgeHealthy,
    latestMessageFromUser,
    sendErrorText,
    sendMut.isPending,
    threadState,
    wsStatus,
  ]);

  const bridgeHealth = useMemo(() => {
    if (threadState === "error") {
      return { dotClass: "bg-destructive", textClass: "text-destructive", label: "Reply failed" };
    }
    if (!bridgeStatus?.bridge.connected) {
      return { dotClass: "bg-destructive", textClass: "text-destructive", label: "Bridge offline" };
    }
    if (bridgeStatus.bridge.stale) {
      return { dotClass: "bg-warning", textClass: "text-warning", label: "Bridge stale" };
    }
    if (wsStatus === "reconnecting") {
      return { dotClass: "bg-warning animate-pulse", textClass: "text-warning", label: "Realtime reconnecting" };
    }
    if (ACTIVE_REPLY_THREAD_STATES.has(threadState)) {
      return { dotClass: "bg-primary animate-pulse", textClass: "text-primary", label: "Hermes working" };
    }
    if (awaitingReplySince || latestMessageFromUser) {
      return { dotClass: "bg-warning animate-pulse", textClass: "text-warning", label: "Waiting for Hermes" };
    }
    if (!wsConnected) {
      return { dotClass: "bg-muted-foreground/40", textClass: "text-muted-foreground", label: "Realtime offline" };
    }
    return { dotClass: "bg-success", textClass: "text-success", label: "Bridge healthy" };
  }, [
    awaitingReplySince,
    bridgeStatus?.bridge.connected,
    bridgeStatus?.bridge.stale,
    latestMessageFromUser,
    threadState,
    wsConnected,
    wsStatus,
  ]);

  const showHermesTypingIndicator =
    !!activeThreadId &&
    bridgeHealthy &&
    (ACTIVE_REPLY_THREAD_STATES.has(threadState) || !!awaitingReplySince || latestMessageFromUser);

  return (
    <div className="flex h-full flex-col">
      <HiveHeader title="Hermes" />
      <div className="flex min-h-0 flex-1">
      {/* Thread list */}
      <aside className="flex w-60 shrink-0 flex-col border-r">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <h2 className="text-sm font-semibold">Threads</h2>
          <button
            type="button"
            className="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent/70 hover:text-foreground"
            onClick={() => setCreating(true)}
            title="New thread"
          >
            <span className="text-base leading-none">+</span>
          </button>
        </div>

        {creating && (
          <div className="border-b p-2">
            <input
              autoFocus
              className="w-full rounded border bg-background px-2 py-1 text-sm outline-none focus:ring-1 focus:ring-primary"
              placeholder="Thread title…"
              value={newThreadTitle}
              onChange={(e) => setNewThreadTitle(e.target.value)}
              onKeyDown={handleThreadKeyDown}
              onBlur={() => {
                if (!newThreadTitle.trim()) setCreating(false);
              }}
            />
          </div>
        )}

        <ul className="flex-1 overflow-y-auto py-1">
          {threadsError && (
            <li className="px-4 py-2 text-xs text-destructive">
              Failed to load threads.
            </li>
          )}
          {!threadsError && threads.length === 0 && (
            <li className="px-4 py-3 text-xs text-muted-foreground">
              No threads yet.
            </li>
          )}
          {threads.map((thread) => (
            <li key={thread.ID}>
              <button
                type="button"
                onClick={() => setActiveThreadId(thread.ID)}
                className={`w-full px-4 py-2 text-left text-sm transition-colors hover:bg-sidebar-accent/70 ${
                  thread.ID === activeThreadId
                    ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                    : "text-muted-foreground"
                }`}
              >
                <span className="block truncate">{thread.Title || "Untitled"}</span>
              </button>
            </li>
          ))}
        </ul>
      </aside>

      {/* Message panel */}
      <div className="flex min-w-0 flex-1 flex-col">
        {!activeThreadId ? (
          <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
            Select or create a thread to start chatting.
          </div>
        ) : (
          <>
            <header className="border-b px-6 py-3">
              <div className="flex items-center justify-between gap-3">
                <h1 className="text-sm font-semibold">
                  {activeThread?.Title ?? "Thread"}
                </h1>
                <div className="flex items-center gap-2 min-w-0">
                  {asModel && (
                    <div title={asModel} className="inline-flex items-center gap-2 rounded-full border px-2.5 py-1 text-[11px] text-muted-foreground max-w-[12rem] truncate">
                      <span className="truncate">{asModel}</span>
                    </div>
                  )}
                  {asTokens != null && (
                    <div className="inline-flex items-center gap-2 rounded-full border px-2.5 py-1 text-[11px] text-muted-foreground">
                      {fmtTokens(asTokens)}{asWindow ? ` / ${fmtTokens(asWindow)} · ${Math.round((asTokens / asWindow) * 100)}%` : ""}
                    </div>
                  )}
                  <div className={`inline-flex items-center gap-2 rounded-full border px-2.5 py-1 text-[11px] ${bridgeHealth.textClass}`}>
                    <span aria-hidden className={`size-2 rounded-full ${bridgeHealth.dotClass}`} />
                    <span>{bridgeHealth.label}</span>
                  </div>
                </div>
              </div>
            </header>

            <div className="flex-1 overflow-y-auto px-6 py-4">
              {hasNextPage && (
                <div className="mb-4 flex justify-center">
                  <button
                    type="button"
                    disabled={isFetchingNextPage}
                    onClick={() => fetchNextPage()}
                    className="rounded border px-3 py-1 text-xs text-muted-foreground hover:bg-sidebar-accent/70 disabled:opacity-50"
                  >
                    {isFetchingNextPage ? "Loading…" : "Load older messages"}
                  </button>
                </div>
              )}

              {msgsError && (
                <p className="mb-4 text-sm text-destructive">
                  Failed to load messages.
                </p>
              )}

              <div className="flex flex-col gap-3">
                {messages.map((msg) => {
                  const role = msg.Role || "assistant";

                  if (role === "tool") {
                    return (
                      <div key={msg.ID} className="flex flex-col gap-0.5 items-start">
                        <div className="rounded px-3 py-1 text-xs text-muted-foreground bg-muted/60 max-w-[80%] truncate">
                          {msg.Body}
                        </div>
                      </div>
                    );
                  }

                  if (role === "reasoning") {
                    return (
                      <div key={msg.ID} className="flex flex-col gap-0.5 items-start">
                        <details className="text-xs text-muted-foreground">
                          <summary className="cursor-pointer select-none">ð­ thinking…</summary>
                          <p className="mt-1 whitespace-pre-wrap text-muted-foreground">{msg.Body}</p>
                        </details>
                      </div>
                    );
                  }

                  // assistant + user + legacy — unchanged
                  const isOwn = msg.AuthorID === userId;
                  return (
                    <div
                      key={msg.ID}
                      className={`flex flex-col gap-0.5 ${isOwn ? "items-end" : "items-start"}`}
                    >
                      <div
                        className={`max-w-[70%] rounded-lg px-3 py-2 text-sm ${
                          isOwn
                            ? "bg-primary text-primary-foreground"
                            : "bg-muted text-foreground"
                        }`}
                      >
                        {isOwn ? msg.Body : <Markdown mode="minimal">{msg.Body}</Markdown>}
                      </div>
                      <span className="text-[10px] text-muted-foreground">
                        {new Date(msg.CreatedAt).toLocaleTimeString([], {
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                      </span>
                    </div>
                  );
                })}
                {showHermesTypingIndicator && (
                  <div className="flex flex-col gap-0.5 items-start">
                    <div className="max-w-[70%] rounded-lg bg-muted px-3 py-2 text-sm text-foreground">
                      <span className="inline-flex items-center gap-2">
                        <span aria-hidden className="inline-flex items-center gap-1">
                          <span className="size-1.5 rounded-full bg-primary animate-pulse [animation-delay:0ms]" />
                          <span className="size-1.5 rounded-full bg-primary animate-pulse [animation-delay:150ms]" />
                          <span className="size-1.5 rounded-full bg-primary animate-pulse [animation-delay:300ms]" />
                        </span>
                        <span>Hermes is working…</span>
                      </span>
                    </div>
                  </div>
                )}
              </div>
              <div ref={messagesEndRef} />
            </div>

            {replyStatus && (
              <div
                className={`border-t px-4 py-2 text-xs ${
                  replyStatus.tone === "error"
                    ? "text-destructive"
                    : replyStatus.tone === "warning"
                      ? "text-warning"
                      : replyStatus.tone === "active"
                        ? "text-foreground"
                        : "text-muted-foreground"
                }`}
              >
                <span className="inline-flex items-center gap-2">
                  {replyStatus.tone === "active" && (
                    <span aria-hidden className="size-2 rounded-full bg-success animate-pulse" />
                  )}
                  {replyStatus.tone === "warning" && (
                    <span aria-hidden className="size-2 rounded-full bg-warning animate-pulse" />
                  )}
                  {replyStatus.text}
                </span>
              </div>
            )}

            <form onSubmit={handleSend} className="border-t px-4 py-3">
              <div className="flex gap-2">
                <input
                  className="min-w-0 flex-1 rounded-lg border bg-background px-3 py-2 text-sm outline-none focus:ring-1 focus:ring-primary"
                  placeholder="Type a message…"
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                  disabled={sendMut.isPending}
                />
                <button
                  type="submit"
                  disabled={sendMut.isPending || !body.trim()}
                  className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                >
                  Send
                </button>
              </div>
            </form>
          </>
        )}
      </div>
      </div>
    </div>
  );
}
