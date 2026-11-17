package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// These tests cover the per-runtime half of daemon authorization: a user
// credential (PAT or JWT, which the daemon routes see as X-User-ID) acts only
// for runtimes that same user owns, and an agent task token never manages an
// agent. Before, workspace membership was enough, so one runtime's member PAT
// could claim another runtime's tasks (receiving a task token bound to that
// runtime's owner), re-register another runtime's broadcast daemon_id and take
// it over, and through the task token rewrite custom_args or mcp_config on
// the other owner's agents.

// runtimeOwnerFixture is two workspace members, each owning one daemon
// runtime with an agent and a queued task on it.
type runtimeOwnerFixture struct {
	ownerA, ownerB     string
	runtimeA, runtimeB string
	daemonA, daemonB   string
	agentA, agentB     string
	taskA, taskB       string
}

func newRuntimeOwnerFixture(t *testing.T) runtimeOwnerFixture {
	t.Helper()
	ctx := context.Background()
	var f runtimeOwnerFixture
	f.ownerA = dbfx.User(t, "Runtime owner A", "runtime-owner-a-"+uuid.NewString()+"@example.com")
	f.ownerB = dbfx.User(t, "Runtime owner B", "runtime-owner-b-"+uuid.NewString()+"@example.com")
	dbfx.Member(t, testWorkspaceID, f.ownerA, "member")
	dbfx.Member(t, testWorkspaceID, f.ownerB, "member")
	f.daemonA = "owner-a-daemon-" + uuid.NewString()
	f.daemonB = "owner-b-daemon-" + uuid.NewString()
	f.runtimeA = dbfx.Runtime(t, "Owner A runtime", testutil.Cols{
		"owner_id": f.ownerA, "daemon_id": f.daemonA, "runtime_mode": "local", "provider": "claude",
	})
	f.runtimeB = dbfx.Runtime(t, "Owner B runtime", testutil.Cols{
		"owner_id": f.ownerB, "daemon_id": f.daemonB, "runtime_mode": "local", "provider": "claude",
	})
	var issueA, issueB string
	f.agentA, issueA = createClaimReclaimAgentAndIssue(t, ctx, f.runtimeA, "Owner A agent")
	f.agentB, issueB = createClaimReclaimAgentAndIssue(t, ctx, f.runtimeB, "Owner B agent")
	dbfx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, f.ownerA, f.agentA)
	dbfx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, f.ownerB, f.agentB)
	f.taskA = seedQueuedIssueTask(t, ctx, f.agentA, f.runtimeA, issueA)
	f.taskB = seedQueuedIssueTask(t, ctx, f.agentB, f.runtimeB, issueB)
	return f
}

func ownerTestTaskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	return status
}

func runtimeOwner(t *testing.T, runtimeID string) string {
	t.Helper()
	var owner string
	dbfx.QueryRow(t, `SELECT COALESCE(owner_id::text, '') FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&owner)
	return owner
}

func TestDaemonClaim_CrossUserRefused(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	f := newRuntimeOwnerFixture(t)

	// A's PAT claiming B's runtime: 404, B's task stays queued, no token.
	req := newRequestAs(f.ownerA, http.MethodPost, "/api/daemon/runtimes/"+f.runtimeB+"/tasks/claim", nil)
	req = withURLParam(req, "runtimeId", f.runtimeB)
	w := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user claim = %d, want 404: %s", w.Code, w.Body.String())
	}
	if got := ownerTestTaskStatus(t, f.taskB); got != "queued" {
		t.Fatalf("B's task status after cross-user claim = %q, want queued", got)
	}
	var tokens int
	dbfx.QueryRow(t, `SELECT count(*) FROM task_token WHERE task_id = $1`, f.taskB).Scan(&tokens)
	if tokens != 0 {
		t.Fatalf("cross-user claim minted %d task tokens for B's task", tokens)
	}

	// The batch claim with B's (broadcast) daemon_id claims nothing either.
	req = newRequestAs(f.ownerA, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": f.daemonB, "runtime_ids": []string{f.runtimeB}, "max_tasks": 5,
	})
	w = httptest.NewRecorder()
	testHandler.ClaimTasksByRuntime(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"tasks":[]}` {
		t.Fatalf("cross-user batch claim = %d %s, want 200 with no tasks", w.Code, w.Body.String())
	}
	if got := ownerTestTaskStatus(t, f.taskB); got != "queued" {
		t.Fatalf("B's task status after cross-user batch claim = %q, want queued", got)
	}

	// B's own PAT still claims its task.
	req = newRequestAs(f.ownerB, http.MethodPost, "/api/daemon/runtimes/"+f.runtimeB+"/tasks/claim", nil)
	req = withURLParam(req, "runtimeId", f.runtimeB)
	w = httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), f.taskB) {
		t.Fatalf("owner claim = %d %s, want B's task", w.Code, w.Body.String())
	}
}

func TestDaemonRuntimeRoutes_CrossUserRefused(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	f := newRuntimeOwnerFixture(t)

	// Heartbeat for B's runtime with A's PAT.
	req := newRequestAs(f.ownerA, http.MethodPost, "/api/daemon/heartbeat", map[string]any{"runtime_id": f.runtimeB})
	w := httptest.NewRecorder()
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user heartbeat = %d, want 404: %s", w.Code, w.Body.String())
	}

	// Pending list, recover-orphans: requireDaemonRuntimeAccess.
	req = newRequestAs(f.ownerA, http.MethodGet, "/api/daemon/runtimes/"+f.runtimeB+"/tasks/pending", nil)
	req = withURLParam(req, "runtimeId", f.runtimeB)
	w = httptest.NewRecorder()
	testHandler.ListPendingTasksByRuntime(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user pending list = %d, want 404: %s", w.Code, w.Body.String())
	}

	// Deregister skips B's runtime: it stays online.
	req = newRequestAs(f.ownerA, http.MethodPost, "/api/daemon/deregister", map[string]any{"runtime_ids": []string{f.runtimeB}})
	w = httptest.NewRecorder()
	testHandler.DaemonDeregister(w, req)
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_runtime WHERE id = $1`, f.runtimeB).Scan(&status)
	if status != "online" {
		t.Fatalf("B's runtime status after cross-user deregister = %q, want online", status)
	}

	// The daemon WebSocket refuses a runtime set naming B's runtime.
	req = newRequestAs(f.ownerA, http.MethodGet, "/api/daemon/ws", nil)
	w = httptest.NewRecorder()
	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{f.runtimeA, f.runtimeB}, f.ownerA); ok {
		t.Fatalf("cross-user websocket identity accepted")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user websocket identity = %d, want 404", w.Code)
	}

	// Own runtime still works on each route.
	req = newRequestAs(f.ownerA, http.MethodPost, "/api/daemon/heartbeat", map[string]any{"runtime_id": f.runtimeA})
	w = httptest.NewRecorder()
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("own heartbeat = %d, want 200: %s", w.Code, w.Body.String())
	}
	req = newRequestAs(f.ownerA, http.MethodGet, "/api/daemon/ws", nil)
	w = httptest.NewRecorder()
	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{f.runtimeA}, f.ownerA); !ok {
		t.Fatalf("own websocket identity refused: %d %s", w.Code, w.Body.String())
	}
}

func TestDaemonTaskRoutes_CrossUserRefused(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	f := newRuntimeOwnerFixture(t)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', dispatched_at = now(), started_at = now() WHERE id = $1`, f.taskB)

	call := func(userID, method, path string, body any, h http.HandlerFunc) *httptest.ResponseRecorder {
		req := newRequestAs(userID, method, path, body)
		req = withURLParam(req, "taskId", f.taskB)
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	routes := []struct {
		name   string
		method string
		path   string
		body   any
		h      http.HandlerFunc
	}{
		{"status", http.MethodGet, "/status", nil, testHandler.GetTaskStatus},
		{"progress", http.MethodPost, "/progress", map[string]any{"summary": "x", "step": 1, "total": 2}, testHandler.ReportTaskProgress},
		{"messages", http.MethodPost, "/messages", map[string]any{"messages": []any{map[string]any{"seq": 1, "type": "text", "content": "forged"}}}, testHandler.ReportTaskMessages},
		{"list messages", http.MethodGet, "/messages", nil, testHandler.ListTaskMessages},
		{"usage", http.MethodPost, "/usage", map[string]any{"usage": []any{}}, testHandler.ReportTaskUsage},
		{"complete", http.MethodPost, "/complete", map[string]any{"output": "hijacked"}, testHandler.CompleteTask},
		{"fail", http.MethodPost, "/fail", map[string]any{"error": "hijacked"}, testHandler.FailTask},
	}
	for _, rt := range routes {
		w := call(f.ownerA, rt.method, "/api/daemon/tasks/"+f.taskB+rt.path, rt.body, rt.h)
		if w.Code != http.StatusNotFound {
			t.Errorf("cross-user task %s = %d, want 404: %s", rt.name, w.Code, w.Body.String())
		}
	}
	if got := ownerTestTaskStatus(t, f.taskB); got != "running" {
		t.Fatalf("B's task status after cross-user task calls = %q, want running", got)
	}

	// B's own PAT reads the status.
	w := call(f.ownerB, http.MethodGet, "/api/daemon/tasks/"+f.taskB+"/status", nil, testHandler.GetTaskStatus)
	if w.Code != http.StatusOK {
		t.Fatalf("owner task status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func registerAs(t *testing.T, userID, daemonID string, legacy []string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"device_name":  "host",
		"runtimes": []map[string]any{
			{"name": "claude (host)", "type": "claude", "version": "1.0.0", "status": "online"},
		},
	}
	if legacy != nil {
		body["legacy_daemon_ids"] = legacy
	}
	w := httptest.NewRecorder()
	testHandler.DaemonRegister(w, newRequestAs(userID, http.MethodPost, "/api/daemon/register", body))
	return w
}

func TestDaemonRegister_HijackRefused(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	f := newRuntimeOwnerFixture(t)

	// A re-registers with B's daemon_id: refused, B keeps the runtime.
	w := registerAs(t, f.ownerA, f.daemonB, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("re-register hijack = %d, want 403: %s", w.Code, w.Body.String())
	}
	if got := runtimeOwner(t, f.runtimeB); got != f.ownerB {
		t.Fatalf("B's runtime owner after hijack attempt = %q, want %q", got, f.ownerB)
	}

	// A names B's daemon_id as a legacy id: B's runtime is not folded in.
	newDaemon := "owner-a-new-daemon-" + uuid.NewString()
	w = registerAs(t, f.ownerA, newDaemon, []string{f.daemonB})
	if w.Code != http.StatusOK {
		t.Fatalf("register with legacy ids = %d, want 200: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, newDaemon)
	})
	var agentRuntime string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.agentB).Scan(&agentRuntime)
	if agentRuntime != f.runtimeB || runtimeOwner(t, f.runtimeB) != f.ownerB {
		t.Fatalf("legacy merge moved B's agent to %s (owner %s), want it on %s", agentRuntime, runtimeOwner(t, f.runtimeB), f.runtimeB)
	}

	// The owner re-registers its own daemon_id (a reconnect).
	w = registerAs(t, f.ownerB, f.daemonB, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("owner re-register = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestDaemonRegister_OwnerlessRuntimeAdminOnly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	member := dbfx.User(t, "Ownerless member", "ownerless-member-"+uuid.NewString()+"@example.com")
	dbfx.Member(t, testWorkspaceID, member, "member")
	daemonID := "ownerless-daemon-" + uuid.NewString()
	runtimeID := dbfx.Runtime(t, "Ownerless runtime", testutil.Cols{
		"owner_id": nil, "daemon_id": daemonID, "runtime_mode": "local", "provider": "claude",
	})

	// A plain member neither registers over nor acts for an ownerless runtime.
	if w := registerAs(t, member, daemonID, nil); w.Code != http.StatusForbidden {
		t.Fatalf("member register over ownerless runtime = %d, want 403: %s", w.Code, w.Body.String())
	}
	req := newRequestAs(member, http.MethodPost, "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	w := httptest.NewRecorder()
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("member heartbeat on ownerless runtime = %d, want 404", w.Code)
	}

	// The workspace owner (testUserID) may.
	req = newRequestAs(testUserID, http.MethodPost, "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})
	w = httptest.NewRecorder()
	testHandler.DaemonHeartbeat(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("workspace-owner heartbeat on ownerless runtime = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestDaemonRoutes_SharedUserStillWorks pins today's single-user setup: both
// runtimes registered with one operator PAT have the same owner, so each one
// still claims, heartbeats and re-registers. It also documents the limit: with
// one shared user the owner check separates nothing.
func TestDaemonRoutes_SharedUserStillWorks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	operator := dbfx.User(t, "Shared operator", "shared-operator-"+uuid.NewString()+"@example.com")
	dbfx.Member(t, testWorkspaceID, operator, "member")
	daemons := []string{"shared-one-" + uuid.NewString(), "shared-two-" + uuid.NewString()}
	for _, d := range daemons {
		if w := registerAs(t, operator, d, nil); w.Code != http.StatusOK {
			t.Fatalf("register %s = %d: %s", d, w.Code, w.Body.String())
		}
		d := d
		t.Cleanup(func() {
			testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, d)
		})
		var runtimeID string
		dbfx.QueryRow(t, `SELECT id::text FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, d).Scan(&runtimeID)
		agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Shared agent "+d)
		dbfx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, operator, agentID)
		taskID := seedQueuedIssueTask(t, ctx, agentID, runtimeID, issueID)

		req := newRequestAs(operator, http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
		req = withURLParam(req, "runtimeId", runtimeID)
		w := httptest.NewRecorder()
		testHandler.ClaimTaskByRuntime(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), taskID) {
			t.Fatalf("shared-user claim on %s = %d %s", d, w.Code, w.Body.String())
		}
		if w := registerAs(t, operator, d, nil); w.Code != http.StatusOK {
			t.Fatalf("shared-user re-register %s = %d: %s", d, w.Code, w.Body.String())
		}
	}
}

func TestCanManageAgent_TaskTokenRefused(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	f := newRuntimeOwnerFixture(t)

	updates := []map[string]any{
		{"custom_args": []string{"--settings", "/tmp/evil.json"}},
		{"mcp_config": map[string]any{"mcpServers": map[string]any{"x": map[string]any{"command": "sh"}}}},
		{"instructions": "do something else"},
		{"runtime_id": f.runtimeA},
	}
	// A task token bound to B (as a claim on B's runtime would mint) on B's
	// own agent and on another of B's agents: always refused.
	otherAgentB := dbfx.Agent(t, "Owner B second agent", f.runtimeB, testutil.Cols{"owner_id": f.ownerB})
	for _, target := range []string{f.agentB, otherAgentB} {
		for _, body := range updates {
			req := newRequestAs(f.ownerB, http.MethodPut, "/api/agents/"+target, body)
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Agent-ID", f.agentB)
			req.Header.Set("X-Task-ID", f.taskB)
			req = withURLParam(req, "id", target)
			w := httptest.NewRecorder()
			testHandler.UpdateAgent(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("task token update %v on %s = %d, want 403: %s", body, target, w.Code, w.Body.String())
			}
		}
		req := newRequestAs(f.ownerB, http.MethodPost, "/api/agents/"+target+"/archive", nil)
		req.Header.Set("X-Actor-Source", "task_token")
		req = withURLParam(req, "id", target)
		w := httptest.NewRecorder()
		testHandler.ArchiveAgent(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("task token archive on %s = %d, want 403", target, w.Code)
		}
	}
	var args, instructions string
	dbfx.QueryRow(t, `SELECT custom_args::text, instructions FROM agent WHERE id = $1`, f.agentB).Scan(&args, &instructions)
	if strings.Contains(args, "--settings") || instructions == "do something else" {
		t.Fatalf("task token changed the agent: custom_args=%s instructions=%q", args, instructions)
	}

	// The owner's PAT (a human in the UI or CLI) still edits its agent, and a
	// workspace owner/admin still edits any agent.
	for _, userID := range []string{f.ownerB, testUserID} {
		req := newRequestAs(userID, http.MethodPut, "/api/agents/"+f.agentB, map[string]any{"instructions": "owner edit"})
		req = withURLParam(req, "id", f.agentB)
		w := httptest.NewRecorder()
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("human update by %s = %d, want 200: %s", userID, w.Code, w.Body.String())
		}
	}
}
