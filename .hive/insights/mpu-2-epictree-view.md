# mpu-2 EpicTree View — Non-obvious Insights

## Adding a workspace package to Multica frontend

1. **Internal packages pattern** — `packages/*` folders are compiled by the consuming app's bundler. No pre-compilation step. `package.json` exports point directly to `.tsx` files. Add to `apps/web/next.config.ts transpilePackages` so Next.js processes the raw TSX.

2. **Nav entry wiring is 5-touch** — `NavKey` union (must match `WorkspacePaths` method name), `NavLabelKey` union, locale JSON for both `en` and `zh-Hans`, `workspaceNav` array entry, and `WorkspacePaths` method in `packages/core/paths/paths.ts`. Missing any one of these causes a type error caught by `tsc --noEmit`.

3. **`consistency.test.ts` guards path shape** — when adding a parameterless `WorkspacePaths` method, update both the `Set` check and the `expectedSegments` array in `packages/core/paths/consistency.test.ts`. Multi-segment paths (`hive/epics`) work fine.

4. **Hive API uses the authed Next.js rewrite** — `/api/*` is rewritten to the Go backend via `next.config.ts afterFiles`. Cookie-based session auth flows through automatically; add `credentials: "include"` and `X-Workspace-ID` header on fetch calls to Hive endpoints.

5. **Locale parity test is strict** — both `en` and `zh-Hans` must have identical key sets. Adding a key to one without the other fails `pnpm test` in `packages/views`.

6. **Route adapter pattern** — Next.js page files under `apps/web/app/[workspaceSlug]/(dashboard)/` are thin wrappers: `"use client"` + import from shared package + `<ErrorBoundary>` wrap. No business logic in page files.

7. **`useCurrentWorkspace` in shared packages** — components in `packages/hive/` or `packages/views/` call `useCurrentWorkspace()` from `@multica/core/paths` directly; workspace ID is not passed as props. The slug context is populated by `[workspaceSlug]/layout.tsx` in the web app.
