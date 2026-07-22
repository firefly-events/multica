-- judge_sample_decision records that the judge sampler already considered
-- a completed task, regardless of whether the deterministic sample-rate
-- hash actually selected it for scoring (DOS-860 follow-up).
--
-- Without this, ListUnjudgedCompletedTasks (judge_score.sql) can only
-- exclude tasks that already have a judge_score row. A batch of oldest
-- unscored tasks that all happen to miss the sample-rate hash then never
-- advances: the same fixed-size window is returned on every tick forever,
-- and no task past it is ever considered again. Recording every decision
-- (sampled or not) here lets the candidate query's NOT EXISTS clause move
-- past a task the moment it's been considered once, so the sampler always
-- makes forward progress through the completed-task backlog.
CREATE TABLE judge_sample_decision (
    task_id UUID PRIMARY KEY REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    sampled BOOLEAN NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
