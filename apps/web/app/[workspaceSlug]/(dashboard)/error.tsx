"use client";

import { Button } from "@multica/ui/components/ui/button";

// Segment-level error boundary for the dashboard route group. Without it, an
// error thrown by any child route (including the Hive plugin screens) bubbles
// past the dashboard layout to the root, which drops the sidebar entirely.
// Rendering the fallback here keeps it inside the dashboard layout so the
// sidebar and navigation remain functional.
export default function DashboardError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <div className="flex flex-col items-start gap-3 p-6">
      <h1 className="text-lg font-semibold">Something went wrong</h1>
      <p className="text-sm text-muted-foreground">
        {error.message || "This screen failed to render."}
      </p>
      <Button variant="outline" size="sm" onClick={reset}>
        Try again
      </Button>
    </div>
  );
}
