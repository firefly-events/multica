"use client";

import { EpicTree } from "@multica/hive";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <EpicTree />
    </ErrorBoundary>
  );
}
