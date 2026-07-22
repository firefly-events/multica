package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/judge"
	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameJudgeScoreSampler is the canonical name used in audit rows.
// Stable across releases — do not rename without a migration.
const JobNameJudgeScoreSampler = "judge_score_sampler"

// JudgeScoreSamplerConfig controls how much of the completed-task
// stream the sampler actually grades. SampleRate is a fraction in
// [0,1]; BatchLimit bounds how many candidate tasks a single tick
// considers, so a large backlog can't turn one run into an unbounded
// LLM-cost spike.
type JudgeScoreSamplerConfig struct {
	SampleRate float64
	BatchLimit int32
}

// DefaultJudgeScoreSamplerConfig matches the DOS-860 "sample rather
// than score-everything to bound cost" requirement: 5% of completed
// tasks, at most 50 considered per tick.
func DefaultJudgeScoreSamplerConfig() JudgeScoreSamplerConfig {
	return JudgeScoreSamplerConfig{SampleRate: 0.05, BatchLimit: 50}
}

// JudgeScoreSamplerJob returns the JobSpec that drives the sampled
// LLM-as-judge scoring pass (DOS-860). It reuses the DB-backed
// execution scheduler (rather than a plain ticker, i.e. "no box cron")
// so runs are leased/audited/retried the same way as every other
// internal periodic job, and so the judge's own cost is visible in
// sys_cron_executions alongside the judge_score rows it writes.
func JudgeScoreSamplerJob(pool *pgxpool.Pool, j judge.Judge, cfg JudgeScoreSamplerConfig) JobSpec {
	return JobSpec{
		Name:              JobNameJudgeScoreSampler,
		Cadence:           15 * time.Minute,
		ScheduleDelay:     5 * time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        20 * time.Minute,
		StaleTimeout:      30 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff: []time.Duration{
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
		},
		Scopes:  StaticScopes(ScopeGlobal),
		Handler: makeJudgeScoreSamplerHandler(pool, j, cfg),
	}
}

// makeJudgeScoreSamplerHandler pulls unjudged completed tasks, applies
// the deterministic sample-rate gate per candidate, and scores the
// ones that land inside the sample. Judge and DB failures on one task
// are logged into the result but do not fail the whole run — a single
// bad trajectory (e.g. a task with a malformed result blob) shouldn't
// block scoring the rest of the batch.
func makeJudgeScoreSamplerHandler(pool *pgxpool.Pool, j judge.Judge, cfg JudgeScoreSamplerConfig) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		q := db.New(pool)

		// Use the database's own clock rather than the process's wall
		// clock: the candidate pool includes tasks that may have
		// completed milliseconds ago, and any drift between the app
		// host clock and the DB clock could otherwise exclude a
		// just-completed row from its own eligible window.
		now, err := dbNow(ctx, pool)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("read db now: %w", err)
		}

		candidates, err := q.ListUnjudgedCompletedTasks(ctx, db.ListUnjudgedCompletedTasksParams{
			Before:     pgtype.Timestamptz{Time: now, Valid: true},
			LimitCount: cfg.BatchLimit,
		})
		if err != nil {
			return HandlerResult{}, fmt.Errorf("list unjudged completed tasks: %w", err)
		}

		var scored, sampled, failed int64
		for _, task := range candidates {
			taskID := task.ID.String()
			decision := judge.ShouldSample(taskID, cfg.SampleRate)

			// Record that this task was considered regardless of the
			// sample outcome. ListUnjudgedCompletedTasks excludes on
			// judge_sample_decision, not judge_score, so this is what
			// lets the candidate window advance past a task that missed
			// the sample-rate hash instead of returning it forever.
			if err := q.InsertJudgeSampleDecision(ctx, db.InsertJudgeSampleDecisionParams{
				TaskID:  task.ID,
				Sampled: decision,
			}); err != nil {
				failed++
				continue
			}

			if !decision {
				continue
			}
			sampled++

			if in.Heartbeat != nil {
				_ = in.Heartbeat(ctx)
			}

			trajectory, err := judge.BuildTrajectory(ctx, q, task)
			if err != nil {
				failed++
				continue
			}

			result, err := j.Score(ctx, judge.Input{Trajectory: trajectory})
			if err != nil {
				failed++
				continue
			}

			costUSD := judgeCallCostUSD(result)

			var costNumeric pgtype.Numeric
			if err := costNumeric.Scan(fmt.Sprintf("%.6f", costUSD)); err != nil {
				failed++
				continue
			}

			if _, err := q.InsertJudgeScore(ctx, db.InsertJudgeScoreParams{
				TaskID:              task.ID,
				JudgeProvider:       result.Provider,
				JudgeModel:          result.Model,
				CorrectnessScore:    int32(result.CorrectnessScore),
				AdherenceScore:      int32(result.AdherenceScore),
				ToneScore:           int32(result.ToneScore),
				ClarityScore:        int32(result.ClarityScore),
				TrajectoryScore:     int32(result.TrajectoryScore),
				OverallScore:        int32(result.OverallScore),
				Rationale:           result.Rationale,
				TrajectoryRationale: result.TrajectoryRationale,
				CalibrationStatus:   judge.ModeledStatus,
				InputTokens:         result.InputTokens,
				OutputTokens:        result.OutputTokens,
				CostUsd:             costNumeric,
			}); err != nil {
				failed++
				continue
			}
			scored++
		}

		return HandlerResult{
			RowsAffected: scored,
			Result: map[string]any{
				"candidates":  len(candidates),
				"sampled":     sampled,
				"scored":      scored,
				"failed":      failed,
				"sample_rate": cfg.SampleRate,
			},
		}, nil
	}
}

// judgeCallCostUSD prices the judge's own call using the same model
// price table the rest of the platform uses for task_usage, so the
// judge's cost is directly comparable to (and included in) normal
// per-task spend — satisfying the "judge's own token cost/execution is
// logged so the judge is itself observable and its cost bounded"
// acceptance criterion.
func judgeCallCostUSD(r judge.Result) float64 {
	price, ok := metrics.PriceForModelAlias(r.Model)
	if !ok {
		return 0
	}
	input := float64(r.InputTokens) * price.InputPerM / 1_000_000
	output := float64(r.OutputTokens) * price.OutputPerM / 1_000_000
	return input + output
}
