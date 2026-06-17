# Multica fork → mac-studio migration runbook

**Target:** `macstudio` (192.168.86.91, ssh user `hive`) — the permanent host; Hermes
already runs there (:9119), so the Hermes↔HiveChat bridge becomes localhost-local.
**Decisions:** drive over SSH · persistent + launchd autostart · **fresh DB** (no data carry-over).
**Source of truth:** `firefly-events/multica` @ `feat/multica-plugin-ui` (`579a9dd`).

Studio starting state (probed): node ✓ pnpm ✓ podman ✓ brew ✓; **Go absent**, fork not
cloned, no postgres, podman machine not started, `gh` auth broken but `git` reaches the fork.

---

## Phase 1 — toolchain
```sh
ssh macstudio
podman machine init   # if no machine exists
podman machine start
brew install go        # need >= 1.26
go version
```

## Phase 2 — clone
```sh
mkdir -p ~/Code/spikes && cd ~/Code/spikes
git clone https://github.com/firefly-events/multica.git
cd multica && git checkout feat/multica-plugin-ui && git log --oneline -1   # expect 579a9dd
```

## Phase 3 — postgres (podman)
Run pgvector pg17 (same image as dev), bound to localhost:5432:
```sh
podman run -d --name multica-postgres-1 \
  -e POSTGRES_USER=multica -e POSTGRES_PASSWORD=multica -e POSTGRES_DB=multica \
  -p 127.0.0.1:5432:5432 docker.io/pgvector/pgvector:pg17
```

## Phase 4 — config (.env), fresh secrets
Generate `~/Code/spikes/multica/.env` from `.env.example`. Critical keys:
```
DATABASE_URL=postgres://multica:multica@localhost:5432/multica?sslmode=disable
PORT=8080
MULTICA_SERVER_URL=ws://localhost:8080/ws
MULTICA_APP_URL=http://localhost:3000
APP_ENV=dev
MULTICA_DEV_VERIFICATION_CODE=888888     # dev login code
JWT_SECRET=<freshly generated, do NOT reuse the dev box secret>
```
(Frontend reads `REMOTE_API_URL` default `http://localhost:8080` — fine on same host.)

## Phase 5 — fresh DB + migrations
Core migrations via the migrate cmd; Hive migrations run at server boot.
```sh
cd ~/Code/spikes/multica/server
DATABASE_URL=... go run ./cmd/migrate up      # core schema
# hive.* schema is applied automatically when the server boots (Phase 6)
```

## Phase 6 — build + persistent run (launchd)
Build native binaries + a production frontend, then install launchd agents so all three
autostart on boot (matches Hermes).
```sh
cd ~/Code/spikes/multica
make build                  # server, cli, migrate → server/bin (or `go build ./cmd/server`)
pnpm install --config.engine-strict=false
pnpm --filter @multica/web build    # next build (production)
```
launchd agents under `~/Library/LaunchAgents/`:
- `events.firefly.multica.postgres.plist` — `podman start multica-postgres-1` (or a restart policy)
- `events.firefly.multica.backend.plist` — runs `server/bin/server` with `.env` loaded, KeepAlive
- `events.firefly.multica.web.plist` — `pnpm --filter @multica/web start` (next start, port 3000), KeepAlive
`launchctl load -w` each. (Dev-parity fallback: `make dev` / native `go run` + `pnpm dev:web`.)

## Phase 7 — verify
```sh
curl -s localhost:8080/healthz                 # {"status":"ok","checks":{...,"migrations":"ok"}}
podman exec multica-postgres-1 psql -U multica -d multica -c '\dt hive.*'   # 8 hive tables
# browser: http://<studio>:3000 → login (dev code 888888) → Hive sidebar views
```

## Phase 8 — Hermes bridge (separate; see hermes-bridge-spec.md)
On the studio (Hermes is local now): create the `hermes-bot` user + PAT, set env, run the
subscriber. Multica WS + API are at `localhost:8080`.

## Phase 9 — cutover / teardown
- Confirm studio stack healthy + autostarts across a reboot.
- Stop the dev-box stack (this machine): `kill` backend/web, `podman stop multica-postgres-1`.
- Future: point any external clients at the studio host.

## Access from the local Mac without colliding with local Multica
Keep the local instance on `http://localhost:3000`, and tunnel the Studio instance to
alternate local ports instead:

```sh
ssh -N \
  -L 3300:127.0.0.1:3000 \
  -L 38080:127.0.0.1:8080 \
  -L 39119:127.0.0.1:9119 \
  hive@192.168.86.91
```

Then use:
- Studio Multica web: `http://localhost:3300`
- Studio backend API: `http://localhost:38080`
- Studio Hermes dashboard: `http://localhost:39119`

See `docs/hive-plugin/studio-ssh-tunnel.md` for a reusable `~/.ssh/config` entry.

---
### Open items
- Go version pin: confirm brew Go ≥ 1.26 (else install a pinned toolchain).
- Frontend prod: `next start` needs `next build`; if Turbopack-only dev is preferred short-term, run `pnpm dev:web` under launchd instead.
- gh auth on studio is broken — fix if PAT/issue API automation is wanted there, but `git` clone/pull already work.
- External access: currently all localhost-bound; if other devices must reach Multica, bind `0.0.0.0` + firewall + real `MULTICA_PUBLIC_URL`.
