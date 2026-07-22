"use client";

import { useEffect, useState } from "react";
import { Sparkles } from "lucide-react";
import { api } from "@multica/core/api";
import type { JudgeScore } from "@multica/core/types";
import { useT } from "../../i18n";

interface JudgeScoreSectionProps {
  taskId: string;
  /** Only completed tasks are ever sampled by the judge — skip the fetch otherwise. */
  taskStatus: string;
}

/**
 * Renders the sampled LLM-as-judge score for this task, if one exists
 * (DOS-860). Most tasks were never sampled — the section renders nothing
 * in that case rather than a misleading "no score" state, since absence
 * here is the overwhelmingly common case (only a configured % of
 * completed tasks are ever judged) and not evidence of a problem.
 *
 * Every score rendered here is labeled MODELED: these numbers are not yet
 * validated against human review, and must not be presented as ground
 * truth until the calibration follow-up story lands.
 */
export function JudgeScoreSection({ taskId, taskStatus }: JudgeScoreSectionProps) {
  const { t } = useT("agents");
  const [scores, setScores] = useState<JudgeScore[] | null>(null);

  useEffect(() => {
    if (taskStatus !== "completed") return;
    let cancelled = false;
    api
      .listJudgeScores(taskId)
      .then((result) => {
        if (!cancelled) setScores(result);
      })
      .catch(() => {
        if (!cancelled) setScores([]);
      });
    return () => {
      cancelled = true;
    };
  }, [taskId, taskStatus]);

  if (!scores || scores.length === 0) return null;

  // A task can be scored by more than one judge model over time; show the
  // most recent pass (GetJudgeScoresByTask orders newest-first).
  const score = scores[0];
  if (!score) return null;

  return (
    <div className="border-b px-4 py-3 shrink-0 space-y-2">
      <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
        <Sparkles className="h-3.5 w-3.5" />
        {t(($) => $.transcript.judge_score.title)}
        <span
          className="inline-flex items-center rounded-full bg-amber-500/15 px-2 py-0.5 text-[10px] font-semibold text-amber-800 dark:text-amber-400"
          title={t(($) => $.transcript.judge_score.modeled_tooltip)}
        >
          {score.calibration_status}
        </span>
        <span className="ml-auto text-foreground font-semibold">
          {t(($) => $.transcript.judge_score.overall, { score: score.overall_score })}
        </span>
      </div>

      <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span>
          {t(($) => $.transcript.judge_score.correctness)}:{" "}
          <span className="font-medium text-foreground">{score.correctness_score}</span>
        </span>
        <span>
          {t(($) => $.transcript.judge_score.adherence)}:{" "}
          <span className="font-medium text-foreground">{score.adherence_score}</span>
        </span>
        <span>
          {t(($) => $.transcript.judge_score.tone)}:{" "}
          <span className="font-medium text-foreground">{score.tone_score}</span>
        </span>
        <span>
          {t(($) => $.transcript.judge_score.clarity)}:{" "}
          <span className="font-medium text-foreground">{score.clarity_score}</span>
        </span>
        <span>
          {t(($) => $.transcript.judge_score.trajectory)}:{" "}
          <span className="font-medium text-foreground">{score.trajectory_score}</span>
        </span>
      </div>

      {score.rationale && (
        <p className="text-[11px] text-muted-foreground">{score.rationale}</p>
      )}
    </div>
  );
}
