-- Per-task liveness: GetTaskStatus is already polled every ~5s by the daemon
-- for every in-flight task (see watchTaskCancellation in
-- server/internal/daemon/daemon.go) but that round trip never recorded
-- anything server-side. Stamping it here gives the server (and anything
-- built on top: Hellsing, the UI, an ops dashboard) a real per-task signal
-- distinct from the runtime-level daemon_connection.last_heartbeat_at --
-- a runtime can be online while one specific task on it is hung.
ALTER TABLE agent_task_queue ADD COLUMN last_heartbeat_at TIMESTAMPTZ;

-- Only ever read for status='running' rows (see GetTaskLivenessStale /
-- the query this backs); partial index keeps it small and cheap to update.
CREATE INDEX idx_agent_task_queue_heartbeat
    ON agent_task_queue(last_heartbeat_at)
    WHERE status = 'running';
