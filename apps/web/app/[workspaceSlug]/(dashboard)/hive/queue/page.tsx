"use client";

import { PersonalQueue } from "@multica/hive";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <PersonalQueue />
    </ErrorBoundary>
  );
}
