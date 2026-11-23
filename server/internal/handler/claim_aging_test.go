package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// pantheon-v2#589: ClaimAgentTask ages queued rows, so an older lower-priority
// task eventually beats newer higher-priority ones instead of starving.
func TestClaimAgentTask_PriorityAging(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "claim aging runtime")
	agentID := dbfx.Agent(t, "claim aging agent", runtimeID, testutil.Cols{"max_concurrent_tasks": 4})
	oldIssue := dbfx.Issue(t, "claim aging old medium")
	newIssue := dbfx.Issue(t, "claim aging new high")
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE agent_id = $1", agentID) })

	// A medium task queued 3h ago and a high task queued just now.
	oldMedium := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": oldIssue, "runtime_id": runtimeID, "priority": 2,
		"created_at": testutil.Raw("now() - interval '3 hours'"),
	})
	newHigh := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": newIssue, "runtime_id": runtimeID, "priority": 3,
	})

	claim := func(aging float64) string {
		t.Helper()
		task, err := testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID),
			PrepareLeaseSecs: 60, RuntimeStaleSecs: 120, PriorityAgingPerHour: aging,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Put it back so the next case sees both rows again.
		dbfx.Exec(t, "UPDATE agent_task_queue SET status = 'queued', dispatched_at = NULL, prepare_lease_expires_at = NULL WHERE id = $1", uuidToString(task.ID))
		return uuidToString(task.ID)
	}

	if got := claim(0); got != newHigh {
		t.Fatalf("aging off: claimed %s, want the newer high-priority task %s", got, newHigh)
	}
	if got := claim(1.0); got != oldMedium {
		t.Fatalf("aging 1/h: claimed %s, want the 3h-old medium task %s", got, oldMedium)
	}
	// 0.25/h gives the old task only +0.75: still below the high task.
	if got := claim(0.25); got != newHigh {
		t.Fatalf("aging 0.25/h: claimed %s, want the newer high-priority task %s", got, newHigh)
	}
}
