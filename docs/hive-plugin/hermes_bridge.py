#!/usr/bin/env python3
"""
Hermes ↔ HiveChat two-way bridge subscriber.

Connects to the Multica WebSocket, listens for new messages in Hive threads,
and posts replies using persistent Hermes sessions via the API server.

Each Hive thread gets its own Hermes session with full conversation memory,
proper system prompt, and tool access — so replies feel like a real conversation.

Usage:
    python3 hermes_bridge.py [--once] [--dry-run]

Environment variables (or .env):
    HERMES_SERVER_HTTP      Multica server HTTP URL (default: http://localhost:8080)
    HERMES_SERVER_WS        Multica server WS URL (default: ws://localhost:8080)
    HERMES_PAT              Personal access token for single-workspace mode
    HERMES_BOT_USER_ID      Bot user ID to filter own messages (single-workspace mode)
    HERMES_WORKSPACE_ID     Workspace ID (single-workspace mode)
    HERMES_WORKSPACE_SLUG   Workspace slug (default: plugin-hive)
    HERMES_API_URL          Hermes API server URL (default: http://127.0.0.1:8642)
    HERMES_API_SERVER_KEY   API server auth key
    HERMES_SYSTEM_PROMPT    Path to system prompt file (default: hive-system-prompt.txt)
    HERMES_RECONNECT_MAX_S  Max reconnect backoff seconds (default: 30)
    HERMES_WORKSPACES       JSON array of workspace objects for multi-workspace mode
                            Each: {"workspace_id","slug","pat","bot_user_id"}
"""

import json
import os
import signal
import sys
import tempfile
import time
import logging
import queue
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional, Union

import websocket
import requests

# ---------------------------------------------------------------------------
# Keep-alive HTTP sessions (s4: avoid per-request TCP handshake overhead)
_hermes_session = requests.Session()  # for HERMES_API_URL calls
_multica_session = requests.Session()  # for HERMES_SERVER_HTTP calls

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

def _load_env() -> dict:
    """Load config from environment, falling back to .env file."""
    cfg = {
        "HERMES_SERVER_HTTP": "http://localhost:8080",
        "HERMES_SERVER_WS": "ws://localhost:8080",
        "HERMES_PAT": "",
        "HERMES_BOT_USER_ID": "",
        "HERMES_WORKSPACE_ID": "",
        "HERMES_WORKSPACE_SLUG": "plugin-hive",
        "HERMES_API_URL": "http://127.0.0.1:8642",
        "HERMES_API_SERVER_KEY": "",
        "HERMES_SYSTEM_PROMPT": "",
        "HERMES_BRIDGE_STATUS_PATH": "",
        "HERMES_RECONNECT_MAX_S": 30,
    }
    # Load .env file next to this script
    env_path = Path(__file__).parent / ".env"
    if env_path.exists():
        for line in env_path.read_text().splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, _, v = line.partition("=")
                k = k.strip()
                v = v.strip().strip('"').strip("'")
                if k in cfg:
                    cfg[k] = v
    # Environment variables override .env
    for k in cfg:
        val = os.environ.get(k)
        if val is not None:
            cfg[k] = val
    cfg["HERMES_RECONNECT_MAX_S"] = int(cfg["HERMES_RECONNECT_MAX_S"])

    # s9: multi-workspace support
    ws_json = os.environ.get("HERMES_WORKSPACES", "").strip()
    if ws_json:
        try:
            workspaces = json.loads(ws_json)
            if not isinstance(workspaces, list):
                raise ValueError("HERMES_WORKSPACES must be a JSON array")
            if not workspaces:
                raise ValueError("HERMES_WORKSPACES must contain at least one workspace")
            for ws in workspaces:
                for field in ("workspace_id", "slug", "pat", "bot_user_id"):
                    if not ws.get(field):
                        raise ValueError(f"workspace entry missing field: {field}")
        except Exception as exc:
            print(f"ERROR: HERMES_WORKSPACES invalid: {exc}", file=sys.stderr)
            sys.exit(1)
    else:
        workspaces = [{
            "workspace_id": cfg["HERMES_WORKSPACE_ID"],
            "slug": cfg["HERMES_WORKSPACE_SLUG"],
            "pat": cfg["HERMES_PAT"],
            "bot_user_id": cfg["HERMES_BOT_USER_ID"],
        }]
    cfg["WORKSPACES"] = workspaces

    return cfg

CONFIG = _load_env()

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%dT%H:%M:%S",
)
log = logging.getLogger("hermes-bridge")

# ---------------------------------------------------------------------------
# Bridge status sidecar
# ---------------------------------------------------------------------------

_status_lock = threading.Lock()
_status_stop = threading.Event()
_ws_stop = threading.Event()  # s9: stop signal for per-workspace WS threads
_subscriber_threads: list = []  # s9: per-workspace WS thread refs
_bridge_status: dict = {}  # s9: keyed by workspace_id


def _status_path(workspace_id: str) -> Path:
    # Per-workspace file always wins when workspace_id given
    if workspace_id:
        return Path(tempfile.gettempdir()) / f"multica-hermes-bridge-status-{workspace_id}.json"
    # Back-compat: env override only for legacy (no workspace_id)
    raw = (CONFIG.get("HERMES_BRIDGE_STATUS_PATH") or "").strip()
    if raw:
        return Path(raw)
    return Path(tempfile.gettempdir()) / "multica-hermes-bridge-status.json"


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _write_bridge_status(workspace_id: str) -> None:
    if workspace_id not in _bridge_status:
        return
    path = _status_path(workspace_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    payload = json.dumps(_bridge_status[workspace_id], indent=2, sort_keys=True)
    tmp.write_text(payload)
    tmp.replace(path)


def _update_bridge_status(workspace_id: str, *, connected: Optional[bool] = None, last_error: Optional[str] = None, mark_connect: bool = False, mark_event: bool = False, mark_heartbeat: bool = False) -> None:
    with _status_lock:
        if workspace_id not in _bridge_status:
            _bridge_status[workspace_id] = {
                "workspace_id": workspace_id,
                "updated_at": "",
                "bridge": {
                    "connected": False,
                    "updated_at": "",
                    "last_heartbeat_at": "",
                    "last_event_at": "",
                    "last_connect_at": "",
                    "last_error": "",
                },
                "threads": {},
            }
        ts = _now()
        _bridge_status[workspace_id]["workspace_id"] = workspace_id
        _bridge_status[workspace_id]["updated_at"] = ts
        bridge = _bridge_status[workspace_id]["bridge"]
        if connected is not None:
            bridge["connected"] = connected
        bridge["updated_at"] = ts
        if mark_connect:
            bridge["last_connect_at"] = ts
        if mark_event:
            bridge["last_event_at"] = ts
        if mark_heartbeat:
            bridge["last_heartbeat_at"] = ts
        if last_error is not None:
            bridge["last_error"] = last_error
        _write_bridge_status(workspace_id)


def _set_thread_status(workspace_id: str, thread_id: str, state: str, *, message_id: str = "", error: str = "") -> None:
    if not thread_id:
        return
    with _status_lock:
        if workspace_id not in _bridge_status:
            _update_bridge_status(workspace_id)
        ts = _now()
        _bridge_status[workspace_id]["workspace_id"] = workspace_id
        _bridge_status[workspace_id]["updated_at"] = ts
        threads = _bridge_status[workspace_id].setdefault("threads", {})
        entry = threads.get(thread_id, {})
        entry.update({
            "state": state,
            "updated_at": ts,
        })
        if message_id:
            entry["message_id"] = message_id
        if state == "running" and not entry.get("started_at"):
            entry["started_at"] = ts
        if error:
            entry["error"] = error
        else:
            entry.pop("error", None)
        if state == "idle":
            entry.pop("started_at", None)
        threads[thread_id] = entry
        _write_bridge_status(workspace_id)


def _heartbeat_loop() -> None:
    while not _status_stop.wait(3):
        with _status_lock:
            ws_ids = [
                ws_id for ws_id, data in _bridge_status.items()
                if data.get("bridge", {}).get("connected")
            ]
        for ws_id in ws_ids:
            _update_bridge_status(ws_id, mark_heartbeat=True)

# ---------------------------------------------------------------------------
# State — idempotency ring
# ---------------------------------------------------------------------------

_seen_message_ids: set = set()
_seen_lock = threading.Lock()     # s4: atomic check-add-evict in seen()
_session_lock = threading.Lock()  # s4: guard _session_cache + _prev_input_tokens
_msg_queue: "queue.Queue" = queue.Queue()  # s9: items are (ws_cfg, msg_dict) or None
_workers: list = []  # s4: bridge-worker thread refs for shutdown

def seen(msg_id: str) -> bool:
    """Return True if we've already handled this message ID (s4: lock for worker concurrency)."""
    with _seen_lock:
        if msg_id in _seen_message_ids:
            return True
        _seen_message_ids.add(msg_id)
        # Keep ring bounded
        if len(_seen_message_ids) > 1000:
            for _id in list(_seen_message_ids)[:200]:
                _seen_message_ids.discard(_id)
        return False

# ---------------------------------------------------------------------------
# Session management — (workspace_id, thread_id) → Hermes session_id
# ---------------------------------------------------------------------------

# s9: cache stores (workspace_id, thread_id) tuple → {id, model, title} dicts
# s7: back-compat: bare str entries tolerated on load
_session_cache: dict = {}
# s7: per-session cumulative input-token watermark for turn-delta computation
# keyed by session_id (globally unique) — UNCHANGED
_prev_input_tokens: dict = {}

# s7: known context windows keyed by Hermes session model string. Values are APPROXIMATE;
# unknown models degrade to no-meter (None — ctx_window will be None for unlisted models).
# 'hermes-agent' is the profile name this server returns for bridge-created sessions
# (discovered via POST /api/sessions probe 2026-06-26). Will be updated if model string changes.
MODEL_CONTEXT_WINDOWS = {
    "hermes-agent": 200_000,
    "claude-opus-4-5": 200_000,
    "claude-sonnet-4-5": 200_000,
    "claude-haiku-3-5": 200_000,
    "gpt-5": 128_000,                 # approximate
    "gpt-4o": 128_000,
}
_session_store_path = Path(__file__).parent / "session-store.json"

def _load_session_cache():
    """Load (workspace_id, thread_id) → {id, model} mapping from disk.

    s9 migration: old flat {thread_id: ...} format is migrated to tuple keys
    using WORKSPACES[0]["workspace_id"] as the workspace_id for legacy entries.
    New nested format: {workspace_id: {thread_id: {id, model}}} → flattened to tuples.

    Robust to hand-edited / partially-corrupt stores: format is detected by
    inspecting EVERY top-level value (not just the first), and malformed entries
    are skipped individually rather than wiping or persisting the whole cache.
    """
    global _session_cache
    if not _session_store_path.exists():
        return
    try:
        raw = json.loads(_session_store_path.read_text())
    except Exception as exc:
        log.warning("Failed to read/parse session cache: %s", exc)
        _session_cache = {}
        return
    if not isinstance(raw, dict) or not raw:
        _session_cache = {}
        return

    def _is_entry(v):
        # A leaf session entry: a bare session-id string, or a dict carrying "id".
        return isinstance(v, str) or (isinstance(v, dict) and "id" in v)

    def _is_threadmap(v):
        # A {thread_id: entry} map: a non-empty dict that is NOT itself a leaf entry
        # (the `"id" not in v` guard distinguishes a flat {"id":..,"model":..} entry
        # dict from a workspace threads map) whose values are all leaf entries.
        return (
            isinstance(v, dict) and v and "id" not in v
            and all(_is_entry(e) for e in v.values())
        )

    # NEW nested iff ANY top-level value is a clear threads map. This tolerates
    # empty or garbage sibling workspace values (which carry no threads map) instead
    # of letting one outlier flip the whole file to a flat misclassification; the
    # migration loops below skip non-conforming entries individually.
    is_nested = any(_is_threadmap(v) for v in raw.values())

    migrated = {}
    if is_nested:
        for ws_id, threads in raw.items():
            if not isinstance(threads, dict):
                continue
            for tid, entry in threads.items():
                if isinstance(entry, str):
                    migrated[(ws_id, tid)] = {"id": entry, "model": None}
                elif isinstance(entry, dict) and entry.get("id"):
                    migrated[(ws_id, tid)] = entry
    else:
        legacy_ws_id = CONFIG["WORKSPACES"][0]["workspace_id"] if CONFIG["WORKSPACES"] else ""
        for k, v in raw.items():
            if isinstance(v, str):
                migrated[(legacy_ws_id, k)] = {"id": v, "model": None}
            elif isinstance(v, dict) and v.get("id"):
                migrated[(legacy_ws_id, k)] = v
            # else: skip non-str/non-id-bearing garbage rather than persist it
    _session_cache = migrated
    log.info("Loaded %d session mappings from %s", len(_session_cache), _session_store_path)

def _save_session_cache():
    """Persist (workspace_id, thread_id) → session mapping to disk as nested dict."""
    try:
        # Convert tuple keys back to nested {workspace_id: {thread_id: {...}}}
        nested: dict = {}
        for (ws_id, tid), entry in _session_cache.items():
            if ws_id not in nested:
                nested[ws_id] = {}
            nested[ws_id][tid] = entry
        _session_store_path.write_text(json.dumps(nested, indent=2))
    except Exception as exc:
        log.warning("Failed to save session cache: %s", exc)

def _api_headers() -> dict:
    """Headers for Hermes API server requests."""
    return {
        "Authorization": f"Bearer {CONFIG['HERMES_API_SERVER_KEY']}",
        "Content-Type": "application/json",
    }

def _load_system_prompt() -> str:
    """Load system prompt from file or env, falling back to built-in default."""
    prompt_path_str = CONFIG.get("HERMES_SYSTEM_PROMPT", "").strip()
    if prompt_path_str:
        p = Path(prompt_path_str)
        if not p.is_absolute():
            p = Path(__file__).parent / p
        if p.exists():
            return p.read_text().strip()
        log.warning("HERMES_SYSTEM_PROMPT path not found: %s — using default", p)
    default_path = Path(__file__).parent / "hive-system-prompt.txt"
    if default_path.exists():
        return default_path.read_text().strip()
    return (
        "You are Hermes, an AI assistant in a Multica HiveChat thread. "
        "Be direct, concise, and helpful. Answer the actual question being asked. "
        "You have full tool access — use web_search for current info."
    )


# FIX 2a: TTL cache for _session_title_for — collapses up-to-3x per-message GETs
# to ~1 GET per 30s per thread. Lock guards the dict briefly; GET runs OUTSIDE lock.
_title_cache: dict = {}  # (workspace_id, thread_id) → (title, monotonic_ts)
_title_lock = threading.Lock()
_TITLE_TTL_S = 30.0


def _session_title_for(thread_id: str, ws_cfg: dict) -> str:
    """Return deterministic [multica]-prefixed Hermes session title for a thread.

    Fetches the Multica thread title via GET /api/plugins/hive/hermes-threads.
    Falls back to f"[multica] thread {thread_id[:8]}" on any failure or empty title.
    Never raises — callers depend on this being safe.

    FIX 2a: TTL cache avoids repeated GETs for the same thread within 30s.
    s9: workspace-namespaced via ws_cfg.
    """
    cache_key = (ws_cfg["workspace_id"], thread_id)
    now = time.monotonic()
    with _title_lock:
        entry = _title_cache.get(cache_key)
        if entry is not None and (now - entry[1]) < _TITLE_TTL_S:
            return entry[0]
    # Stale or absent — fetch outside the lock
    fallback = f"[multica] thread {thread_id[:8]}"
    result = fallback
    try:
        threads = http_get(
            "/api/plugins/hive/hermes-threads",
            ws_cfg,
            params={"workspace_id": ws_cfg["workspace_id"]},
        )
        if isinstance(threads, list):
            for t in threads:
                if t.get("ID") == thread_id:
                    title = (t.get("Title") or "").strip()
                    result = f"[multica] {title}" if title else fallback
                    break
    except Exception as exc:
        log.warning("_session_title_for(%s): %s — using fallback", thread_id[:8], exc)
    # Re-lock to store result (even fallback — avoids thundering herd on failures)
    with _title_lock:
        _title_cache[cache_key] = (result, time.monotonic())
    return result


def _find_existing_session(thread_id: str, ws_cfg: dict) -> Optional[str]:
    """LEGACY best-effort fallback: scan Hermes session list by title.

    The authoritative thread_id→session_id identity is the durable _session_cache /
    session-store.json (keyed by (workspace_id, thread_id), loaded at startup and
    consulted first in get_or_create_session). This scan is called ONLY when the
    durable map has no entry — i.e., on first run after a fresh install, or after
    a manual cache wipe.

    On a scan miss a new session is created; any orphaned pre-cache session is harmless —
    title is NOT a stable unique key (user may rename threads). This function is
    best-effort only.
    s9: workspace-namespaced via ws_cfg.
    """
    try:
        resp = _hermes_session.get(
            f"{CONFIG['HERMES_API_URL']}/api/sessions",
            headers=_api_headers(),
            params={"limit": 200},
            timeout=10,
        )
        if resp.status_code == 200:
            sessions = resp.json().get("sessions", resp.json().get("data", {}).get("sessions", []))
            current_title = _session_title_for(thread_id, ws_cfg)
            legacy_title = f"hive:{thread_id}"
            for s in sessions:
                t = s.get("title", "")
                if t == current_title or t == legacy_title:
                    return s["id"]
    except Exception as exc:
        log.warning("Failed to list sessions (legacy scan): %s", exc)
    return None

def get_or_create_session(thread_id: str, ws_cfg: dict) -> str:
    """Get existing Hermes session ID for a thread, or create a new one.

    s4 locking: cache hit checked under _session_lock (brief, no I/O).
    Network calls happen OUTSIDE the lock to avoid serializing turns behind HTTP.
    s9: workspace-namespaced via ws_cfg.
    """
    cache_key = (ws_cfg["workspace_id"], thread_id)

    # Fast path: cache hit (brief lock, no I/O)
    with _session_lock:
        if cache_key in _session_cache:
            return _session_cache[cache_key]["id"]

    # Check disk / API for existing session (outside lock — network I/O)
    existing = _find_existing_session(thread_id, ws_cfg)
    if existing:
        with _session_lock:
            _session_cache[cache_key] = {"id": existing, "model": None}
            _save_session_cache()
        log.info("Found existing session %s for thread %s", existing[:8], thread_id[:8])
        return existing

    # Create new session (outside lock — network I/O; rare: once per thread)
    system_prompt = _load_system_prompt()
    try:
        resp = _hermes_session.post(
            f"{CONFIG['HERMES_API_URL']}/api/sessions",
            headers=_api_headers(),
            json={
                "title": _session_title_for(thread_id, ws_cfg),
                "system_prompt": system_prompt,
            },
            timeout=15,
        )
        if resp.status_code == 201:
            session_data = resp.json()["session"]
            session_id = session_data["id"]
            session_model = session_data.get("model")  # s7: capture model at create time
            with _session_lock:
                _session_cache[cache_key] = {"id": session_id, "model": session_model}
                _save_session_cache()
            log.info("Created session %s (model=%s) for thread %s", session_id[:8], session_model, thread_id[:8])
            return session_id
        else:
            log.error("Failed to create session: %s %s", resp.status_code, resp.text[:200])
    except Exception as exc:
        log.error("Session creation failed: %s", exc)

    raise RuntimeError(f"Cannot get or create session for thread {thread_id}")

def _session_model(workspace_id: str, thread_id: str):
    '''Return the cached model string for (workspace_id, thread_id), or None if unknown (s4: brief lock).'''
    with _session_lock:
        entry = _session_cache.get((workspace_id, thread_id))
        if isinstance(entry, dict):
            return entry.get("model")
        return None

# ---------------------------------------------------------------------------
# HTTP helpers (Multica)
# ---------------------------------------------------------------------------

def _multica_auth_headers(ws_cfg: dict) -> dict:
    return {
        "Authorization": f"Bearer {ws_cfg['pat']}",
        "X-Workspace-Slug": ws_cfg["slug"],
    }

def http_get(path: str, ws_cfg: dict, params: Optional[dict] = None) -> Union[dict, list]:
    url = CONFIG["HERMES_SERVER_HTTP"] + path
    resp = _multica_session.get(url, headers=_multica_auth_headers(ws_cfg), params=params, timeout=15)
    resp.raise_for_status()
    return resp.json()

def http_post(path: str, ws_cfg: dict, body: dict) -> dict:
    url = CONFIG["HERMES_SERVER_HTTP"] + path
    headers = _multica_auth_headers(ws_cfg)
    headers["Content-Type"] = "application/json"
    resp = _multica_session.post(url, headers=headers, json=body, timeout=15)
    resp.raise_for_status()
    return resp.json()

# ---------------------------------------------------------------------------
# Hermes brain — persistent session via API server
# ---------------------------------------------------------------------------

def _post_message(
    ws_cfg: dict,
    thread_id: str,
    body: str,
    role: str = "assistant",
    *,
    tokens_used=None,
    context_window=None,
    model=None,
) -> dict:
    """Post a single message to Multica hermes-messages endpoint with role.

    token metadata (s7): tokens_used = turn delta, context_window = model limit,
    model = model string from session-create cache.
    s9: workspace-namespaced via ws_cfg.
    """
    payload = {
        "thread_id": thread_id,
        "workspace_id": ws_cfg["workspace_id"],
        "body": body,
        "role": role,
    }
    if tokens_used is not None:
        payload["tokens_used"] = tokens_used
    if context_window is not None:
        payload["context_window"] = context_window
    if model is not None:
        payload["model"] = model
    return http_post("/api/plugins/hive/hermes-messages", ws_cfg, payload)


def hermes_respond_sse(ws_cfg: dict, thread_id: str, incoming_body: str, session_id: str, model=None, t_recv: float = 0.0) -> None:
    """
    Stream a Hermes turn via SSE (POST .../chat/stream), posting discrete messages
    to Multica as events arrive.

    Decision: path A — post-on-complete, NO PATCH, NO live token streaming.
    - tool/thinking lines post live as compact single messages.
    - assistant prose posts as ONE message when run.completed fires.
    - s7: tokens_used (turn delta), context_window, and model populated from
          run.completed usage payload and the session-model cache.
    s9: workspace-namespaced via ws_cfg.
    """
    url = f"{CONFIG['HERMES_API_URL']}/api/sessions/{session_id}/chat/stream"
    log.info("TIMING sse_open session=%s thread=%s elapsed=%.3fs",
             session_id[:8], thread_id[:8], time.perf_counter() - t_recv)
    try:
        resp = requests.post(
            url,
            headers=_api_headers(),
            json={"message": incoming_body},
            stream=True,
            timeout=(10, 300),  # 10s connect, 300s read — bounded but generous
        )
        resp.raise_for_status()
    except Exception as e:
        log.error("SSE stream open failed for session %s: %s", session_id[:8], e)
        raise

    # Per-run state
    text_buffer = ""          # accumulate assistant.delta fragments
    final_content = None      # set from assistant.completed (authoritative)
    thinking_posted = False   # guard: post thinking bubble only once per turn
    run_completed = False     # FIX1: track whether run.completed fired
    _first_visible = False    # s4: guard for TIMING first_visible log

    def _mark_first_visible():
        nonlocal _first_visible
        if not _first_visible:
            _first_visible = True
            log.info("TIMING first_visible session=%s thread=%s elapsed=%.3fs",
                     session_id[:8], thread_id[:8], time.perf_counter() - t_recv)

    def _dispatch_event(ev: str, payload: dict) -> None:
        """Handle one SSE event.

        Mutates text_buffer, final_content, thinking_posted via nonlocal.
        """
        nonlocal text_buffer, final_content, thinking_posted, run_completed, _first_visible

        if ev == "tool.progress":
            tool_name = payload.get("tool_name", "")
            if tool_name == "_thinking":
                if not thinking_posted:
                    thinking_posted = True
                    try:
                        _post_message(ws_cfg, thread_id, "💭 thinking…", role="reasoning")
                        _mark_first_visible()  # s4: thinking was first visible
                        log.debug("SSE posted thinking message")
                    except Exception as e:
                        log.warning("Failed to post thinking message: %s", e)
            # Non-_thinking tool.progress deltas: ignore (tool.started/completed carry the line)

        elif ev == "tool.started":
            tool_name = payload.get("tool_name", "")
            preview = payload.get("preview", "") or ""
            compact = f"🔧 {tool_name}"
            if preview:
                compact += f" — {preview[:60]}"
            try:
                _post_message(ws_cfg, thread_id, compact, role="tool")
                _mark_first_visible()  # s4: tool was first visible
                log.debug("SSE posted tool.started: %s", tool_name)
            except Exception as e:
                log.warning("Failed to post tool message: %s", e)

        elif ev == "tool.completed":
            pass  # No PATCH route; skip retro-update

        elif ev == "tool.failed":
            tool_name = payload.get("tool_name", "")
            try:
                _post_message(ws_cfg, thread_id, f"✗ {tool_name} failed", role="tool")
                log.debug("SSE posted tool.failed: %s", tool_name)
            except Exception as e:
                log.warning("Failed to post tool.failed message: %s", e)

        elif ev == "assistant.delta":
            text_buffer += payload.get("content", "")

        elif ev == "assistant.completed":
            # authoritative content from completed event
            final_content = payload.get("content") or text_buffer

        elif ev == "run.completed":
            run_completed = True
            # s7: extract usage from run.completed payload
            usage = payload.get("usage") or {}
            total_input = usage.get("input_tokens") or 0
            total_output = usage.get("output_tokens") or 0
            # Turn delta: subtract previous watermark to get just this turn's input tokens
            prev = _prev_input_tokens.get(session_id, 0)
            turn_input_delta = max(0, total_input - prev) if total_input else 0
            cur_input = total_input or prev
            turn_tokens = (turn_input_delta + total_output) if (turn_input_delta or total_output) else None
            with _session_lock:
                if cur_input:
                    _prev_input_tokens[session_id] = cur_input
            # s7: context window from model→window map; model from caller (session-create cache)
            ctx_window = MODEL_CONTEXT_WINDOWS.get(model) if model else None
            if msg_content := (final_content or text_buffer):
                try:
                    result = _post_message(
                        ws_cfg, thread_id, msg_content, role="assistant",
                        tokens_used=turn_tokens,
                        context_window=ctx_window,
                        model=model,
                    )
                    _mark_first_visible()  # s4: assistant message was first visible (if not already)
                    log.info("TIMING posted session=%s thread=%s elapsed=%.3fs",
                             session_id[:8], thread_id[:8], time.perf_counter() - t_recv)
                    log.debug("SSE run.completed: posted assistant message id=%s tokens=%s",
                              result.get("id", "?")[:8] if isinstance(result, dict) else "?",
                              turn_tokens)
                except Exception as e:
                    log.error("Failed to post assistant message: %s", e)
                    raise
            else:
                log.warning("run.completed with no content to post")

        elif ev == "error":
            err_msg = payload.get("message", "unknown error")
            log.error("SSE error event: %s", err_msg)
            try:
                _post_message(ws_cfg, thread_id, f"⚠ Error: {err_msg}", role="error")
            except Exception as e:
                log.warning("Failed to post error message: %s", e)
            raise RuntimeError(f"SSE error: {err_msg}")

    # --- SSE parse loop ---
    cur_event_name = ""
    cur_data_lines: list = []

    for raw_line in resp.iter_lines(decode_unicode=True):
        if raw_line is None:
            continue
        if raw_line == "":
            # Blank line = end of event block
            if cur_event_name and cur_data_lines:
                data_str = "\n".join(cur_data_lines)
                try:
                    ev_payload = json.loads(data_str)
                except json.JSONDecodeError:
                    ev_payload = {"raw": data_str}
                try:
                    _dispatch_event(cur_event_name, ev_payload)
                except Exception as exc:
                    log.error("Event dispatch failed (%s): %s", cur_event_name, exc)
                    raise
            cur_event_name = ""
            cur_data_lines = []
            continue

        if raw_line.startswith("event:"):
            cur_event_name = raw_line[6:].strip()
        elif raw_line.startswith("data:"):
            cur_data_lines.append(raw_line[5:].strip())

    # FIX1: post-loop guard — if stream ended without run.completed, rescue buffered text
    if not run_completed:
        rescue_content = final_content or text_buffer
        if rescue_content:
            log.warning("SSE stream ended without run.completed — posting buffered assistant text")
            try:
                _post_message(
                    ws_cfg, thread_id, rescue_content, role="assistant",
                    model=model,  # s7: pass model if available; tokens unknown in guard path
                )
                _set_thread_status(ws_cfg["workspace_id"], thread_id, "posting")
            except Exception as e:
                log.error("Failed to post rescued assistant content: %s", e)
                raise
        else:
            log.warning("SSE stream ended without run.completed and no buffered content")


# ---------------------------------------------------------------------------
# Message handler
# ---------------------------------------------------------------------------

def handle_message(msg: dict, ws_cfg: dict):
    """Process a single hive:message:created event."""
    t_recv = time.perf_counter()  # s4: timing — message received from WS queue
    msg_id = msg.get("ID", "")
    thread_id = msg.get("ThreadID", "")
    author_id = msg.get("AuthorID", "")
    body = msg.get("Body", "")

    # Loop guard: skip own messages (critical now that the bot posts multiple
    # messages per turn — tool/reasoning lines + final assistant message)
    if author_id == ws_cfg["bot_user_id"]:
        log.info("Skipping own message %s (loop guard)", msg_id[:8])
        return

    # Idempotency — ring buffer prevents duplicate processing
    if seen(msg_id):
        log.debug("Already handled %s", msg_id[:8])
        return

    log.info("New message in %s from %s: %s", thread_id[:8], author_id[:8], body[:80])
    _update_bridge_status(ws_cfg["workspace_id"], mark_event=True, last_error="")
    _set_thread_status(ws_cfg["workspace_id"], thread_id, "running", message_id=msg_id)

    session_id = get_or_create_session(thread_id, ws_cfg)

    # Handle 404 (session expired) by recreating once before giving up
    try:
        hermes_respond_sse(ws_cfg, thread_id, body, session_id, model=_session_model(ws_cfg["workspace_id"], thread_id), t_recv=t_recv)
    except requests.HTTPError as e:
        if e.response is not None and e.response.status_code == 404:
            log.warning("Session %s not found, recreating for thread %s", session_id[:8], thread_id[:8])
            cache_key = (ws_cfg["workspace_id"], thread_id)
            with _session_lock:
                _session_cache.pop(cache_key, None)
                _save_session_cache()
            session_id = get_or_create_session(thread_id, ws_cfg)
            try:
                hermes_respond_sse(ws_cfg, thread_id, body, session_id, model=_session_model(ws_cfg["workspace_id"], thread_id), t_recv=t_recv)
            except Exception as e2:
                log.error("SSE retry after session recreate failed: %s", e2)
                _set_thread_status(ws_cfg["workspace_id"], thread_id, "error", message_id=msg_id, error=str(e2))
                _update_bridge_status(ws_cfg["workspace_id"], last_error=str(e2))
                return
        else:
            log.error("SSE stream HTTP error: %s", e)
            _set_thread_status(ws_cfg["workspace_id"], thread_id, "error", message_id=msg_id, error=str(e))
            _update_bridge_status(ws_cfg["workspace_id"], last_error=str(e))
            return
    except Exception as e:
        log.error("SSE stream failed: %s", e)
        _set_thread_status(ws_cfg["workspace_id"], thread_id, "error", message_id=msg_id, error=str(e))
        _update_bridge_status(ws_cfg["workspace_id"], last_error=str(e))
        return

    # FIX 2b: best-effort rename-sync AFTER the SSE turn (off critical path).
    # Runs once per message in the worker; does NOT delay time-to-first-visible.
    # FIX 3: recheck cached_title under _session_lock immediately before PATCH —
    # another worker may have already synced while this turn was running.
    try:
        want = _session_title_for(thread_id, ws_cfg)
        cache_key = (ws_cfg["workspace_id"], thread_id)
        with _session_lock:
            cached_title = _session_cache.get(cache_key, {}).get("title")
        if want != cached_title:
            # FIX 3: recheck under lock before issuing the PATCH — skip if already synced
            with _session_lock:
                rechecked = _session_cache.get(cache_key, {}).get("title")
            if want != rechecked:
                # PATCH runs OUTSIDE lock — no lock held across network I/O
                _hermes_session.patch(
                    f"{CONFIG['HERMES_API_URL']}/api/sessions/{session_id}/title",
                    headers=_api_headers(),
                    json={"title": want},
                    timeout=10,
                )
                with _session_lock:
                    if cache_key in _session_cache:
                        _session_cache[cache_key]["title"] = want
                        _save_session_cache()
                log.debug("Renamed session %s to %r", session_id[:8], want)
    except Exception as exc:
        log.warning("Title sync for thread %s failed (best-effort): %s", thread_id[:8], exc)

    _set_thread_status(ws_cfg["workspace_id"], thread_id, "idle", message_id=msg_id)

# ---------------------------------------------------------------------------
# Worker queue (s4: process messages off the WS-recv thread)
# ---------------------------------------------------------------------------

def _worker() -> None:
    """Drain _msg_queue; block on None poison-pill to exit."""
    while True:
        item = _msg_queue.get()
        if item is None:
            _msg_queue.task_done()
            break
        try:
            ws_cfg, msg = item
            handle_message(msg, ws_cfg)
        except Exception as e:
            log.error("Worker error: %s", e)
        finally:
            _msg_queue.task_done()

# ---------------------------------------------------------------------------
# Per-workspace WebSocket subscriber (s9)
# ---------------------------------------------------------------------------

def _run_ws_for_workspace(ws_cfg: dict) -> None:
    """Subscribe to Multica WebSocket for one workspace with reconnect/backoff."""
    workspace_id = ws_cfg["workspace_id"]
    ws_url = f"{CONFIG['HERMES_SERVER_WS']}/ws?workspace_id={workspace_id}"
    backoff = 1

    ws = None
    while not _ws_stop.is_set():
        log.info("[%s] Connecting to %s", ws_cfg["slug"], ws_url)
        try:
            ws = websocket.create_connection(ws_url, timeout=30)
            log.info("[%s] Connected. Sending auth...", ws_cfg["slug"])

            # Send auth frame
            auth_frame = json.dumps({
                "type": "auth",
                "payload": {"token": ws_cfg["pat"]},
            })
            ws.send(auth_frame)

            # Wait for auth_ack
            ws.settimeout(10)
            raw = ws.recv()
            ack = json.loads(raw)
            if ack.get("type") != "auth_ack":
                log.error("[%s] Auth failed: %s", ws_cfg["slug"], ack)
                _update_bridge_status(workspace_id, connected=False, last_error=f"auth failed: {ack}")
                ws.close()
                if _ws_stop.wait(backoff):
                    break
                backoff = min(backoff * 2, CONFIG["HERMES_RECONNECT_MAX_S"])
                continue

            log.info("[%s] Auth OK. Listening for events...", ws_cfg["slug"])
            _update_bridge_status(workspace_id, connected=True, last_error="", mark_connect=True, mark_heartbeat=True)
            backoff = 1  # reset on successful connect

            # Event loop
            ws.settimeout(None)  # block forever
            while not _ws_stop.is_set():
                raw = ws.recv()
                if not raw:
                    continue
                try:
                    event = json.loads(raw)
                except json.JSONDecodeError:
                    log.warning("[%s] Non-JSON WS frame: %r", ws_cfg["slug"], raw[:100])
                    continue

                event_type = event.get("type", "")
                if event_type != "hive:message:created":
                    continue

                payload = event.get("payload", {})
                message = payload.get("message", {})
                if not message:
                    continue

                _msg_queue.put((ws_cfg, message))  # s9: enqueue (ws_cfg, msg) tuple

        except websocket.WebSocketTimeoutException:
            log.warning("[%s] WebSocket timeout — reconnecting", ws_cfg["slug"])
            _update_bridge_status(workspace_id, connected=False, last_error="timeout")
        except websocket.WebSocketConnectionClosedException:
            log.warning("[%s] WebSocket closed — reconnecting", ws_cfg["slug"])
            _update_bridge_status(workspace_id, connected=False, last_error="connection closed")
        except Exception as e:
            log.error("[%s] WebSocket error: %s", ws_cfg["slug"], e)
            _update_bridge_status(workspace_id, connected=False, last_error=str(e))
        finally:
            if ws:
                try:
                    ws.close()
                except Exception:
                    pass
                ws = None

        if _ws_stop.is_set():
            break
        if _ws_stop.wait(backoff):
            break
        backoff = min(backoff * 2, CONFIG["HERMES_RECONNECT_MAX_S"])

    log.info("[%s] WS subscriber exiting", ws_cfg["slug"])

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    # Validate config
    # HERMES_API_SERVER_KEY is always required; workspace fields validated in _load_env
    missing = [k for k in ["HERMES_API_SERVER_KEY"]
               if not CONFIG.get(k)]
    # Also validate single-workspace mode fields if not using HERMES_WORKSPACES
    if not os.environ.get("HERMES_WORKSPACES", "").strip():
        missing += [k for k in ["HERMES_PAT", "HERMES_BOT_USER_ID", "HERMES_WORKSPACE_ID"]
                    if not CONFIG.get(k)]
    if missing:
        log.error("Missing required config: %s", ", ".join(missing))
        log.error("Set them as environment variables or in .env next to this script.")
        sys.exit(1)

    log.info("Hermes bridge starting — %d workspace(s)", len(CONFIG["WORKSPACES"]))
    for ws in CONFIG["WORKSPACES"]:
        log.info("  Workspace: %s (%s)", ws["slug"], ws["workspace_id"][:8])

    # Load session cache
    _load_session_cache()

    # Initialize bridge status for all workspaces
    for ws_cfg in CONFIG["WORKSPACES"]:
        _update_bridge_status(ws_cfg["workspace_id"], connected=False, last_error="")

    heartbeat = threading.Thread(target=_heartbeat_loop, name="bridge-heartbeat", daemon=True)
    heartbeat.start()

    # s4: spawn 2 worker threads to process messages concurrently
    global _workers
    for i in range(2):
        w = threading.Thread(target=_worker, name=f"bridge-worker-{i}", daemon=True)
        w.start()
        _workers.append(w)
    log.info("Started %d bridge worker threads", len(_workers))

    # s9: spawn one WS subscriber thread per workspace
    global _subscriber_threads
    for ws_cfg in CONFIG["WORKSPACES"]:
        t = threading.Thread(
            target=_run_ws_for_workspace,
            args=(ws_cfg,),
            name=f"bridge-ws-{ws_cfg['slug']}",
            daemon=True,
        )
        t.start()
        _subscriber_threads.append(t)
    log.info("Started %d WS subscriber threads", len(_subscriber_threads))

    # Handle graceful shutdown
    def _shutdown(signum, frame):
        log.info("Shutting down...")
        _status_stop.set()
        _ws_stop.set()
        # s4: poison-pill workers so they exit cleanly
        for _ in _workers:
            _msg_queue.put(None)
        # Update all workspace statuses to disconnected
        for ws_cfg in CONFIG["WORKSPACES"]:
            try:
                _update_bridge_status(ws_cfg["workspace_id"], connected=False)
            except Exception:
                pass
        # Join subscriber threads with timeout
        for t in _subscriber_threads:
            t.join(timeout=5)
        with _session_lock:
            _save_session_cache()
        sys.exit(0)
    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    # Block main thread — subscriber threads do the real work
    for t in _subscriber_threads:
        t.join()

if __name__ == "__main__":
    main()
