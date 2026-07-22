-- name: InsertJudgeScore :one
INSERT INTO judge_score (
    task_id, judge_provider, judge_model,
    correctness_score, adherence_score, tone_score, clarity_score, trajectory_score, overall_score,
    rationale, trajectory_rationale, calibration_status,
    input_tokens, output_tokens, cost_usd
) VALUES (
    $1, $2, $3,
    $4, $5, $6, $7, $8, $9,
    $10, $11, $12,
    $13, $14, $15
)
ON CONFLICT (task_id, judge_provider, judge_model)
DO UPDATE SET
    correctness_score = EXCLUDED.correctness_score,
    adherence_score = EXCLUDED.adherence_score,
    tone_score = EXCLUDED.tone_score,
    clarity_score = EXCLUDED.clarity_score,
    trajectory_score = EXCLUDED.trajectory_score,
    overall_score = EXCLUDED.overall_score,
    rationale = EXCLUDED.rationale,
    trajectory_rationale = EXCLUDED.trajectory_rationale,
    calibration_status = EXCLUDED.calibration_status,
    input_tokens = EXCLUDED.input_tokens,
    output_tokens = EXCLUDED.output_tokens,
    cost_usd = EXCLUDED.cost_usd
RETURNING *;

-- name: GetJudgeScoresByTask :many
SELECT * FROM judge_score
WHERE task_id = $1
ORDER BY created_at DESC;

-- name: TaskHasJudgeScore :one
SELECT EXISTS (
    SELECT 1 FROM judge_score WHERE task_id = $1
);

-- name: ListUnjudgedCompletedTasks :many
-- Candidate pool for the judge sampler: completed tasks not yet
-- *considered* by the sampler, oldest-completed-first so a slow sampler
-- still makes forward progress across restarts instead of re-considering
-- the same recent tail. Excluding on judge_sample_decision (not just
-- judge_score) is what lets the window advance past tasks that were
-- considered but missed the sample-rate hash — see
-- 122_judge_sample_decision.up.sql.
SELECT atq.*
FROM agent_task_queue atq
WHERE atq.status = 'completed'
  AND atq.completed_at IS NOT NULL
  AND atq.completed_at <= @before::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM judge_sample_decision jsd WHERE jsd.task_id = atq.id
  )
ORDER BY atq.completed_at ASC
LIMIT @limit_count;

-- name: InsertJudgeSampleDecision :exec
-- Records that the sampler considered task_id this tick, whether or not
-- it landed inside the sample-rate hash. Idempotent: a task is only ever
-- considered once, so a conflict here means concurrent sampler runs raced
-- on the same candidate and the loser's decision is discarded.
INSERT INTO judge_sample_decision (task_id, sampled)
VALUES ($1, $2)
ON CONFLICT (task_id) DO NOTHING;
