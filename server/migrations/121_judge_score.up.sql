-- judge_score holds sampled LLM-as-judge scoring passes over completed
-- agent_task_queue rows (DOS-860). One row per (task, judge run).
--
-- Rubric dimensions are broken into their own INT columns (rather than a
-- single JSONB scores blob) so they can be aggregated/filtered directly in
-- SQL for the ramp-test quality gate and future router training queries.
--
-- calibration_status defaults to MODELED: these numbers are not yet
-- validated against human review and must be labeled as such in the UI
-- until the calibration follow-up story recalibrates them.
CREATE TABLE judge_score (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,

    judge_provider TEXT NOT NULL,
    judge_model TEXT NOT NULL,

    correctness_score INT NOT NULL CHECK (correctness_score BETWEEN 0 AND 100),
    adherence_score INT NOT NULL CHECK (adherence_score BETWEEN 0 AND 100),
    tone_score INT NOT NULL CHECK (tone_score BETWEEN 0 AND 100),
    clarity_score INT NOT NULL CHECK (clarity_score BETWEEN 0 AND 100),
    trajectory_score INT NOT NULL CHECK (trajectory_score BETWEEN 0 AND 100),
    overall_score INT NOT NULL CHECK (overall_score BETWEEN 0 AND 100),

    rationale TEXT NOT NULL DEFAULT '',
    trajectory_rationale TEXT NOT NULL DEFAULT '',

    calibration_status TEXT NOT NULL DEFAULT 'MODELED'
        CHECK (calibration_status IN ('MODELED', 'CALIBRATED')),

    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (task_id, judge_provider, judge_model)
);

CREATE INDEX idx_judge_score_task_id ON judge_score(task_id);
CREATE INDEX idx_judge_score_created_at ON judge_score(created_at);
