# Command Center prototype (archived 2026-09-21)

## What this was

A client-only dashboard (`packages/views/command/command-center-page.tsx`,
route `/[workspaceSlug]/command`) that polled and SSE-streamed from
`http://localhost:3005` — a personal, machine-local dev tool (`~/Code/Claud-ometer`
on the operator's own machine, invoked via `npm run dev`). It was never a real
Multica feature: the base URL, the "start it with `cd ~/Code/Claud-ometer`"
error copy, and the `@claud-ometer/envelope` type import all point at
developer-machine-only tooling with zero portable resolution (broke `apps/desktop`
tests and, most likely, `apps/desktop`'s real production build in CI since the
feature was first committed in 2026-07).

Its only real purpose was to **visually replicate** a local ops/usage/agents/clients
monitoring surface so it was clear what needed to be ported into Multica for real.
It was never meant to ship. Removed entirely from the fork (2026-09-21) rather than
patched, per explicit operator direction — do not resurrect it as-is or re-add the
`@claud-ometer/envelope` dependency.

## The real shape it prototyped (for whoever builds the real version)

The envelope it consumed had four sections, each independently health-tagged
(`{ ok: boolean, stub: boolean }` per source):

- **`ops`** — `stuck` (list of hung agents by name), `zombies` (worker counts per
  provider: opencode / claude / daemon cap), `burn` (total tokens + cost today),
  `errors` (recent error log lines).
- **`usage`** — Claude weekly-subscription % and session-window %, next reset
  timestamp; Gemini RPD used/limit; today's input/output/total tokens + cost.
- **`agents`** — a graph (`nodes`/`edges`) of agent state, each node with
  `id`/`label`/`kind`/`status` (`working`/`in_progress`/etc.).
- **`clients`** — a list of client engagements: `name`/`status`/`stage`/`stageName`/
  `blocked`.

Transport was dual: SSE (`/api/command/stream`, event name `envelope`) as the
primary live channel, with a 15s poll (`/api/command`) as a fallback when SSE
drops — `live` vs `polling` was surfaced in the header.

## Backlog

The real version of this needs a real backend inside Multica's own architecture
(no dependency on any developer's local machine or any sibling repo) — most
likely composed from data Multica's own server already tracks (runtime/agent
status, token-probe/usage per DOS-1037, task queue state) rather than a bespoke
"envelope" endpoint. File this as a real Multica ticket once tracker access is
available; this doc is the reference for what it should show, not a spec to
implement verbatim.
