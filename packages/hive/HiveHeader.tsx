"use client";

import React from "react";
import { SidebarTrigger, useSidebar } from "@multica/ui/components/ui/sidebar";

// The dashboard sidebar collapses off-canvas at narrow widths and is reopened
// via a SidebarTrigger that core pages render in their header. The Hive views
// rendered bare content with no header, so once the sidebar collapsed there was
// no way to bring it back. This header mirrors @multica/views PageHeader but
// lives in-package to avoid a circular dependency on @multica/views.
function MobileSidebarTrigger() {
  try {
    useSidebar();
  } catch {
    return null;
  }
  return <SidebarTrigger className="mr-2 md:hidden" />;
}

export function HiveHeader({
  title,
  right,
}: {
  title: string;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex h-12 shrink-0 items-center border-b px-4">
      <MobileSidebarTrigger />
      <h1 className="text-sm font-semibold">{title}</h1>
      {right && <div className="ml-auto">{right}</div>}
    </div>
  );
}
