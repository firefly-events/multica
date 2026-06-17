# Plan: Persistent Hermes Session per Hive Thread (Option C)

## Goal
Replace the one-shot `hermes chat -q` bridge with persistent Hermes sessions
keyed by Hive thread ID. Each Hive thread gets its own Hermes session with full
conversation memory, proper system prompt, and tool access.

## Architecture
```
Hive thread message → bridge looks up/creates session for thread_id →
  POST /api/sessions/{session_id}/chat {message: "..."} →
  Hermes gateway (full system prompt + tools + conversation memory) →
  contextual reply
```

## Prerequisites
1. **API server must be enabled** on the Hermes gateway (port 8642 by default)
   - Add `api_server` platform to config.yaml
   - Set `platforms.api_server.key` for auth
   - Restart gateway
2. **Bridge must know the API server key** for auth

## Implementation Steps

### Step 1: Enable the API server on the Hermes gateway
Add to `~/.hermes/config.yaml`:
```yaml
platforms:
  api_server:
    key: <generate-a-random-key>
    port: 8642
    host: 127.0.0.1
```

### Step 2: Rewrite the bridge's hermes_respond() function
Replace subprocess `hermes chat -q` with HTTP calls to the Hermes API server:

```python
def get_or_create_session(thread_id: str, thread_title: str = "") -> str:
    """Get existing session ID for thread, or create a new one."""
    # Check in-memory cache first
    if thread_id in _session_cache:
        return _session_cache[thread_id]
    
    # Try to find existing session by title
    resp = requests.get(
        f"{HERMES_API_URL}/api/sessions",
        headers=_api_headers(),
        params={"limit": 100},
    )
    if resp.status_code == 200:
        sessions = resp.json().get("sessions", [])
        for s in sessions:
            if s.get("title") == f"hive:{thread_id}" or s.get("title") == thread_title:
                _session_cache[thread_id] = s["id"]
                _save_session_cache()
                return s["id"]
    
    # Create new session with proper system prompt
    resp = requests.post(
        f"{HERMES_API_URL}/api/sessions",
        headers={**_api_headers(), "Content-Type": "application/json"},
        json={
            "title": f"hive:{thread_id}",
            "system_prompt": _load_system_prompt(),
        },
    )
    if resp.status_code == 201:
        session_id = resp.json()["session"]["id"]
        _session_cache[thread_id] = session_id
        _save_session_cache()
        return session_id
    
    raise RuntimeError(f"Failed to create session: {resp.status_code} {resp.text}")


def hermes_respond(thread_id: str, incoming_body: str) -> str:
    """Send message to persistent Hermes session and return reply."""
    session_id = get_or_create_session(thread_id)
    
    resp = requests.post(
        f"{HERMES_API_URL}/api/sessions/{session_id}/chat",
        headers={**_api_headers(), "Content-Type": "application/json"},
        json={"message": incoming_body},
        timeout=120,
    )
    
    if resp.status_code == 404:
        # Session expired/invalid, recreate
        _session_cache.pop(thread_id, None)
        session_id = get_or_create_session(thread_id)
        resp = requests.post(
            f"{HERMES_API_URL}/api/sessions/{session_id}/chat",
            headers={**_api_headers(), "Content-Type": "application/json"},
            json={"message": incoming_body},
            timeout=120,
        )
    
    if resp.status_code == 200:
        return resp.json()["message"]["content"]
    
    raise RuntimeError(f"Hermes API error: {resp.status_code} {resp.text}")
```

### Step 3: Write a proper system prompt
Create `hive-system-prompt.txt` that gives the model:
- Identity and personality (concise, direct, helpful)
- Context that this is a Hive chat thread
- Instructions to respond to the actual message, not meta-comment
- No need for the full Hermes system prompt — just enough to be useful

### Step 4: Add session persistence
- In-memory dict `_session_cache: {thread_id: session_id}`
- Persisted to `session-store.json` next to the bridge script
- Loaded on startup, saved on every new session creation

### Step 5: Update .env
Add:
```
HERMES_API_URL=http://localhost:8642
HERMES_API_SERVER_KEY=<key-from-step-1>
```

### Step 6: End-to-end test
1. Enable API server in config, restart gateway
2. Start the bridge
3. Send a message in a Hive thread
4. Verify: session created, reply is contextual
5. Send a follow-up message
6. Verify: Hermes remembers the conversation
7. Send "what's the weather in 78753"
8. Verify: Hermes uses web_search and gives actual weather

## Files to modify
- `~/.hermes/config.yaml` — add api_server platform config
- `hermes_bridge.py` — rewrite hermes_respond() + add session management
- `.env` — add HERMES_API_URL and HERMES_API_SERVER_KEY

## Files to create
- `hive-system-prompt.txt` — system prompt for Hive sessions
- `session-store.json` — thread_id → session_id mapping (auto-created)
