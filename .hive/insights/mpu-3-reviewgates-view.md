# mpu-3 ReviewGates — Insights

## Go: pgx.ErrNoRows import needed for UPDATE returns
`pgx.ErrNoRows` requires a direct import of `"github.com/jackc/pgx/v5"` even when pool is from `pgxpool` — the error sentinel lives in the base package.

## Sidebar nav pattern: NavKey → WorkspacePaths method
Adding a nav item requires touching 6 places in a fixed order:
1. `NavKey` union type in `app-sidebar.tsx`
2. `NavLabelKey` union type in `app-sidebar.tsx`
3. `workspaceNav` array entry (key + labelKey + icon)
4. `paths.ts` `workspaceScoped` method
5. `consistency.test.ts` — both the Set and the expectedSegments array
6. Both locale files (en + zh-Hans)
Missing any one causes a TS error or a failing test.

## Migration ledger is independent per plugin
`hive.schema_migrations` is the hive plugin's own ledger (not `public.schema_migrations`). New migrations just need a `002_*.up.sql` file; `RunMigrations` discovers and applies them in lexicographic order.

## Upsert on (workspace_id, epic_id, gate_key) is safer than separate create+update
The `ON CONFLICT DO UPDATE` pattern avoids race conditions if two callers try to create the same gate simultaneously. The `CreateReviewGate` method doubles as an upsert so idempotent seeding is trivial.

## `packages/hive` is not pre-compiled — export raw `.tsx`
The package follows the "Internal Packages" pattern. No build step; consuming app's bundler (Next.js turbopack) compiles it directly. Just add exports to `index.tsx`.
