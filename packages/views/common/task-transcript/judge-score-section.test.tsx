// @vitest-environment jsdom

import { cleanup, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { JudgeScore } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mockState = vi.hoisted(() => ({
  listJudgeScores: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listJudgeScores: mockState.listJudgeScores,
  },
}));

import { JudgeScoreSection } from "./judge-score-section";

function makeScore(overrides: Partial<JudgeScore> = {}): JudgeScore {
  return {
    id: "score-1",
    task_id: "task-1",
    judge_provider: "anthropic",
    judge_model: "claude-sonnet-5",
    correctness_score: 90,
    adherence_score: 85,
    tone_score: 80,
    clarity_score: 88,
    trajectory_score: 92,
    overall_score: 87,
    rationale: "Followed the plan and produced correct output.",
    trajectory_rationale: "Efficient path to the fix.",
    calibration_status: "MODELED",
    input_tokens: 1000,
    output_tokens: 200,
    cost_usd: "0.05",
    created_at: "2026-06-08T08:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("JudgeScoreSection", () => {
  it("skips the fetch and renders nothing when the task is not completed", () => {
    const { container } = renderWithI18n(
      <JudgeScoreSection taskId="task-1" taskStatus="running" />,
    );

    expect(mockState.listJudgeScores).not.toHaveBeenCalled();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when the task was never sampled (empty result)", async () => {
    mockState.listJudgeScores.mockResolvedValue([]);

    const { container } = renderWithI18n(
      <JudgeScoreSection taskId="task-1" taskStatus="completed" />,
    );

    await waitFor(() => {
      expect(mockState.listJudgeScores).toHaveBeenCalledWith("task-1");
    });
    expect(container).toBeEmptyDOMElement();
  });

  it("treats an API rejection as no scores rather than throwing", async () => {
    mockState.listJudgeScores.mockRejectedValue(new Error("network error"));

    const { container } = renderWithI18n(
      <JudgeScoreSection taskId="task-1" taskStatus="completed" />,
    );

    await waitFor(() => {
      expect(mockState.listJudgeScores).toHaveBeenCalledWith("task-1");
    });
    expect(container).toBeEmptyDOMElement();
  });

  it("does not update state after unmount once the fetch resolves", async () => {
    let resolveFetch: (scores: JudgeScore[]) => void = () => {};
    mockState.listJudgeScores.mockReturnValue(
      new Promise<JudgeScore[]>((resolve) => {
        resolveFetch = resolve;
      }),
    );

    const { unmount } = renderWithI18n(
      <JudgeScoreSection taskId="task-1" taskStatus="completed" />,
    );

    unmount();
    resolveFetch([makeScore()]);

    // No assertion target beyond "no error/warning thrown" — React would
    // log a state-update-on-unmounted-component warning if the cancelled
    // guard were missing, and this promise settling after unmount is the
    // regression scenario itself.
    await Promise.resolve();
  });

  it("renders the newest score, MODELED badge, rubric numbers, and rationale on the happy path", async () => {
    mockState.listJudgeScores.mockResolvedValue([
      makeScore({ id: "newest" }),
      makeScore({ id: "older", overall_score: 50 }),
    ]);

    renderWithI18n(<JudgeScoreSection taskId="task-1" taskStatus="completed" />);

    await waitFor(() => {
      expect(screen.getByText("Overall 87/100")).toBeInTheDocument();
    });

    expect(screen.getByText("MODELED")).toBeInTheDocument();
    expect(
      screen.getByTitle(
        "Modeled score, not yet calibrated against human review",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("90")).toBeInTheDocument();
    expect(screen.getByText("85")).toBeInTheDocument();
    expect(screen.getByText("80")).toBeInTheDocument();
    expect(screen.getByText("88")).toBeInTheDocument();
    expect(screen.getByText("92")).toBeInTheDocument();
    expect(
      screen.getByText("Followed the plan and produced correct output."),
    ).toBeInTheDocument();
  });

  it("omits the rationale paragraph when rationale is an empty string", async () => {
    mockState.listJudgeScores.mockResolvedValue([makeScore({ rationale: "" })]);

    renderWithI18n(<JudgeScoreSection taskId="task-1" taskStatus="completed" />);

    await waitFor(() => {
      expect(screen.getByText("Overall 87/100")).toBeInTheDocument();
    });

    expect(
      screen.queryByText("Followed the plan and produced correct output."),
    ).not.toBeInTheDocument();
    expect(document.querySelectorAll("p")).toHaveLength(0);
  });
});
