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
    HERMES_SERVER_HTTP      Multica HTTP base URL (default: http://localhost:8080)
    HERMES_SERVER_WS        Multica WS base URL   (default: ws://localhost:8080)
    HERMES_PAT              Bot PAT (mul_…)
    HERMES_BOT_USER_ID      Bot user UUID (for loop guard)
    HERMES_WORKSPACE_ID     Workspace UUID to serve
    HERMES_WORKSPACE_SLUG   Workspace slug (default: plugin-hive)
    HERMES_API_URL          Hermes API server URL (default: http://127.0.0.1:8642)
    HERMES_API_SERVER_KEY   API server auth key
    HERMES_SYSTEM_PROMPT    Path to system prompt file (default: hive-system-prompt.txt)
    HERMES_RECONNECT_MAX_S  Max reconnect backoff seconds (default: 30)
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
_bridge_status = {
    "workspace_id": "",
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

def _status_path() -> Path:
    raw = (CONFIG.get("HERMES_BRIDGE_STATUS_PATH") or "").strip()
    if raw:
        return Path(raw)
    return Path(tempfile.gettempdir()) / "multica-hermes-bridge-status.json"

def _now() -> str:
    return datetime.now(timezone.utc).isoformat()

def _write_bridge_status() -> None:
    path = _status_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    payload = json.dumps(_bridge_status, indent=2, sort_keys=True)
    tmp.write_text(payload)
    tmp.replace(path)

def _update_bridge_status(*, connected: Optional[bool] = None, last_error: Optional[str] = None, mark_connect: bool = False, mark_event: bool = False, mark_heartbeat: bool = False) -> None:
    with _status_lock:
        ts = _now()
        _bridge_status["workspace_id"] = CONFIG.get("HERMES_WORKSPACE_ID", "")
        _bridge_status["updated_at"] = ts
        bridge = _bridge_status["bridge"]
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
        _write_bridge_status()

def _set_thread_status(thread_id: str, state: str, *, message_id: str = "", error: str = "") -> None:
    if not thread_id:
        return
    with _status_lock:
        ts = _now()
        _bridge_status["workspace_id"] = CONFIG.get("HERMES_WORKSPACE_ID", "")
        _bridge_status["updated_at"] = ts
        threads = _bridge_status.setdefault("threads", {})
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
        _write_bridge_status()

def _heartbeat_loop() -> None:
    while not _status_stop.wait(3):
        with _status_lock:
            connected = bool(_bridge_status.get("bridge", {}).get("connected"))
        if connected:
            _update_bridge_status(mark_heartbeat=True)

# ---------------------------------------------------------------------------
# State — idempotency ring
# ---------------------------------------------------------------------------

_seen_message_ids: set[str] = set()
_seen_lock = threading.Lock()     # s4: atomic check-add-evict in seen()
_session_lock = threading.Lock()  # s4: guard _session_cache + _prev_input_tokens
_msg_queue: "queue.Queue[dict | None]" = queue.Queue()  # s4: worker dispatch queue
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
# Session management — thread_id → Hermes session_id
# ---------------------------------------------------------------------------

# s7: cache stores {id, model} dicts; back-compat: bare str entries tolerated on load
_session_cache: dict[str, dict] = {}
# s7: per-session cumulative input-token watermark for turn-delta computation
_prev_input_tokens: dict[str, int] = {}

# s7: known context windows keyed by Hermes session model string. Values are APPROXIMATE;
# unknown models degrade to no-meter (None — ctx_window will be None for unlisted models).
# 'hermes-agent' is the profile name this server returns for bridge-created sessions
# (discovered via POST /api/sessions probe 2026-06-26). Window is 200k (Claude Sonnet default).
# openrouter/* profiles listed for completeness; add entries as new profiles appear.
MODEL_CONTEXT_WINDOWS: dict[str, int] = {
    "hermes-agent": 200_000,          # Claude Sonnet via Hermes agent profile (VERIFIED)
    "openrouter/owl-alpha": 200_000,   # observed in cron sessions
    "claude-3-5-sonnet-20241022": 200_000,
    "claude-opus-4-5": 200_000,
    "claude-sonnet-4-5": 200_000,
    "claude-haiku-3-5": 200_000,
    "gpt-5": 128_000,                 # approximate
    "gpt-4o": 128_000,
}
_session_store_path = Path(__file__).parent / "session-store.json"

def _load_session_cache():
    """Load thread_id → {id, model} mapping from disk.

    Back-compat: if a stored value is a bare string (old format), migrates it
    to {"id": value, "model": None} transparently so reads still work.
    """
    global _session_cache
    if _session_store_path.exists():
        try:
            raw = json.loads(_session_store_path.read_text())
            migrated = {}
            for k, v in raw.items():
                if isinstance(v, str):
                    migrated[k] = {"id": v, "model": None}
                else:
                    migrated[k] = v
            _session_cache = migrated
            log.info("Loaded %d session mappings from %s", len(_session_cache), _session_store_path)
        except Exception as exc:
            log.warning("Failed to load session cache: %s", exc)
            _session_cache = {}

def _save_session_cache():
    """Persist thread_id → session_id mapping to disk."""
    try:
        _session_store_path.write_text(json.dumps(_session_cache, indent=2))
    except Exception as exc:
        log.warning("Failed to save session cache: %s", exc)

def _api_headers() -> dict:
    """Headers for Hermes API server requests."""
    return {
        "Authorization": f"Bearer {CONFIG['HERMES_API_SERVER_KEY']}",
        "Content-Type": "application/json",
    }

def _load_system_prompt() -> str:
    """Load the system prompt for Hive sessions."""
    prompt_path = CONFIG.get("HERMES_SYSTEM_PROMPT", "")
    if prompt_path and Path(prompt_path).exists():
        return Path(prompt_path).read_text().strip()
    # Default fallback
    default_path = Path(__file__).parent / "hive-system-prompt.txt"
    if default_path.exists():
        return default_path.read_text().strip()
    return (
        "You are Hermes, an AI assistant in a Multica HiveChat thread. "
        "Be direct, concise, and helpful. Answer the actual question being asked. "
        "You have full tool access — use web_search for current info."
    )

def _find_existing_session(thread_id: str) -> Optional[str]:
    """Check if a session already exists for this thread by listing sessions."""
    try:
        resp = _hermes_session.get(
            f"{CONFIG['HERMES_API_URL']}/api/sessions",
            headers=_api_headers(),
            params={"limit": 200},
            timeout=10,
        )
        if resp.status_code == 200:
            sessions = resp.json().get("sessions", resp.json().get("data", {}).get("sessions", []))
            title = f"hive:{thread_id}"
            for s in sessions:
                if s.get("title") == title:
                    return s["id"]
    except Exception as exc:
        log.warning("Failed to list sessions: %s", exc)
    return None

def get_or_create_session(thread_id: str) -> str:
    """Get existing Hermes session ID for a thread, or create a new one.

    s4 locking: cache hit checked under _session_lock (brief, no I/O).
    Network calls happen OUTSIDE the lock to avoid serializing turns behind HTTP.
    Dict mutation + file write are re-locked briefly before returning.
    """
    # Fast path: cache hit (brief lock, no I/O)
    with _session_lock:
        if thread_id in _session_cache:
            return _session_cache[thread_id]["id"]

    # Check disk / API for existing session (outside lock — network I/O)
    existing = _find_existing_session(thread_id)
    if existing:
        with _session_lock:
            _session_cache[thread_id] = {"id": existing, "model": None}
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
                "title": f"hive:{thread_id}",
                "system_prompt": system_prompt,
            },
            timeout=15,
        )
        if resp.status_code == 201:
            session_data = resp.json()["session"]
            session_id = session_data["id"]
            session_model = session_data.get("model")  # s7: capture model at create time
            with _session_lock:
                _session_cache[thread_id] = {"id": session_id, "model": session_model}
                _save_session_cache()
            log.info("Created session %s (model=%s) for thread %s", session_id[:8], session_model, thread_id[:8])
            return session_id
        else:
            log.error("Failed to create session: %s %s", resp.status_code, resp.text[:200])
    except Exception as exc:
        log.error("Session creation failed: %s", exc)

    raise RuntimeError(f"Cannot get or create session for thread {thread_id}")

def _session_model(thread_id: str):
    '''Return the cached model string for thread_id, or None if unknown (s4: brief lock).'''
    with _session_lock:
        entry = _session_cache.get(thread_id)
        if isinstance(entry, dict):
            return entry.get("model")
        return None

# ---------------------------------------------------------------------------
# HTTP helpers (Multica)
# ---------------------------------------------------------------------------

def _multica_auth_headers() -> dict:
    return {
        "Authorization": f"Bearer {CONFIG['HERMES_PAT']}",
        "X-Workspace-Slug": CONFIG["HERMES_WORKSPACE_SLUG"],
    }

def http_get(path: str, params: Optional[dict] = None) -> Union[dict, list]:
    url = CONFIG["HERMES_SERVER_HTTP"] + path
    resp = _multica_session.get(url, headers=_multica_auth_headers(), params=params, timeout=15)
    resp.raise_for_status()
    return resp.json()

def http_post(path: str, body: dict) -> dict:
    url = CONFIG["HERMES_SERVER_HTTP"] + path
    headers = _multica_auth_headers()
    headers["Content-Type"] = "application/json"
    resp = _multica_session.post(url, headers=headers, json=body, timeout=15)
    resp.raise_for_status()
    return resp.json()

# ---------------------------------------------------------------------------
# Hermes brain — persistent session via API server
# ---------------------------------------------------------------------------

def _post_message(
    thread_id: str,
    body: str,
    role: str = "assistant",
    *,
    tokens_used=None,
    context_window=None,
    model=None,
) -> dict:
    """Post a single message to Multica hermes-messages endpoint with role.

    tokens_used, context_window, and model are optional — each is included in the
    POST body only when not None so callers that don't have usage info still work.
    """
    payload: dict = {
        "thread_id": thread_id,
        "workspace_id": CONFIG["HERMES_WORKSPACE_ID"],
        "body": body,
        "role": role,
    }
    if tokens_used is not None:
        payload["tokens_used"] = tokens_used
    if context_window is not None:
        payload["context_window"] = context_window
    if model is not None:
        payload["model"] = model
    return http_post("/api/plugins/hive/hermes-messages", payload)


def hermes_respond_sse(thread_id: str, incoming_body: str, session_id: str, model=None, t_recv: float = 0.0) -> None:
    """
    Stream a Hermes turn via SSE (POST .../chat/stream), posting discrete messages
    to Multica as events arrive.

    Decision: path A — post-on-complete, NO PATCH, NO live token streaming.
    - tool/thinking lines post live as compact single messages.
    - assistant prose posts as ONE message when run.completed fires.
    - s7: tokens_used (turn delta), context_window, and model populated from
          run.completed usage payload and the session-model cache.
    """
    url = f"{CONFIG['HERMES_API_URL']}/api/sessions/{session_id}/chat/stream"
    log.info("TIMING sse_open session=%s thread=%s elapsed=%.3fs",
             session_id[:8], thread_id[:8], time.perf_counter() - t_recv)
    try:
        resp = _hermes_session.post(
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
    _first_visible = False  # s4: guard for TIMING first_visible log
    text_buffer = ""          # accumulate assistant.delta fragments
    final_content = None      # set from assistant.completed (authoritative)
    thinking_posted = False   # guard: post reasoning message only once per run

    # FIX1+FIX3: spec-correct SSE framing — accumulate per-event, dispatch on blank line.
    run_completed = False       # FIX1: track whether run.completed fired
    cur_event_name: Optional[str] = None
    cur_data_lines: list = []

    def _dispatch_event(ev_name: Optional[str], data_lines: list) -> bool:
        """
        Dispatch one fully-accumulated SSE event.
        Returns True if the loop should break (stream done).
        Mutates text_buffer, final_content, thinking_posted via nonlocal.
        """
        nonlocal text_buffer, final_content, thinking_posted, run_completed, _first_visible

        def _mark_first_visible() -> None:
            """Emit TIMING first_visible exactly once per turn."""
            nonlocal _first_visible
            if not _first_visible:
                _first_visible = True
                log.info(
                    "TIMING first_visible session=%s elapsed=%.3fs",
                    session_id[:8], time.perf_counter() - t_recv,
                )


        if not data_lines:
            return False  # heartbeat/comment-only event — skip

        data_str = "\n".join(data_lines)

        if not data_str or data_str == "[DONE]":
            return True  # sentinel — done

        try:
            payload = json.loads(data_str)
        except json.JSONDecodeError:
            log.debug("SSE non-JSON data (event=%s): %s", ev_name, data_str[:80])
            return False

        ev = ev_name

        if ev == "run.started":
            log.debug("SSE run.started")
            # status already set to "running" by caller; no post

        elif ev == "message.started":
            log.debug("SSE message.started: %s", payload.get("message", {}).get("id", "")[:8])

        elif ev == "tool.progress":
            tool_name = payload.get("tool_name", "")
            if tool_name == "_thinking":
                if not thinking_posted:
                    thinking_posted = True
                    try:
                        _post_message(thread_id, "💭 thinking…", role="reasoning")
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
                _post_message(thread_id, compact, role="tool")
                _mark_first_visible()  # s4: tool was first visible
                log.debug("SSE posted tool.started: %s", tool_name)
            except Exception as e:
                log.warning("Failed to post tool message: %s", e)

        elif ev == "tool.completed":
            pass  # No PATCH route; skip retro-update

        elif ev == "tool.failed":
            tool_name = payload.get("tool_name", "")
            try:
                _post_message(thread_id, f"✗ {tool_name} failed", role="tool")
                log.debug("SSE posted tool.failed: %s", tool_name)
            except Exception as e:
                log.warning("Failed to post tool.failed message: %s", e)

        elif ev == "assistant.delta":
            delta = payload.get("delta", "")
            if delta:
                text_buffer += delta

        elif ev == "assistant.completed":
            # Prefer authoritative full content from this event
            final_content = payload.get("content") or text_buffer

        elif ev == "run.completed":
            # Post the assistant message NOW (one message for the whole turn)
            msg_content = final_content or text_buffer
            # s7: compute per-turn token delta from cumulative session totals
            usage = payload.get("usage", {}) or {}
            raw = usage.get("input_tokens")
            turn_tokens = None
            if raw is not None:
                try:
                    cur_input = int(raw)
                except (TypeError, ValueError):
                    cur_input = None
                if cur_input is not None:
                    # s4: lock briefly for watermark get+set (NOT held across SSE or HTTP)
                    with _session_lock:
                        prev = _prev_input_tokens.get(session_id, 0)
                        if cur_input < prev:
                            # session reset / context compaction boundary
                            turn_tokens = cur_input
                        else:
                            turn_tokens = cur_input - prev
                        # ALWAYS update watermark when a numeric value is present (incl 0)
                        if session_id not in _prev_input_tokens and len(_prev_input_tokens) >= 1000:
                            # soft cap: clear ~half to bound memory; dropped sessions recount from 0
                            keys = list(_prev_input_tokens.keys())
                            for k in keys[:500]:
                                del _prev_input_tokens[k]
                        _prev_input_tokens[session_id] = cur_input
            # s7: context window from model→window map; model from caller (session-create cache)
            ctx_window = MODEL_CONTEXT_WINDOWS.get(model) if model else None
            if msg_content:
                try:
                    result = _post_message(
                        thread_id, msg_content, role="assistant",
                        tokens_used=turn_tokens,
                        context_window=ctx_window,
                        model=model,
                    )
                    log.info("SSE run done, assistant message posted: %s (tokens=%s model=%s)",
                             result.get("ID", "?")[:8], turn_tokens, model)
                    _mark_first_visible()  # s4: prose-only turn, this is first visible
                    log.info("TIMING assistant_posted session=%s elapsed=%.3fs",
                             session_id[:8], time.perf_counter() - t_recv)
                except Exception as e:
                    log.error("Failed to post assistant message on run.completed: %s", e)
                    raise
            else:
                log.warning("SSE run.completed with no content — nothing posted")
            _set_thread_status(thread_id, "posting")
            run_completed = True
            return True  # break — done

        elif ev == "done":
            return True  # break

        else:
            log.debug("SSE unknown event: %s", ev)

        return False

    for raw_line in resp.iter_lines():
        if raw_line is None:
            break
        if isinstance(raw_line, bytes):
            raw_line = raw_line.decode("utf-8", errors="replace")

        if raw_line.startswith("event:"):
            cur_event_name = raw_line[len("event:"):].strip()

        elif raw_line.startswith("data:"):
            cur_data_lines.append(raw_line[len("data:"):].strip())

        elif raw_line == "":
            # Blank line — dispatch the accumulated event, then reset
            if _dispatch_event(cur_event_name, cur_data_lines):
                cur_event_name = None
                cur_data_lines = []
                break
            cur_event_name = None
            cur_data_lines = []

        else:
            log.debug("SSE unexpected line format: %s", raw_line[:80])

    # Flush any partial event that arrived without a trailing blank line
    if cur_data_lines and not run_completed:
        _dispatch_event(cur_event_name, cur_data_lines)

    # FIX1: post-loop guard — if stream ended without run.completed, rescue buffered text
    if not run_completed:
        rescue_content = final_content or text_buffer
        if rescue_content:
            log.warning("SSE stream ended without run.completed — posting buffered assistant text")
            try:
                _post_message(
                    thread_id, rescue_content, role="assistant",
                    model=model,  # s7: pass model if available; tokens unknown in guard path
                )
                _set_thread_status(thread_id, "posting")
            except Exception as e:
                log.error("Failed to post rescued assistant content: %s", e)
                raise
        else:
            log.warning("SSE stream ended without run.completed and no buffered content")


# ---------------------------------------------------------------------------
# Message handler
# ---------------------------------------------------------------------------

def handle_message(msg: dict):
    """Process a single hive:message:created event."""
    t_recv = time.perf_counter()  # s4: timing — message received from WS queue
    msg_id = msg.get("ID", "")
    thread_id = msg.get("ThreadID", "")
    author_id = msg.get("AuthorID", "")
    body = msg.get("Body", "")

    # Loop guard: skip own messages (critical now that the bot posts multiple
    # messages per turn — tool/reasoning lines + final assistant message)
    if author_id == CONFIG["HERMES_BOT_USER_ID"]:
        log.info("Skipping own message %s (loop guard)", msg_id[:8])
        return

    # Idempotency — ring buffer prevents duplicate processing
    if seen(msg_id):
        log.debug("Already handled %s", msg_id[:8])
        return

    log.info("New message in %s from %s: %s", thread_id[:8], author_id[:8], body[:80])
    _update_bridge_status(mark_event=True, last_error="")
    _set_thread_status(thread_id, "running", message_id=msg_id)

    session_id = get_or_create_session(thread_id)

    # Handle 404 (session expired) by recreating once before giving up
    try:
        hermes_respond_sse(thread_id, body, session_id, model=_session_model(thread_id), t_recv=t_recv)
    except requests.HTTPError as e:
        if e.response is not None and e.response.status_code == 404:
            log.warning("Session %s not found, recreating for thread %s", session_id[:8], thread_id[:8])
            with _session_lock:
                _session_cache.pop(thread_id, None)
                _save_session_cache()
            session_id = get_or_create_session(thread_id)
            try:
                hermes_respond_sse(thread_id, body, session_id, model=_session_model(thread_id), t_recv=t_recv)
            except Exception as e2:
                log.error("SSE retry after session recreate failed: %s", e2)
                _set_thread_status(thread_id, "error", message_id=msg_id, error=str(e2))
                _update_bridge_status(last_error=str(e2))
                return
        else:
            log.error("SSE stream HTTP error: %s", e)
            _set_thread_status(thread_id, "error", message_id=msg_id, error=str(e))
            _update_bridge_status(last_error=str(e))
            return
    except Exception as e:
        log.error("SSE stream failed: %s", e)
        _set_thread_status(thread_id, "error", message_id=msg_id, error=str(e))
        _update_bridge_status(last_error=str(e))
        return

    _set_thread_status(thread_id, "idle", message_id=msg_id)

# ---------------------------------------------------------------------------
# Worker queue (s4: process messages off the WS-recv thread)
# ---------------------------------------------------------------------------

def _worker() -> None:
    """Drain _msg_queue; block on None poison-pill to exit."""
    while True:
        msg = _msg_queue.get()
        if msg is None:
            _msg_queue.task_done()
            break
        try:
            handle_message(msg)
        except Exception as e:
            log.error("Worker error: %s", e)
        finally:
            _msg_queue.task_done()

# ---------------------------------------------------------------------------
# WebSocket loop
# ---------------------------------------------------------------------------

def run_ws():
    """Main WebSocket subscribe loop with reconnect backoff."""
    ws_url = f"{CONFIG['HERMES_SERVER_WS']}/ws?workspace_id={CONFIG['HERMES_WORKSPACE_ID']}"
    backoff = 1

    ws = None
    while True:
        log.info("Connecting to %s", ws_url)
        try:
            ws = websocket.create_connection(ws_url, timeout=30)
            log.info("Connected. Sending auth...")

            # Send auth frame
            auth_frame = json.dumps({
                "type": "auth",
                "payload": {"token": CONFIG["HERMES_PAT"]},
            })
            ws.send(auth_frame)

            # Wait for auth_ack
            ws.settimeout(10)
            raw = ws.recv()
            ack = json.loads(raw)
            if ack.get("type") != "auth_ack":
                log.error("Auth failed: %s", ack)
                _update_bridge_status(connected=False, last_error=f"auth failed: {ack}")
                ws.close()
                time.sleep(backoff)
                backoff = min(backoff * 2, CONFIG["HERMES_RECONNECT_MAX_S"])
                continue

            log.info("Auth OK. Listening for events...")
            _update_bridge_status(connected=True, last_error="", mark_connect=True, mark_heartbeat=True)
            backoff = 1  # reset on successful connect

            # Event loop
            ws.settimeout(None)  # block forever
            while True:
                raw = ws.recv()
                event = json.loads(raw)
                event_type = event.get("type", "")
                _update_bridge_status(mark_event=True)

                if event_type != "hive:message:created":
                    continue

                payload = event.get("payload", {})
                message = payload.get("message", {})
                if not message:
                    continue

                _msg_queue.put(message)  # s4: hand off to worker thread

        except websocket.WebSocketTimeoutException:
            log.warning("WS timeout, reconnecting...")
            _update_bridge_status(connected=False, last_error="websocket timeout")
        except websocket.WebSocketConnectionClosedException:
            log.warning("WS connection closed, reconnecting...")
            _update_bridge_status(connected=False, last_error="websocket connection closed")
        except ConnectionRefusedError:
            log.error("Connection refused — is Multica running?")
            _update_bridge_status(connected=False, last_error="connection refused")
        except Exception as e:
            log.error("WS error: %s", e)
            _update_bridge_status(connected=False, last_error=str(e))
        finally:
            if ws is not None:
                try:
                    ws.close()
                except Exception:
                    pass
            _update_bridge_status(connected=False)

        log.info("Reconnecting in %ds...", backoff)
        time.sleep(backoff)
        backoff = min(backoff * 2, CONFIG["HERMES_RECONNECT_MAX_S"])

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    # Validate config
    missing = [k for k in ["HERMES_PAT", "HERMES_BOT_USER_ID", "HERMES_WORKSPACE_ID", "HERMES_API_SERVER_KEY"]
               if not CONFIG.get(k)]
    if missing:
        log.error("Missing required config: %s", ", ".join(missing))
        log.error("Set them as environment variables or in a .env file next to this script.")
        sys.exit(1)

    log.info("Hermes ↔ HiveChat bridge starting (persistent sessions)")
    log.info("  Multica HTTP: %s", CONFIG["HERMES_SERVER_HTTP"])
    log.info("  Multica WS:   %s", CONFIG["HERMES_SERVER_WS"])
    log.info("  Hermes API:   %s", CONFIG["HERMES_API_URL"])
    log.info("  Bot:          %s", CONFIG["HERMES_BOT_USER_ID"][:8])
    log.info("  Workspace:    %s", CONFIG["HERMES_WORKSPACE_ID"][:8])

    # Load session cache
    _load_session_cache()
    _update_bridge_status(connected=False, last_error="")

    heartbeat = threading.Thread(target=_heartbeat_loop, name="bridge-heartbeat", daemon=True)
    heartbeat.start()

    # s4: spawn 2 worker threads to process messages concurrently
    global _workers
    for i in range(2):
        w = threading.Thread(target=_worker, name=f"bridge-worker-{i}", daemon=True)
        w.start()
        _workers.append(w)
    log.info("Started %d bridge worker threads", len(_workers))

    # Handle graceful shutdown
    def _shutdown(signum, frame):
        log.info("Shutting down...")
        _status_stop.set()
        # s4: poison-pill workers so they exit cleanly
        for _ in _workers:
            _msg_queue.put(None)
        _update_bridge_status(connected=False)
        with _session_lock:
            _save_session_cache()
        sys.exit(0)
    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    run_ws()

if __name__ == "__main__":
    main()
