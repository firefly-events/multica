#!/bin/bash
# Studio launchd service: Hermes <-> HiveChat bridge.
# Mirrors run-backend.sh. MUST use /usr/bin/python3 — it has requests +
# websocket installed; Homebrew python3 does not (ModuleNotFoundError).
# Status path is pinned so this bridge and the Go server
# (MULTICA_HERMES_BRIDGE_STATUS_PATH in run-backend.sh) agree on one file
# regardless of each process's TMPDIR — a mismatch made the bridge look
# dead while it was actually writing to a different per-user temp dir.
export PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin
export HERMES_BRIDGE_STATUS_PATH=/tmp/multica-hermes-bridge-status.json
cd /Users/hive/Code/spikes/multica/docs/hive-plugin
set -a; source .env; set +a
# Wait for the backend API (:8080) before connecting. /dev/tcp is a bash
# builtin — no curl/nc dependency.
for i in $(seq 1 30); do
  (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null && { exec 3>&-; break; }
  sleep 2
done
exec /usr/bin/python3 hermes_bridge.py
