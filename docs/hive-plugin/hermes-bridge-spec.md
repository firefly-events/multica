# Hermes ↔ HiveChat two-way bridge — implementation spec

**Audience:** the always-on local Hermes agent (the implementer/executor of this doc).
**Status:** spec for handoff. Nothing in Multica/Hive needs to change — the messaging
substrate (epic `multica-plugin-ui`, story mpu-5) already exposes everything below. This
is a *client* to build and run beside Multica.

---

## 1. What this is

HiveChat (the "Hermes" view inside Multica) is a persisted message board with realtime
broadcast — **no responder is wired in**. This bridge makes it two-way:

- **Inbound** — a human posts in a thread → Multica broadcasts it over WebSocket → Hermes
  receives it, thinks, and posts a reply → the reply broadcasts back and appears in the UI.
- **Outbound** — Hermes can also *initiate*: post a message into any thread at any time
  (alerts, nudges, async results). Same POST path; the user just sees it arrive.

Hermes is an **external WebSocket subscriber + HTTP poster**. It is decoupled: Multica
owns storage + broadcast; Hermes owns the brain. If Hermes is offline, messages still
persist and simply go unanswered until it reconnects.

```
  human ──POST /hermes-messages──▶ Multica ──hive:message:created (WS)──▶ Hermes
                                      ▲                                      │
                                      └──────POST /hermes-messages──────────┘
                                              (reply, author = Hermes bot)
```

---

## 2. Prerequisites (one-time setup)

1. **A dedicated bot user** for Hermes. Its user id becomes the `author_id` of every
   message Hermes sends (the server derives author from the authenticated token — Hermes
   never sets `author_id` itself). Create/invite a member named e.g. `hermes-bot`, or
   reuse an existing service account. Record its **user UUID** → `HERMES_BOT_USER_ID`.
2. **A Personal Access Token (PAT)** for that bot user. PATs start with `mul_` and, unlike
   cookie sessions, **bypass CSRF** — exactly what a non-browser agent needs.
   - Mint via `POST /api/tokens` (authenticated as the bot user; see `CreatePATRequest`
     in `server/internal/handler/personal_access_token.go`) or the Multica UI under
     Settings → API tokens.
   - Store the returned token → `HERMES_PAT`. It is shown once.
3. **The workspace UUID** to operate in → `HERMES_WORKSPACE_ID`
   (e.g. `plugin-hive` = `21c6d282-d6b4-4b25-8d0d-a85e96038416`).

---

## 3. Auth model (use for BOTH the WS and every HTTP call)

- Header on HTTP: `Authorization: Bearer ${HERMES_PAT}`
- **No cookie, no CSRF token** — PAT auth (`mul_*`) is exempt from CSRF
  (`server/internal/middleware/auth.go`: CSRF is enforced only `if fromCookie`).
- Also send `X-Workspace-ID: ${HERMES_WORKSPACE_ID}` on Hive API calls.
- The server sets the request actor from the token; the Hive handlers read the author
  from that, not from the request body (post-`mpu-fix-1` hardening). So you cannot, and
  need not, spoof `author_id`.

---

## 4. Inbound — subscribe to the live queue (WebSocket)

**Connect:** `ws://localhost:8080/ws?workspace_id=${HERMES_WORKSPACE_ID}`
(through the Multica backend; in prod use the deployed host + `wss://`).

**Authenticate:** immediately after the socket opens, send one JSON frame:

```json
{ "type": "auth", "payload": { "token": "mul_…the PAT…" } }
```

The server responds with `{"type":"auth_ack"}` on success. The token is nested
inside a `payload` object — a flat `{"token": "…"}` at the top level is rejected
with `"expected auth message as first frame"`.

On connect the server subscribes you to the `workspace` scope, so you receive every
workspace-scoped broadcast (`server/internal/realtime/hub.go`).

**Events you care about** arrive as JSON frames:

```json
{
  "type": "hive:message:created",
  "payload": {
    "thread_id": "<uuid>",
    "message": {
      "ID": "<uuid>",
      "ThreadID": "<uuid>",
      "WorkspaceID": "<uuid>",
      "AuthorID": "<uuid>",
      "Body": "the text",
      "CreatedAt": "2026-06-15T…Z"
    }
  }
}
```

Ignore all other `type`s. (You may also see daemon/issue/etc. frames — filter on
`type === "hive:message:created"`.)

---

## 5. Loop guard (MANDATORY)

When Hermes posts a reply, the server broadcasts *that* message too — which Hermes will
receive. **Do not reply to your own messages or you will infinite-loop.**

```
on hive:message:created:
    if message.AuthorID == HERMES_BOT_USER_ID:   # it's our own echo
        skip
    if already_handled(message.ID):              # idempotency, see §8
        skip
    handle(message)
```

---

## 6. Read thread context (HTTP GET)

Before answering, pull recent history for the thread:

```
GET /api/plugins/hive/hermes-messages?thread_id=<id>&workspace_slug=plugin-hive&limit=30
Authorization: Bearer ***
X-Workspace-Slug: plugin-hive
```

**Important:** The Hive plugin endpoints are behind `RequireWorkspaceMember` middleware
which resolves the workspace via `resolveWorkspaceUUID`. This resolver checks
`workspace_slug` (query param or `X-Workspace-Slug` header) **first**, then falls
back to `workspace_id` / `X-Workspace-ID`. In practice, `workspace_slug` is the
reliable param — `workspace_id` as a query param alone may return 400 on some
middleware paths. Always send `workspace_slug` (or `X-Workspace-Slug`) alongside
`workspace_id` for compatibility.

Returns newest-first `HermesMessage[]` (same shape as §4). For older pages use the tuple
cursor `&before=<CreatedAt>&before_id=<ID>` (both required together, or you get 400 —
post-`mpu-fix-2`).

---

## 7. Reply / initiate (HTTP POST)

Same endpoint for responding and for proactively starting a message:

```
POST /api/plugins/hive/hermes-messages?workspace_slug=plugin-hive
Authorization: Bearer ***
Content-Type: application/json
X-Workspace-Slug: plugin-hive

{ "thread_id": "<uuid>", "workspace_id": "${HERMES_WORKSPACE_ID}", "body": "…reply…" }
```

Include `?workspace_slug=plugin-hive` and `X-Workspace-Slug: plugin-hive` per the
note in §6. The `workspace_id` in the body is validated against the
middleware-resolved workspace.

- `author_id` is **ignored** if sent — the server stamps it from the PAT's user.
- Returns `201` + the created message. The server also broadcasts it (so the human's UI
  appends it live). Hermes will see its own broadcast → caught by the §5 loop guard.

To start a brand-new thread (proactive, not tied to an existing one):

```
POST /api/plugins/hive/hermes-threads?workspace_slug=plugin-hive
{ "workspace_id": "${HERMES_WORKSPACE_ID}", "title": "…" }
→ 201 { ID, … }   then POST a message with that thread_id.
```

---

## 8. Idempotency & delivery

- Track handled `message.ID`s (in-memory set + small persisted ring is enough) so a
  reconnect that replays recent frames doesn't double-answer.
- On (re)connect, do a catch-up GET on active threads for any human message newer than the
  last one you answered, in case you were offline when it was posted (WS only delivers
  while connected — there is no server-side replay/queue).

---

## 9. Reference loop (pseudocode)

```python
ws = connect(f"ws://{HOST}/ws?workspace_id={WS_ID}")
ws.send({"type": "auth", "token": PAT})

for frame in ws:
    if frame.type != "hive:message:created":
        continue
    msg = frame.payload.message
    if msg.AuthorID == BOT_USER_ID or seen(msg.ID):
        continue
    mark_seen(msg.ID)

    history = http_get(f"/api/plugins/hive/hermes-messages?thread_id={msg.ThreadID}"
                       f"&workspace_id={WS_ID}&limit=30")
    reply = llm(history, msg.Body)          # the Hermes/Nous brain
    http_post("/api/plugins/hive/hermes-messages",
              {"thread_id": msg.ThreadID, "workspace_id": WS_ID, "body": reply})
# on disconnect: reconnect with backoff, re-auth, run §8 catch-up.
```

All HTTP carries `Authorization: Bearer PAT` + `X-Workspace-ID: WS_ID`.

---

## 10. Config (env Hermes reads)

| Var | Meaning | Example |
|---|---|---|
| `HERMES_SERVER_HTTP` | Multica HTTP base | `http://localhost:8080` |
| `HERMES_SERVER_WS` | Multica WS base | `ws://localhost:8080` |
| `HERMES_PAT` | bot PAT (`mul_…`) | — |
| `HERMES_BOT_USER_ID` | bot's user UUID (loop guard + author) | — |
| `HERMES_WORKSPACE_ID` | workspace to serve | `21c6d282-…` |
| `HERMES_RECONNECT_MAX_S` | reconnect backoff cap | `30` |

---

## 11. Failure modes

- **WS drops** → reconnect with exponential backoff, re-send the auth frame, run §8
  catch-up. Multica has no replay; missed-while-offline messages are recovered only by the
  GET catch-up.
- **POST 401/403** → PAT invalid/expired or CSRF leaked in (don't send a cookie); re-mint.
- **POST 400 on pagination** → you sent `before` without `before_id` (or vice versa).
- **Multiple workspaces** → run one subscriber per workspace, or connect once per
  `workspace_id`; scope is per-connection.

---

## 12. Decisions left to Hermes

1. **Which threads to answer** — all, or only threads whose title/first-message opts in
   (e.g. prefix `@hermes`)? Default recommendation: answer any thread where the latest
   non-bot message @-mentions or addresses Hermes, to avoid hijacking human-to-human
   threads.
2. **Streaming vs single-shot** — this bridge posts one reply per message. If you want
   token streaming, post a placeholder then PATCH/append (needs a small Hive endpoint
   addition — out of scope here).
3. **Identity display** — the bot user's name/avatar is what humans see as the responder;
   set them on the `hermes-bot` member.

---

_Bridge target: firefly-events/multica `feat/multica-plugin-ui`. Substrate = mpu-5
(`hive.hermes_threads` / `hive.hermes_messages` + `realtime.Broadcaster`). No Hive-side
code change required to implement this spec._
