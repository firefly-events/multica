"use client";

import { HermesChat } from "@multica/hive";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <HermesChat />
    </ErrorBoundary>
  );
}
