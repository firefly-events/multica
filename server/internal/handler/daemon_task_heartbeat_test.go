package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DOS-1042: GetTaskStatus already gets polled every ~5s for every in-flight
// daemon task (watchTaskCancellation on the daemon side) -- these tests pin
// the server-side half of the per-task liveness signal that piggybacks on
// that existing poll, without any new daemon-side call.

// TestGetTaskStatus_StampsHeartbeatWhileRunning covers the primary case: a
// running task's poll advances last_heartbeat_at.
func TestGetTaskStatus_StampsHeartbeatWhileRunning(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "DOS-1042 heartbeat runtime")
	agentID := dbfx.Agent(t, "DOS-1042 heartbeat agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "running", "started_at": testutil.Raw("now()"),
	})

	var before *time.Time
	dbfx.QueryRow(t, `SELECT last_heartbeat_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&before)
	if before != nil {
		t.Fatalf("last_heartbeat_at = %v before any poll, want nil", before)
	}

	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/tasks/"+taskID+"/status", nil, testWorkspaceID, "test-daemon")
	req = withURLParam(req, "taskId", taskID)
	testutil.Call(t, testHandler.GetTaskStatus, req).Want(http.StatusOK)

	var after *time.Time
	dbfx.QueryRow(t, `SELECT last_heartbeat_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&after)
	if after == nil {
		t.Fatal("last_heartbeat_at is still nil after a status poll on a running task")
	}
	if time.Since(*after) > 10*time.Second {
		t.Fatalf("last_heartbeat_at = %v, too old to have just been stamped", after)
	}
}

// TestGetTaskStatus_DoesNotStampHeartbeatWhenNotRunning covers the exclusion:
// a poll against a task that already reached a terminal state must not
// fabricate a heartbeat for it (the query's own WHERE status='running' is
// the real guard; this pins the handler-level skip too, since a terminal
// task's poll response is what makes the daemon interrupt the agent in the
// first place -- it should not also look "freshly alive" server-side).
func TestGetTaskStatus_DoesNotStampHeartbeatWhenNotRunning(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "DOS-1042 terminal runtime")
	agentID := dbfx.Agent(t, "DOS-1042 terminal agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "completed", "completed_at": testutil.Raw("now()"),
	})

	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/tasks/"+taskID+"/status", nil, testWorkspaceID, "test-daemon")
	req = withURLParam(req, "taskId", taskID)
	var response map[string]string
	testutil.Call(t, testHandler.GetTaskStatus, req).Want(http.StatusOK).JSON(&response)
	if response["status"] != "completed" {
		t.Fatalf("status = %q, want completed", response["status"])
	}

	var heartbeat *time.Time
	dbfx.QueryRow(t, `SELECT last_heartbeat_at FROM agent_task_queue WHERE id = $1`, taskID).Scan(&heartbeat)
	if heartbeat != nil {
		t.Fatalf("last_heartbeat_at = %v for a completed task, want nil", heartbeat)
	}
}

// TestTaskToResponse_SurfacesLastHeartbeatAt pins that any consumer of the
// normal task response shape (ListAgentTasks, task detail, etc.) gets the
// liveness signal for free, without a bespoke staleness endpoint.
func TestTaskToResponse_SurfacesLastHeartbeatAt(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "DOS-1042 response runtime")
	agentID := dbfx.Agent(t, "DOS-1042 response agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "running", "started_at": testutil.Raw("now()"),
	})
	dbfx.Exec(t, `UPDATE agent_task_queue SET last_heartbeat_at = now() WHERE id = $1`, taskID)

	task, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	resp := taskToResponse(task, testWorkspaceID)
	if resp.LastHeartbeatAt == nil {
		t.Fatal("taskToResponse dropped last_heartbeat_at")
	}
}
