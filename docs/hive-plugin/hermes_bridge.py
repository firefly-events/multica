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
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional, Union

import websocket
import requests

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

def seen(msg_id: str) -> bool:
    """Return True if we've already handled this message ID."""
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

_session_cache: dict[str, str] = {}
_session_store_path = Path(__file__).parent / "session-store.json"

def _load_session_cache():
    """Load thread_id → session_id mapping from disk."""
    global _session_cache
    if _session_store_path.exists():
        try:
            _session_cache = json.loads(_session_store_path.read_text())
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
        resp = requests.get(
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
    """Get existing Hermes session ID for a thread, or create a new one."""
    # Check in-memory cache first
    if thread_id in _session_cache:
        return _session_cache[thread_id]

    # Check disk / API for existing session
    existing = _find_existing_session(thread_id)
    if existing:
        _session_cache[thread_id] = existing
        _save_session_cache()
        log.info("Found existing session %s for thread %s", existing[:8], thread_id[:8])
        return existing

    # Create new session
    system_prompt = _load_system_prompt()
    try:
        resp = requests.post(
            f"{CONFIG['HERMES_API_URL']}/api/sessions",
            headers=_api_headers(),
            json={
                "title": f"hive:{thread_id}",
                "system_prompt": system_prompt,
            },
            timeout=15,
        )
        if resp.status_code == 201:
            session_id = resp.json()["session"]["id"]
            _session_cache[thread_id] = session_id
            _save_session_cache()
            log.info("Created session %s for thread %s", session_id[:8], thread_id[:8])
            return session_id
        else:
            log.error("Failed to create session: %s %s", resp.status_code, resp.text[:200])
    except Exception as exc:
        log.error("Session creation failed: %s", exc)

    raise RuntimeError(f"Cannot get or create session for thread {thread_id}")

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
    resp = requests.get(url, headers=_multica_auth_headers(), params=params, timeout=15)
    resp.raise_for_status()
    return resp.json()

def http_post(path: str, body: dict) -> dict:
    url = CONFIG["HERMES_SERVER_HTTP"] + path
    headers = _multica_auth_headers()
    headers["Content-Type"] = "application/json"
    resp = requests.post(url, headers=headers, json=body, timeout=15)
    resp.raise_for_status()
    return resp.json()

# ---------------------------------------------------------------------------
# Hermes brain — persistent session via API server
# ---------------------------------------------------------------------------

def hermes_respond(thread_id: str, incoming_body: str) -> str:
    """
    Send message to the persistent Hermes session for this thread and return the reply.
    Uses the Hermes API server for full conversation memory and tool access.
    """
    session_id = get_or_create_session(thread_id)

    try:
        resp = requests.post(
            f"{CONFIG['HERMES_API_URL']}/api/sessions/{session_id}/chat",
            headers=_api_headers(),
            json={"message": incoming_body},
            timeout=120,
        )

        if resp.status_code == 404:
            # Session expired/invalid, recreate
            log.warning("Session %s not found, recreating for thread %s", session_id[:8], thread_id[:8])
            _session_cache.pop(thread_id, None)
            _save_session_cache()
            session_id = get_or_create_session(thread_id)
            resp = requests.post(
                f"{CONFIG['HERMES_API_URL']}/api/sessions/{session_id}/chat",
                headers=_api_headers(),
                json={"message": incoming_body},
                timeout=120,
            )

        if resp.status_code == 200:
            data = resp.json()
            content = data.get("message", {}).get("content", "")
            return content
        else:
            log.error("Hermes API error: %s %s", resp.status_code, resp.text[:300])
            return f"[Bridge error: HTTP {resp.status_code}]"

    except requests.Timeout:
        log.error("Hermes API timed out for thread %s", thread_id[:8])
        return "[Bridge: timeout — Hermes took too long to respond]"
    except Exception as e:
        log.error("Hermes API call failed: %s", e)
        return f"[Bridge error: {e}]"

# ---------------------------------------------------------------------------
# Message handler
# ---------------------------------------------------------------------------

def handle_message(msg: dict):
    """Process a single hive:message:created event."""
    msg_id = msg.get("ID", "")
    thread_id = msg.get("ThreadID", "")
    author_id = msg.get("AuthorID", "")
    body = msg.get("Body", "")

    # Loop guard: skip own messages
    if author_id == CONFIG["HERMES_BOT_USER_ID"]:
        log.info("Skipping own message %s (loop guard)", msg_id[:8])
        return

    # Idempotency
    if seen(msg_id):
        log.debug("Already handled %s", msg_id[:8])
        return

    log.info("New message in %s from %s: %s", thread_id[:8], author_id[:8], body[:80])
    _update_bridge_status(mark_event=True, last_error="")
    _set_thread_status(thread_id, "running", message_id=msg_id)

    # Get Hermes to respond via persistent session
    reply_text = hermes_respond(thread_id, body)

    if not reply_text:
        log.warning("Empty reply, skipping post")
        _set_thread_status(thread_id, "error", message_id=msg_id, error="empty reply")
        return

    # Post reply
    try:
        _set_thread_status(thread_id, "posting", message_id=msg_id)
        result = http_post(
            "/api/plugins/hive/hermes-messages",
            {
                "thread_id": thread_id,
                "workspace_id": CONFIG["HERMES_WORKSPACE_ID"],
                "body": reply_text,
            },
        )
        log.info("Reply posted: %s", result.get("ID", "?")[:8])
        _set_thread_status(thread_id, "idle", message_id=result.get("ID", "") or msg_id)
    except Exception as e:
        log.error("Failed to post reply: %s", e)
        _set_thread_status(thread_id, "error", message_id=msg_id, error=str(e))
        _update_bridge_status(last_error=str(e))

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

                handle_message(message)

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

    # Handle graceful shutdown
    def _shutdown(signum, frame):
        log.info("Shutting down...")
        _status_stop.set()
        _update_bridge_status(connected=False)
        _save_session_cache()
        sys.exit(0)
    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    run_ws()

if __name__ == "__main__":
    main()
