package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PANT-1442: a reassign and the instruction comment that goes with it are one
// handoff. Within the coalescing window they must start ONE run, in either
// order, and the surviving run's trigger_summary must say it absorbed the other.

type coalesceFixture struct {
	ctx     context.Context
	issueID string
	agentID string
	agent   db.Agent
}

func newCoalesceFixture(t *testing.T, name string) coalesceFixture {
	t.Helper()
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, name+" runtime")
	agentID := dbfx.Agent(t, name+" agent", runtimeID)
	issueID := dbfx.Issue(t, name+" issue")
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE issue_id = $1", issueID) })
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	return coalesceFixture{ctx: ctx, issueID: issueID, agentID: agentID, agent: agent}
}

func (f coalesceFixture) assign(t *testing.T) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+f.issueID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   f.agentID,
	}), "id", f.issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("assign: status %d: %s", w.Code, w.Body.String())
	}
}

func (f coalesceFixture) comment(t *testing.T, body string, source commentAgentTriggerSource, want DispatchStatus, over ...testutil.Cols) string {
	t.Helper()
	commentID := dbfx.Comment(t, f.issueID, body, over...)
	issue, err := testHandler.Queries.GetIssue(f.ctx, parseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	result := testHandler.enqueueCommentAgentTriggers(f.ctx, issue, parseUUID(commentID),
		[]commentAgentTrigger{{Agent: f.agent, Source: source}})
	if got := result[f.agentID].status; got != want {
		t.Fatalf("comment trigger: got %+v, want %s", result[f.agentID], want)
	}
	return commentID
}

func (f coalesceFixture) tasks(t *testing.T) []db.AgentTaskQueue {
	t.Helper()
	tasks, err := testHandler.Queries.ListTasksByIssue(f.ctx, parseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestRunTriggerCoalesce_ReassignThenComment(t *testing.T) {
	f := newCoalesceFixture(t, "coalesce reassign-then-comment")
	f.assign(t)
	if n := len(f.tasks(t)); n != 1 {
		t.Fatalf("assignment queued %d runs, want 1", n)
	}
	commentID := f.comment(t, "role: you build this one", commentTriggerSourceIssueAssignee, DispatchCoalesced)

	tasks := f.tasks(t)
	if len(tasks) != 1 {
		t.Fatalf("reassign + comment queued %d runs, want 1", len(tasks))
	}
	task := tasks[0]
	if uuidToString(task.TriggerCommentID) != commentID {
		t.Fatalf("the single run must carry the comment as its trigger, got %s", uuidToString(task.TriggerCommentID))
	}
	if uuidToString(task.CommentThreadID) != commentID {
		t.Fatalf("the run must move into the comment's thread, got %s", uuidToString(task.CommentThreadID))
	}
	if !strings.Contains(task.TriggerSummary.String, coalescedWithAssignmentNote) {
		t.Fatalf("trigger_summary does not record the coalescing: %q", task.TriggerSummary.String)
	}
	// A reply in that thread keeps folding into the same run.
	issue, _ := testHandler.Queries.GetIssue(f.ctx, parseUUID(f.issueID))
	reply := dbfx.Comment(t, f.issueID, "one more detail", testutil.Cols{"parent_id": commentID})
	result := testHandler.enqueueCommentAgentTriggers(f.ctx, issue, parseUUID(reply),
		[]commentAgentTrigger{{Agent: f.agent, Source: commentTriggerSourceIssueAssignee, AlreadyPending: true}})
	if got := result[f.agentID].status; got != DispatchCoalesced {
		t.Fatalf("reply in the coalesced thread: got %s, want coalesced", got)
	}
	// A second ROOT comment opens its own thread and gets its own run, exactly
	// as before PANT-1442: only the thread-less assignment slot coalesces.
	f.comment(t, "separate topic", commentTriggerSourceIssueAssignee, DispatchQueued)
	if n := len(f.tasks(t)); n != 2 {
		t.Fatalf("a second root thread should get its own run, got %d runs", n)
	}
}

func TestRunTriggerCoalesce_CommentThenReassign(t *testing.T) {
	f := newCoalesceFixture(t, "coalesce comment-then-reassign")
	f.comment(t, "@agent please take this", commentTriggerSourceMentionAgent, DispatchQueued)
	f.assign(t)

	tasks := f.tasks(t)
	if len(tasks) != 1 {
		t.Fatalf("comment + reassign queued %d runs, want 1", len(tasks))
	}
	if !strings.Contains(tasks[0].TriggerSummary.String, "assignment arrived within 60s") {
		t.Fatalf("trigger_summary does not record the coalesced assignment: %q", tasks[0].TriggerSummary.String)
	}
}

func TestRunTriggerCoalesce_ClaimedRunAbsorbsReassign(t *testing.T) {
	f := newCoalesceFixture(t, "coalesce running-then-reassign")
	f.comment(t, "@agent please take this", commentTriggerSourceMentionAgent, DispatchQueued)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running', started_at=now() WHERE issue_id=$1", f.issueID)
	f.assign(t)
	if n := len(f.tasks(t)); n != 1 {
		t.Fatalf("a reassign onto the agent's just-started run queued a second run (%d runs)", n)
	}
}

func TestRunTriggerCoalesce_OutsideWindowStartsNewRun(t *testing.T) {
	f := newCoalesceFixture(t, "coalesce outside window")
	f.assign(t)
	dbfx.Exec(t, "UPDATE agent_task_queue SET created_at = now() - interval '2 minutes' WHERE issue_id=$1", f.issueID)
	f.comment(t, "much later instruction", commentTriggerSourceIssueAssignee, DispatchQueued)
	if n := len(f.tasks(t)); n != 2 {
		t.Fatalf("a comment outside the window must start its own run, got %d runs", n)
	}
}

func TestRunTriggerCoalesce_UnrelatedTaskOutsideWindowDoesNotAbsorbReassign(t *testing.T) {
	f := newCoalesceFixture(t, "coalesce stale comment run")
	f.comment(t, "@agent old ask", commentTriggerSourceMentionAgent, DispatchQueued)
	dbfx.Exec(t, "UPDATE agent_task_queue SET created_at = now() - interval '2 minutes' WHERE issue_id=$1", f.issueID)
	f.assign(t)
	if n := len(f.tasks(t)); n != 2 {
		t.Fatalf("an assignment after the window must start its own run, got %d runs", n)
	}
}
