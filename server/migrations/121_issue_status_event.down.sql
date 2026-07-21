DROP TRIGGER IF EXISTS trg_issue_status_event_update ON issue;
DROP TRIGGER IF EXISTS trg_issue_status_event_insert ON issue;
DROP FUNCTION IF EXISTS log_issue_status_event();
DROP TABLE IF EXISTS issue_status_event;
