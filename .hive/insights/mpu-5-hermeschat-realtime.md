# mpu-5 HermesChat Realtime — Insights

## Reusing /ws without a second socket stack

The platform's `WSProvider` (packages/core/realtime/provider.tsx) manages a single WS connection per workspace. Backend events broadcast via `hub.BroadcastToWorkspace(workspaceID, msg)` arrive at all clients subscribed to that workspace scope — which happens automatically on connect. The frontend never needs to open a second socket; it calls `useWSEvent(eventType, handler)` to subscribe to any typed event.

**Pattern:** Add the new event type to `WSEventType` union in `packages/core/types/events.ts`, then broadcast `{"type":"<event>","payload":{...}}` from Go. The existing socket delivers it.

## Extending the hive Router to accept a Broadcaster

`hive.Router` originally took only `*Store`. Adding `realtime.Broadcaster` as a second param is the right seam — the hub is already present in `NewRouterWithOptions` and passes cleanly. Use `if hub != nil { hub.BroadcastToWorkspace(...) }` for defensive nil-safe publishing.

## useInfiniteQuery with cursor pagination (TanStack Query v5)

Use `initialPageParam: undefined as string | undefined` rather than `undefined` alone or explicit type params — the latter leads to `Object is possibly undefined` errors because TS can't narrow array index access. The cleanest approach: omit explicit generic type params, let inference run, cast the queryFn return type with `as Promise<T>`.

Pages arrive newest-first (DESC). Display oldest-first with:
```ts
[...pages].reverse().flatMap(page => [...(page ?? [])].reverse())
```
`?? []` guards against the TS strict-mode index-access-undefined edge case on page items.

## WS cache append for new messages

On `hive:message:created`, prepend the new message to `pages[0]` (newest page) in the InfiniteData cache. After the page-reversal above, this correctly places it at the bottom of the rendered list.

```ts
queryClient.setQueryData(key, (old) => ({
  ...old,
  pages: [[newMsg, ...(old.pages[0] ?? [])], ...old.pages.slice(1)],
}));
```

## Go ListMessages: avoid interface tricks

The dual-branch query (with/without `before` cursor) is cleanest with two separate `pool.Query` calls in an if/else. Assigning `pgxpool.Rows` to a local variable and branching avoids Go's interface generics — just assign to the concrete `pgxpool.Rows` type.

Actually: assign to `var rows pgxpool.Rows` then branch — this is idiomatic.
