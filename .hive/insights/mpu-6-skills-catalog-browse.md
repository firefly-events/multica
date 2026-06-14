# mpu-6: Skills Catalog Browse — Insights

## Go embed boundary

`//go:embed` paths must be *within* the Go module root (`server/`). The
TypeScript `packages/hive/` directory is outside the server module, so the
catalog JSON cannot be shared at the filesystem level — each layer keeps its
own copy. If catalog content diverges in the future, a build-time script that
copies `packages/hive/catalog/*.json → server/internal/hive/catalog/` would
keep them in sync without crossing module boundaries.

## Async goroutine context

The browse-event goroutine must use `context.Background()`, not `r.Context()`.
The request context is cancelled the moment the handler returns, so any
in-flight DB write using it will silently fail. This is a subtle bug class:
goroutines outliving the HTTP handler always need a fresh context.

## hive.plugin_skill_catalog_state design

The table is workspace-scoped with a UNIQUE constraint on `workspace_id`.
`ON CONFLICT ... DO UPDATE SET workspace_id = EXCLUDED.workspace_id` on
`GetOrCreateCatalogState` is a no-op update — it exists only to trigger the
`RETURNING` clause without an error, making the upsert idempotent. This is the
standard PostgreSQL pattern for "get-or-insert" without a SELECT+INSERT race.

## Browse-only contract

The catalog endpoint never touches `public.skill` or `public.skill_file`.
State tracking in `hive.plugin_skill_catalog_state` records *that* a workspace
browsed and *which version* they saw. mpu-7 (materialization) can later use
`last_browsed_at IS NOT NULL` as an intent signal before offering import.

## Frontend placement

`SkillCatalogPanel` renders below the workspace skills table on the existing
Skills page — always visible, no route change needed. This satisfies the
"reuse existing Skills page" sign-off while keeping the two data sources
(workspace DB vs embedded catalog) visually separate.
