# mpu-1 Proof Gate — Insights

## Router mount is one-liner

`NewRouterWithOptions` already receives the pool. Adding `HiveStore *hive.Store` to
`RouterOptions` and `r.Mount("/api/plugins/hive", hive.Router(opts.HiveStore))` inside
the existing `r.Group(middleware.Auth...)` block at the bottom (after the workspace-member
subgroup closes) is all that's needed. Auth inherited automatically — no separate
middleware registration.

## Migration ledger is independent

Core uses `public.schema_migrations`. Hive uses `hive.schema_migrations` inside its own
`hive` schema. The bootstrap SQL (`CREATE SCHEMA IF NOT EXISTS hive; CREATE TABLE IF NOT
EXISTS hive.schema_migrations ...`) runs before any migration is applied, so idempotent on
restart. Never mix the two ledgers — `cmd/migrate` and `server/internal/migrations` only
know about `public.schema_migrations`.

## Fail-fast beats readiness flag for schema gates

Calling `hive.RunMigrations(ctx, pool)` before `NewRouterWithOptions` means a broken Hive
migration kills the process before it binds any port. Simpler than extending `serverHealth`
with a second migration check, and satisfies "never serves Hive routes against stale schema"
trivially.

## embed.FS path must match directory structure

`//go:embed migrations/*.sql` embeds with path prefix `migrations/`. `fs.WalkDir` must
walk from `"migrations"` (not `"."`) to match. `filepath.Base` on the walked path strips
the prefix for the version key stored in `hive.schema_migrations`.

## pgx JSONB → []byte

`store.go` scans `payload JSONB` into `[]byte`. pgx v5 delivers JSON columns as raw bytes
when the scan target is `*[]byte` — no extra unmarshalling needed. Inserting with
`$1::jsonb` cast validates JSON on the Postgres side.

## No sqlc for Hive

Views 2-8 can add sqlc queries under `server/internal/hive/queries/` with a separate
`sqlc.yaml` entry pointing at a `hive/` schema dir. Do NOT add Hive tables to
`server/sqlc.yaml` or `server/migrations/` — that would violate the clean separation gate.
