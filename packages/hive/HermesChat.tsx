"use client";

import React, { useCallback, useEffect, useRef, useState } from "react";
import {
  useQuery,
  useMutation,
  useQueryClient,
  useInfiniteQuery,
  type InfiniteData,
} from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useAuthStore } from "@multica/core/auth";
import { useWSEvent } from "@multica/core/realtime";
import { hiveRequest } from "./hiveRequest";
import { HiveHeader } from "./HiveHeader";

interface HermesThread {
  ID: string;
  WorkspaceID: string;
  Title: string;
  CreatedBy: string;
  CreatedAt: string;
}

interface HermesMessage {
  ID: string;
  ThreadID: string;
  WorkspaceID: string;
  AuthorID: string;
  Body: string;
  CreatedAt: string;
}

interface MessageCreatedPayload {
  thread_id: string;
  message: HermesMessage;
}

const PAGE_LIMIT = 30;

export function HermesChat() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const userId = useAuthStore((s) => s.user?.id ?? "");
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [newThreadTitle, setNewThreadTitle] = useState("");
  const [creating, setCreating] = useState(false);
  const [body, setBody] = useState("");
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const queryClient = useQueryClient();

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
  });

  // Pages arrive newest-first. Reverse page order and each page to display oldest at top.
  const messages = React.useMemo<HermesMessage[]>(() => {
    if (!msgData) return [];
    return [...msgData.pages]
      .reverse()
      .flatMap((page) => [...(page ?? [])].reverse());
  }, [msgData]);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages.length]);

  const handleMessageCreated = useCallback(
    (payload: unknown) => {
      const p = payload as MessageCreatedPayload;
      if (p.thread_id !== activeThreadId) return;
      queryClient.setQueryData(
        ["hive", "hermes-messages", wsId, activeThreadId ?? ""],
        (old: InfiniteData<HermesMessage[]> | undefined) => {
          if (!old || old.pages.length === 0) return old;
          const firstPage = old.pages[0] ?? [];
          if (firstPage.some((m) => m.ID === p.message.ID)) return old;
          return {
            ...old,
            pages: [[p.message, ...firstPage], ...old.pages.slice(1)],
          };
        },
      );
    },
    [activeThreadId, queryClient, wsId],
  );

  useWSEvent("hive:message:created", handleMessageCreated);

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
      // Echo the sent message immediately rather than waiting on the WS
      // broadcast (which may be delayed or dropped). The WS handler dedupes
      // by ID so the same message is not appended twice.
      queryClient.setQueryData(
        ["hive", "hermes-messages", wsId, activeThreadId ?? ""],
        (old: InfiniteData<HermesMessage[]> | undefined) => {
          if (!old || old.pages.length === 0) return old;
          const firstPage = old.pages[0] ?? [];
          if (firstPage.some((m) => m.ID === msg.ID)) return old;
          return { ...old, pages: [[msg, ...firstPage], ...old.pages.slice(1)] };
        },
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
              <h1 className="text-sm font-semibold">
                {activeThread?.Title ?? "Thread"}
              </h1>
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
                        {msg.Body}
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
              </div>
              <div ref={messagesEndRef} />
            </div>

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
