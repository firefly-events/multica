# Hermes Chat UX Port for Multica — Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** Port the useful Hermes web/desktop chat and session UX into the Multica Hive Hermes experience, with truthful reply state driven by real backend/bridge events rather than cosmetic polling.

**Architecture:** Multica should keep owning thread/message persistence, workspace auth, and realtime fanout. Instead of trying to drop Hermes desktop UI in verbatim, add a thin Hermes-turn event protocol to the Multica Hive integration, then port the Hermes thread/composer/status UX onto a Multica adapter that consumes those events. Execute in this order: **(1) architecture pass / adapter contract → (3) protocol-first backend and bridge events → (2) UI port onto the new contract.**

**Tech Stack:** Multica Go server (`server/internal/hive`), Python bridge (`docs/hive-plugin/hermes_bridge.py`), React/TanStack Query Hive UI (`packages/hive/HermesChat.tsx`), Hermes desktop reference implementation (`apps/desktop/src/components/assistant-ui/thread.tsx`, `apps/desktop/src/app/session/hooks/use-message-stream.ts`, `apps/shared/src/json-rpc-gateway.ts`).

---

## What exists today

### Multica side
- `packages/hive/HermesChat.tsx`
  - owns threads, message list, send box, websocket invalidation
  - has only a heuristic `awaitingReplySince` state
  - treats "I sent a message" and "Hermes bridge actually claimed the work" as the same thing
- `server/internal/hive/router.go`
  - exposes `GET/POST /hermes-threads`
  - exposes `GET/POST /hermes-messages`
  - publishes only `hive:message:created`
- `server/internal/hive/store.go`
  - persists only `HermesThread` and `HermesMessage`
  - no per-thread turn state, bridge health, or in-flight turn model
- `docs/hive-plugin/hermes_bridge.py`
  - maps Hive thread → Hermes session
  - listens for `hive:message:created`
  - posts final reply back as another Hive message
  - exposes no truthful intermediate lifecycle to the UI

### Hermes reference side
- `apps/shared/src/json-rpc-gateway.ts`
  - event transport used by Hermes UI
- `apps/desktop/src/app/session/hooks/use-message-stream.ts`
  - consumes `message.start`, `message.delta`, `message.complete`, `tool.*`, `status.update`, `error`
  - derives real busy / awaiting / failure / needs-input state from backend events
- `apps/desktop/src/components/assistant-ui/thread.tsx`
  - has reusable UX ideas worth porting:
    - bottom-of-thread response loading indicator
    - stream stall indicator
    - clear session/response loading separation
    - error-aware running state handling

## Core architectural conclusion

A literal UI transplant is **not** the right first move.

Hermes UI is coupled to a richer event contract than Multica currently has. The right path is:
1. define a **Multica-specific Hermes turn event model**,
2. make the bridge/server publish it truthfully,
3. port Hermes thread UX pieces onto that adapter.

That yields a believable Hermes experience in Multica without needing to embed Hermes’s entire JSON-RPC gateway and session system.

---

# Phase 1 — Architecture pass and adapter contract

## Target state

Introduce a Multica-native "Hermes thread runtime" model with explicit per-thread status instead of inferring reply state from message timestamps.

### Proposed thread runtime states
- `idle` — no pending user turn
- `queued` — user message accepted, bridge has not yet claimed it
- `claimed` — bridge has explicitly claimed the turn
- `running` — Hermes session is actively processing the turn
- `streaming` — optional future state if bridge can emit partial deltas
- `posting` — final reply ready, being posted back into Multica
- `completed` — turn finished successfully
- `error` — turn failed; include message
- `bridge_unhealthy` — no heartbeat / bridge offline / stale worker

### Proposed sources of truth
1. **Server-side thread runtime snapshot**
   - queryable by UI via HTTP
2. **Workspace realtime events**
   - push runtime transitions into the UI as they happen
3. **Bridge heartbeat**
   - tells UI whether lack of reply means "still working" or "nobody is home"

### New API surface to add
- `GET /api/plugins/hive/hermes-thread-runtime?thread_id=...`
  - returns runtime snapshot for a single thread
- `GET /api/plugins/hive/hermes-bridge-status`
  - returns workspace-scoped bridge health snapshot
- workspace websocket events:
  - `hive:hermes:turn.updated`
  - `hive:hermes:bridge.status`

### Runtime snapshot shape (v1)
```json
{
  "thread_id": "uuid",
  "workspace_id": "uuid",
  "state": "queued|claimed|running|posting|completed|error|bridge_unhealthy|idle",
  "current_user_message_id": "uuid|null",
  "claimed_at": "timestamp|null",
  "started_at": "timestamp|null",
  "last_activity_at": "timestamp|null",
  "completed_at": "timestamp|null",
  "error": {
    "message": "string"
  },
  "bridge": {
    "healthy": true,
    "last_heartbeat_at": "timestamp|null",
    "worker_id": "string|null"
  }
}
```

### Minimal event contract (v1)
This is intentionally smaller than Hermes JSON-RPC.

```json
{ "type": "hive:hermes:turn.updated", "payload": {
  "thread_id": "uuid",
  "workspace_id": "uuid",
  "state": "queued|claimed|running|posting|completed|error",
  "current_user_message_id": "uuid",
  "last_activity_at": "timestamp",
  "error": { "message": "optional" }
}}
```

```json
{ "type": "hive:hermes:bridge.status", "payload": {
  "workspace_id": "uuid",
  "healthy": true,
  "last_heartbeat_at": "timestamp",
  "worker_id": "local-hermes-bridge"
}}
```

### Why not full `message.delta` now?
The current bridge calls Hermes through `/api/sessions/{id}/chat` and receives the final answer after the turn completes. There is no existing partial-token stream in this bridge path. So v1 should focus on **truthful turn ownership and failure state**, not fake streaming.

Streaming can be added later if the bridge moves to a streaming Hermes API surface.

## Files to modify in Phase 1
- Create: `docs/hive-plugin/PLAN-hermes-ui-port.md` (this file)
- Future server contract files:
  - `server/internal/hive/router.go`
  - `server/internal/hive/store.go`
  - likely new file: `server/internal/hive/runtime.go`
  - likely tests in `server/internal/hive/store_integration_test.go`
- Future bridge files:
  - `docs/hive-plugin/hermes_bridge.py`
- Future UI files:
  - `packages/hive/HermesChat.tsx`
  - likely new adapter/status components under `packages/hive/`

## Acceptance criteria for Phase 1
- Contract documented in repo
- State model names are final enough to implement
- We explicitly choose **protocol-first before UI port**

---

# Phase 3 — Protocol-first backend and bridge work

## Objective
Make reply state truthful before any UI port by teaching the bridge and server to publish real turn lifecycle and bridge health.

## Recommended implementation shape

### Server persistence
Add a small runtime table or equivalent durable cache for per-thread turn state.

Preferred DB object:
- new table like `hive.hermes_thread_runtime`

Suggested columns:
- `thread_id uuid primary key`
- `workspace_id uuid not null`
- `state text not null`
- `current_user_message_id uuid null`
- `claimed_at timestamptz null`
- `started_at timestamptz null`
- `last_activity_at timestamptz null`
- `completed_at timestamptz null`
- `error_message text null`
- `bridge_worker_id text null`
- `bridge_last_heartbeat_at timestamptz null`
- `updated_at timestamptz not null default now()`

Why DB instead of process memory:
- truthful after page refresh
- debuggable from SQL
- survives server restart better than in-memory map
- supports bridge health timeout logic on the server side

### Bridge lifecycle changes
Extend `docs/hive-plugin/hermes_bridge.py` to emit lifecycle transitions:

1. on seeing a human `hive:message:created`
   - mark `queued` or `claimed`
2. before calling Hermes session chat
   - mark `running`
3. while waiting on Hermes
   - heartbeat update every few seconds
4. before POSTing reply back to Multica
   - mark `posting`
5. after successful reply POST
   - mark `completed`
6. on exception / timeout
   - mark `error` with message

### Server routes to add
- `POST /hermes-thread-runtime/claim`
- `POST /hermes-thread-runtime/activity`
- `POST /hermes-thread-runtime/complete`
- `POST /hermes-thread-runtime/error`
- `POST /hermes-bridge-status/heartbeat`
- `GET /hermes-thread-runtime?thread_id=...`
- `GET /hermes-bridge-status`

If route count feels too high, collapse writes into one endpoint:
- `POST /hermes-thread-runtime/events`

with body:
```json
{
  "thread_id": "uuid",
  "workspace_id": "uuid",
  "event": "claimed|running|posting|completed|error|heartbeat",
  "current_user_message_id": "uuid|null",
  "error_message": "optional"
}
```

### Realtime broadcast
Whenever runtime state changes, `server/internal/hive/router.go` should broadcast:
- `hive:hermes:turn.updated`
- `hive:hermes:bridge.status`

This mirrors the existing `hive:message:created` pattern already in `handleCreateMessage()`.

### Truthfulness rules
The UI may show:
- **"Waiting for Hermes"** only if thread runtime is `claimed|running|posting`
- **"Bridge offline"** if heartbeat is stale
- **"Reply failed"** if runtime is `error`
- **nothing** if only a user message exists but no claim arrived and bridge is stale/unhealthy

It must **not** show an active reply indicator based solely on:
- newest message being from the user
- POST success of `/hermes-messages`
- local optimistic send state alone

## Files to modify in Phase 3
- Modify: `server/internal/hive/router.go`
- Modify: `server/internal/hive/store.go`
- Create: `server/internal/hive/runtime.go`
- Modify: `server/internal/hive/store_integration_test.go`
- Modify: `docs/hive-plugin/hermes_bridge.py`
- Possibly remove or supersede: `server/internal/hive/bridge_status.go` if the DB-backed route supersedes the file-based approach

## Phase 3 verification
### Server tests
Run:
```bash
go test ./server/internal/hive -run Hermes -v
```
Expected:
- runtime create/update/read passes
- workspace scoping enforced
- stale heartbeat logic works

### Manual end-to-end
1. start Multica backend
2. start Hermes bridge
3. send a Hive Hermes message
4. verify runtime transitions:
   - queued/claimed → running → posting → completed
5. kill bridge
6. send another message
7. verify UI/runtime shows bridge unhealthy or no claim, not fake waiting

---

# Phase 2 — Port Hermes thread UX onto the new adapter

## Objective
After truthful runtime exists, port the Hermes UX pieces that improve clarity without requiring Hermes’s full gateway protocol.

## What to port first

### Port candidate A: bottom-of-thread loading indicator
Reference:
- `apps/desktop/src/components/assistant-ui/thread.tsx:347-361`

Adaptation:
- render when runtime state is `claimed|running|posting`
- label from Multica runtime state, not Hermes gateway text

### Port candidate B: stalled-response indicator
Reference:
- `apps/desktop/src/components/assistant-ui/thread.tsx:377-419`

Adaptation:
- use `last_activity_at` age, not token-delta silence
- if heartbeat is healthy but activity is stale, show "Hermes is still working"
- if heartbeat is stale, switch to error/offline copy instead of pretending progress

### Port candidate C: session-vs-response separation
Reference:
- `apps/desktop/src/components/assistant-ui/thread.tsx:203-212`

Adaptation:
- separate loading the thread history from loading a reply
- current `HermesChat.tsx` conflates initial history and pending reply state

### Port candidate D: error row / failed turn copy
Reference behavior:
- Hermes UI treats `error` as end-of-turn and surfaces it explicitly

Adaptation:
- when runtime becomes `error`, show failure state inline near composer/thread footer
- do not keep spinner active

## What not to port yet
- full Hermes Assistant UI primitives
- tool call transcript rendering
- subagent UI
- clarify/approval overlays
- streaming delta handling
- full session sidebar semantics from Hermes desktop

Those all depend on Hermes gateway protocol that Multica does not currently expose.

## Proposed UI refactor shape
Instead of growing `packages/hive/HermesChat.tsx` further, split it into:
- `packages/hive/HermesChat.tsx` — shell/orchestration
- `packages/hive/hermes-runtime.ts` — query hooks + websocket event reducer
- `packages/hive/HermesThreadList.tsx`
- `packages/hive/HermesMessagePane.tsx`
- `packages/hive/HermesReplyStatus.tsx`
- `packages/hive/HermesComposer.tsx`

This makes the future port of Hermes UX patterns much easier.

## Adapter hook shape
```ts
interface HermesThreadRuntime {
  state: 'idle' | 'queued' | 'claimed' | 'running' | 'posting' | 'completed' | 'error' | 'bridge_unhealthy'
  currentUserMessageId: string | null
  lastActivityAt: string | null
  completedAt: string | null
  errorMessage: string | null
  bridgeHealthy: boolean
  bridgeLastHeartbeatAt: string | null
}
```

```ts
function useHermesThreadRuntime(workspaceId: string, threadId: string | null): {
  runtime: HermesThreadRuntime | null
  isLoading: boolean
}
```

## Files to modify in Phase 2
- Modify: `packages/hive/HermesChat.tsx`
- Create: `packages/hive/hermes-runtime.ts`
- Create: `packages/hive/HermesReplyStatus.tsx`
- Possibly create: `packages/hive/HermesComposer.tsx`
- Possibly create tests near the new hook/components if the package already has test infra

## Phase 2 verification
Manual acceptance:
1. send a message while bridge healthy
2. UI shows claimed/running state only after bridge claim
3. reply arrives and indicator clears
4. stop bridge, send message
5. UI does not pretend Hermes is replying
6. bridge error shows explicit failure copy

---

# Bite-sized task sequence

### Task 1: Add server-side runtime model
**Objective:** Persist truthful per-thread runtime state.

**Files:**
- Modify: `server/internal/hive/store.go`
- Create: `server/internal/hive/runtime.go`
- Test: `server/internal/hive/store_integration_test.go`

### Task 2: Add runtime read/write routes
**Objective:** Expose runtime state and bridge heartbeats via Hive plugin endpoints.

**Files:**
- Modify: `server/internal/hive/router.go`
- Modify: `server/internal/hive/runtime.go`
- Test: `server/internal/hive/store_integration_test.go`

### Task 3: Broadcast runtime websocket events
**Objective:** Push runtime changes into the existing workspace realtime channel.

**Files:**
- Modify: `server/internal/hive/router.go`
- Test: add/extend Hive realtime tests if present

### Task 4: Teach bridge to claim/start/complete/error turns
**Objective:** Make the bridge publish truthful lifecycle events.

**Files:**
- Modify: `docs/hive-plugin/hermes_bridge.py`

### Task 5: Add bridge heartbeat loop
**Objective:** Surface bridge health distinctly from per-thread progress.

**Files:**
- Modify: `docs/hive-plugin/hermes_bridge.py`

### Task 6: Extract a runtime hook in Hive UI
**Objective:** Consume runtime snapshots + websocket updates outside the giant chat component.

**Files:**
- Create: `packages/hive/hermes-runtime.ts`
- Modify: `packages/hive/HermesChat.tsx`

### Task 7: Add truthful reply-status UI
**Objective:** Render real waiting/error/offline states in the chat pane/composer area.

**Files:**
- Create: `packages/hive/HermesReplyStatus.tsx`
- Modify: `packages/hive/HermesChat.tsx`

### Task 8: Port Hermes loading/stall/error UX patterns
**Objective:** Bring over the best parts of Hermes thread UX using the new adapter.

**Files:**
- Modify: `packages/hive/HermesChat.tsx`
- Modify/Create: `packages/hive/HermesMessagePane.tsx` if extracted

---

# Key decisions captured here
- Do **not** fake a typing indicator from `awaitingReplySince`.
- Do **not** attempt a full Hermes gateway/UI transplant first.
- Use a **protocol-first** adapter layer.
- Prefer a **small truthful event model** over prematurely cloning Hermes JSON-RPC events.
- Port **UX patterns**, not the entire Hermes desktop implementation.

---

# Suggested execution order
1. implement runtime persistence + endpoints
2. teach bridge lifecycle + heartbeat publishing
3. wire realtime updates
4. build Multica runtime hook
5. port reply-status/loading/error UX
6. only then consider optional streaming or deeper Hermes UI reuse

---

# Notes from current repo state
- `server/internal/hive/bridge_status.go` is present but not integrated; treat it as exploratory work, not settled architecture.
- `packages/hive/HermesChat.tsx` currently contains a local-only `awaitingReplySince` heuristic that should be retired once runtime state exists.
- Existing docs worth keeping aligned:
  - `docs/hive-plugin/hermes-bridge-spec.md`
  - `docs/hive-plugin/PLAN-session-per-thread.md`

---

# Ready-for-implementation summary
This feature should be built as a **Multica Hermes runtime protocol + adapter-based UI port**, not as a direct copy of Hermes desktop chat internals. The smallest high-value outcome is truthful reply state and bridge health. Once that exists, porting Hermes’s loading/stall/error UX becomes straightforward and honest.
