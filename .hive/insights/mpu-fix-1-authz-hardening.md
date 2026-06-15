# mpu-fix-1 — Workspace Authz Hardening Insights

## Middleware group scope vs. mount position
chi's `r.Group(func(r chi.Router) { r.Use(...) })` only applies to routes registered *inside* that closure. A `r.Mount(...)` placed *after* the closing `})` is at the parent router level and does not inherit the group's middleware. This is the most common authz bypass pattern in chi apps. Always verify mounts are physically inside the correct group closure.

## Never trust client-supplied workspace_id in multi-tenant APIs
Even when a route is behind workspace middleware, a handler that reads `workspace_id` from the request body/query and passes it to the store allows cross-workspace IDOR. The only trusted source is `middleware.WorkspaceIDFromContext`. Reject mismatches with 403, do not silently honor the client value.

## Bare-ID store queries need workspace_id predicates
A store method like `WHERE id = $1::uuid` without `AND workspace_id = $2::uuid` is a latent cross-workspace read. Any caller who guesses or enumerates a UUID can read across tenant boundaries. Add the workspace predicate at the store layer, not just the handler layer, so future callers can't accidentally bypass it.

## Atomic thread-ownership enforcement via subquery INSERT
Rather than SELECT-then-INSERT (TOCTOU), use a subquery INSERT that selects from the parent table with the workspace constraint. If the parent row doesn't match, zero rows are inserted and pgx returns ErrNoRows — cleanly mappable to 404. No separate ownership check needed.

## ON CONFLICT eliminates TOCTOU in upsert patterns
A pre-check SELECT followed by a conditional INSERT has a race window even inside a transaction (unless SERIALIZABLE). Replace with `ON CONFLICT (unique_cols) DO NOTHING RETURNING id` — if no row is returned, a concurrent insert won the race and the caller gets a clean collision signal rather than a 500 unique-violation.

## Tuple cursors for Hermes pagination
`created_at < $cursor` skips messages sharing the boundary timestamp. The correct cursor for DESC pagination is `(created_at, id) < ($ts, $id)` which is lexicographic in Postgres and deterministic even when timestamps collide. Both cursor components must travel together; a timestamp-only cursor is broken by design.

## Bounded goroutines for fire-and-forget DB writes
`context.Background()` goroutines for async DB writes can pile up indefinitely under DB stalls or request floods. Use `context.WithTimeout(context.Background(), N*time.Second)` and always `defer cancel()`. The timeout is a backstop, not a correctness requirement — the write is best-effort by design.

## Transactional DDL in PostgreSQL
Unlike MySQL, PostgreSQL supports transactional DDL. Wrapping each migration's DDL + ledger INSERT in a single `BEGIN/COMMIT` means a crash after DDL but before the ledger record is fully rolled back on restart, keeping the migration runner replayable. This is safe for all standard DDL (CREATE TABLE, ALTER TABLE, CREATE INDEX, etc.).
