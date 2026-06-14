# mpu-4 PersonalQueue — Insights

## Auth middleware sets X-User-ID as a request header, not context
The `middleware.Auth` sets `r.Header.Set("X-User-ID", userID)` before calling next. Hive handlers inherit auth via the outer group mount but don't use a context key — extract the user with `r.Header.Get("X-User-ID")`. An empty string means unauthenticated (return 401).

## User isolation via SQL predicate, not app-layer filter
`UpdatePersonalQueueItem` includes `AND assignee_id = $2::uuid` in the WHERE clause. If the row exists but belongs to another user, `pgx.ErrNoRows` is returned — exactly as if the item didn't exist. This is safer than fetching then checking, because it eliminates the TOCTOU window and keeps the ownership check atomic.

## Wrong-user fixture pattern
To test that a user never sees another user's items: insert a row with `assignee_id = userB`, then query as `userA` (X-User-ID = userA). The list handler passes `userA`'s ID to the SQL `WHERE assignee_id = $2::uuid`, so the row is invisible. No app-layer filtering needed.

## NavKey + NavLabelKey types must stay in sync
Both types in `app-sidebar.tsx` are discriminated unions. Adding a new hive nav entry requires touching: NavKey, NavLabelKey, workspaceNav array, both locale files (en + zh-Hans), and the path builder. The parity test (`locales/parity.test.ts`) enforces en/zh-Hans sync at CI time.

## `hiveQueue` path lives in `workspaceScoped()`, not `personalNav`
Even though the queue is user-scoped by authz, its URL is workspace-scoped (`/:slug/hive/queue`). This matches how "My Issues" works — user-filtered server-side, but URL carries workspace slug for routing consistency.
