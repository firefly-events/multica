"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { AlertCircle, BookMarked, ChevronDown, ChevronUp } from "lucide-react";
import { useState } from "react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";

interface CatalogSkill {
  name: string;
  description: string;
  version: string;
  when_to_use: string;
}

interface SkillCatalogResponse {
  version: string;
  skills: CatalogSkill[];
}

async function fetchSkillCatalog(wsId: string): Promise<SkillCatalogResponse> {
  const res = await fetch(
    `/api/plugins/hive/skill-catalog?workspace_id=${encodeURIComponent(wsId)}`,
    { credentials: "include" },
  );
  if (!res.ok) throw new Error(`skill-catalog ${res.status}`);
  return res.json();
}

function CatalogSkillRow({ skill }: { skill: CatalogSkill }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div className="rounded-lg border bg-card">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-accent/40 transition-colors rounded-lg"
      >
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{skill.name}</span>
            <Badge variant="secondary" className="shrink-0 text-[10px]">
              v{skill.version}
            </Badge>
          </div>
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
            {skill.description}
          </p>
        </div>
        {expanded ? (
          <ChevronUp className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronDown className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        )}
      </button>

      {expanded && (
        <div className="border-t bg-muted/30 px-4 py-2.5">
          <p className="text-xs text-muted-foreground">
            <span className="font-medium text-foreground">When to use: </span>
            {skill.when_to_use}
          </p>
        </div>
      )}
    </div>
  );
}

export function SkillCatalogPanel() {
  const wsId = useWorkspaceId();

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ["hive", "skill-catalog", wsId],
    queryFn: () => fetchSkillCatalog(wsId),
    enabled: !!wsId,
    staleTime: 5 * 60 * 1000,
  });

  return (
    <div className="rounded-lg border bg-background">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <BookMarked className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Hive Plugin Catalog</span>
        {data && (
          <Badge variant="outline" className="ml-1 text-[10px]">
            v{data.version}
          </Badge>
        )}
        {data && (
          <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
            {data.skills.length}
          </span>
        )}
        <p className="ml-2 hidden text-xs text-muted-foreground md:block">
          Packaged skills available in this workspace · browse-only
        </p>
      </div>

      <div className="p-3 space-y-2">
        {isLoading &&
          Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full rounded-lg" />
          ))}

        {error && (
          <div className="flex items-center gap-2 rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
            <AlertCircle className="h-3.5 w-3.5 shrink-0" />
            <span>Failed to load Hive skill catalog.</span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="ml-auto h-6 text-xs"
              onClick={() => refetch()}
            >
              Retry
            </Button>
          </div>
        )}

        {data?.skills.map((skill) => (
          <CatalogSkillRow key={skill.name} skill={skill} />
        ))}
      </div>
    </div>
  );
}
