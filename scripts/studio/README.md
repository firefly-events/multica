# Studio host — persistent launchd setup

Runs the Multica fork (backend :8080 + frontend :3000 + pgvector pg17 in podman) as
always-on launchd services on the mac-studio host. Paths assume ssh user `hive` and the
repo at `/Users/hive/Code/spikes/multica`. See
`.pHive/epics/multica-plugin-ui/docs/studio-migration-runbook.md` for the full migration.

## Install
```sh
chmod +x scripts/studio/*.sh
cp scripts/studio/events.firefly.multica.*.plist ~/Library/LaunchAgents/
for p in postgres backend web; do
  launchctl load -w ~/Library/LaunchAgents/events.firefly.multica.$p.plist
done
```

## Files
- `ensure-db.sh` — starts the podman machine + `multica-postgres-1` container (RunAtLoad one-shot).
- `run-backend.sh` — sources `.env`, PORT=8080, waits for postgres, execs `server/bin/server` (KeepAlive).
- `run-web.sh` — sources `.env`, PORT=3000 (overrides .env's backend PORT), `next start` (KeepAlive).

## Notes
- Backend runs Hive migrations (`hive.*` schema) at boot; `/healthz` reports `migrations`.
- macOS reboot: launchd `RunAtLoad` fires at login; the podman *machine* must come up first
  (`ensure-db.sh` starts it). Verify across a full power-cycle before trusting always-on.
- Rebuild after pulling: `cd server && go build -o bin/server ./cmd/server` and
  `pnpm --filter @multica/web build`, then `launchctl kickstart -k` the backend/web labels.
