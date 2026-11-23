package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// pantheon-v2#589: when an issue leaves in_review or changes assignee, the
// previous assignee's queued runs are cancelled. Running work and other
// agents' tasks on the issue are not.

type staleQueueFixture struct {
	issueID   string
	runtimeID string
	holder    string // the agent holding the issue
	other     string // an unrelated agent with its own task on the issue
}

func newStaleQueueFixture(t *testing.T, name, status string) staleQueueFixture {
	t.Helper()
	runtimeID := dbfx.Runtime(t, name+" runtime")
	holder := dbfx.Agent(t, name+" holder", runtimeID)
	other := dbfx.Agent(t, name+" other", runtimeID)
	issueID := dbfx.Issue(t, name+" issue", testutil.Cols{
		"status": status, "assignee_type": "agent", "assignee_id": holder,
	})
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE issue_id = $1", issueID) })
	return staleQueueFixture{issueID: issueID, runtimeID: runtimeID, holder: holder, other: other}
}

func (f staleQueueFixture) task(t *testing.T, agentID, status string) string {
	t.Helper()
	cols := testutil.Cols{"issue_id": f.issueID, "runtime_id": f.runtimeID, "status": status}
	if status == "running" {
		cols["dispatched_at"] = testutil.Raw("now()")
		cols["started_at"] = testutil.Raw("now()")
	}
	return dbfx.Task(t, agentID, cols)
}

func (f staleQueueFixture) update(t *testing.T, body map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+f.issueID, body), "id", f.issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update %v: status %d: %s", body, w.Code, w.Body.String())
	}
}

func wantTaskStatus(t *testing.T, label, taskID, want string) {
	t.Helper()
	if got := taskStatus(t, taskID); got != want {
		t.Fatalf("%s: status %q, want %q", label, got, want)
	}
}

func TestStaleQueue_LeavingInReviewCancelsHoldersQueuedRuns(t *testing.T) {
	f := newStaleQueueFixture(t, "stale queue leave review", "in_review")
	queued := f.task(t, f.holder, "queued")
	otherQueued := f.task(t, f.other, "queued")

	f.update(t, map[string]any{"status": "blocked"})

	wantTaskStatus(t, "holder's queued review run", queued, "cancelled")
	wantTaskStatus(t, "other agent's queued run", otherQueued, "queued")
}

func TestStaleQueue_LeavingInReviewKeepsRunningWork(t *testing.T) {
	f := newStaleQueueFixture(t, "stale queue keep running", "in_review")
	running := f.task(t, f.holder, "running")
	deferred := f.task(t, f.holder, "deferred")

	f.update(t, map[string]any{"status": "blocked"})

	wantTaskStatus(t, "holder's running run", running, "running")
	wantTaskStatus(t, "holder's deferred run", deferred, "cancelled")
}

func TestStaleQueue_ReassignCancelsPreviousAssigneesQueuedRuns(t *testing.T) {
	f := newStaleQueueFixture(t, "stale queue reassign", "in_progress")
	queued := f.task(t, f.holder, "queued")
	newAssignee := dbfx.Agent(t, "stale queue reassign new", f.runtimeID)

	f.update(t, map[string]any{"assignee_type": "agent", "assignee_id": newAssignee})

	wantTaskStatus(t, "previous assignee's queued run", queued, "cancelled")
	tasks, err := testHandler.Queries.ListTasksByIssue(context.Background(), parseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if uuidToString(task.AgentID) == newAssignee && task.Status == "cancelled" {
			t.Fatal("the new assignee's run was cancelled")
		}
	}
}

func TestStaleQueue_OtherTransitionsKeepQueuedRuns(t *testing.T) {
	f := newStaleQueueFixture(t, "stale queue other transition", "todo")
	queued := f.task(t, f.holder, "queued")

	f.update(t, map[string]any{"status": "in_progress"})
	wantTaskStatus(t, "todo -> in_progress", queued, "queued")

	f.update(t, map[string]any{"status": "in_review"})
	wantTaskStatus(t, "in_progress -> in_review", queued, "queued")

	f.update(t, map[string]any{"title": "renamed while in review"})
	wantTaskStatus(t, "title edit in review", queued, "queued")
}
