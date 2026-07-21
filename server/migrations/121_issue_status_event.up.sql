-- Issue status transition log (DOS-747 metrics foundation).
--
-- The `issue` table only ever stores the CURRENT status — there is no
-- history of when an issue moved todo -> in_progress -> in_review -> done.
-- That makes burndown, cycle time, review-queue age, and stale-in_progress
-- detection impossible to compute without an external audit trail. This
-- migration adds that trail as an append-only event table, populated by a
-- trigger so every status-changing code path (handler, autopilot, task
-- completion, Lark integration, ...) is covered without having to hunt
-- down and instrument each call site individually.
--
-- Only forward from here: existing issues have no recorded history before
-- this migration runs, so early burndown windows will look like every
-- open issue "started" on the day this shipped. That's an accepted gap,
-- not a bug — there is no legitimate source to backfill from.
CREATE TABLE issue_status_event (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id     UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    from_status  TEXT
        CHECK (from_status IS NULL OR from_status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled')),
    to_status    TEXT NOT NULL
        CHECK (to_status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled')),
    changed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Burndown / throughput queries bucket by day and filter by workspace + status.
CREATE INDEX idx_issue_status_event_workspace_changed_at
    ON issue_status_event (workspace_id, changed_at);

-- Cycle-time / per-issue history lookups.
CREATE INDEX idx_issue_status_event_issue_id
    ON issue_status_event (issue_id, changed_at);

CREATE OR REPLACE FUNCTION log_issue_status_event()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO issue_status_event (issue_id, workspace_id, from_status, to_status, changed_at)
        VALUES (NEW.id, NEW.workspace_id, NULL, NEW.status, NEW.created_at);
        RETURN NEW;
    ELSIF TG_OP = 'UPDATE' AND OLD.status IS DISTINCT FROM NEW.status THEN
        INSERT INTO issue_status_event (issue_id, workspace_id, from_status, to_status, changed_at)
        VALUES (NEW.id, NEW.workspace_id, OLD.status, NEW.status, now());
        RETURN NEW;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_issue_status_event_insert
AFTER INSERT ON issue
FOR EACH ROW EXECUTE FUNCTION log_issue_status_event();

CREATE TRIGGER trg_issue_status_event_update
AFTER UPDATE OF status ON issue
FOR EACH ROW EXECUTE FUNCTION log_issue_status_event();
