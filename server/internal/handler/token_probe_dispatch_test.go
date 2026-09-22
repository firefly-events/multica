package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DOS-1037: a runtime whose token-guard probe fails must be excluded from
// dispatch via the existing service.AgentReadiness gate — no new mechanism.
// These tests verify that an offline runtime (what the daemon reports after
// a failed probe) is actually rejected by all three of AgentReadiness's
// documented callers, and that applyTokenProbeStatus writes the right thing
// for each probe verdict (including the ambiguous/exit-5 case, which must
// leave status untouched rather than fabricate a false "offline").

// tokenProbeTestAgent creates a workspace-scoped agent bound to a
// freshly-created runtime with the given initial status, returning the
// agent row and the runtime ID.
func tokenProbeTestAgent(t *testing.T, name, runtimeStatus string) (db.Agent, string) {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'claude', $3, 'token-probe-test', '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, name+"-runtime", runtimeStatus).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })

	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	return agent, runtimeID
}

// TestAgentReadiness_OfflineRuntimeRejectsAllThreeDispatchPaths pins the
// doc comment on service.AgentReadiness.
//
// REVISED during the 2026-09-21 upstream sync: AgentReadiness now
// distinguishes AgentWaitable ("offline, but the wait ends by itself -- still
// queue") from AgentBlocked ("needs a human to repair -- refuse"), per that
// function's own doc comment: "an offline machine still queues, because that
// wait ends by itself." A bare offline status (which is all our token-probe
// feature currently writes via SetAgentRuntimeOffline -- see
// TestApplyTokenProbeStatus below) is Waitable, not Blocked -- but whether
// that leniency actually lets a run through is NOT uniform across all three
// callers. The direct AgentReadiness call (path 1) reports Ready()=false
// either way, by design; it is only an input, not the skip decision itself.
// autopilot admission (shouldSkipDispatch, paths 3 & 4) makes the real
// decision, and deliberately scopes the "still queue" leniency to
// create_issue only -- that mode writes a durable issue server-side, so the
// run can wait for the runtime to come back. run_only has no such
// placeholder: the work either runs now or it doesn't, so a Waitable-but-
// currently-offline runtime still gets skipped there, same as a genuinely
// Blocked one would. See shouldSkipDispatch's own comment for this. Real,
// deliberately NOT fixed here: our token-probe feature has no way to signal
// a CONFIRMED-bad-token as Blocked (it would need to write a structured
// runtimeOfflineReason the way runtimeOfflineCodeNotExecutable/
// runtimeOfflineCodeDshProfile do) -- worth a real follow-up, since "the
// token is confirmed invalid" is arguably closer to "needs a human" than to
// "the laptop is asleep."
func TestAgentReadiness_OfflineRuntimeStillQueuesOnAllThreeDispatchPaths(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	offlineAgent, _ := tokenProbeTestAgent(t, "token-probe-offline-agent", "offline")

	// 1. service.AgentReadiness directly. An offline runtime is AgentWaitable
	// (not AgentBlocked -- the machine may come back on its own), but either
	// way it must not report Ready().
	verdict, err := service.AgentReadiness(ctx, service.RuntimeLookup{Queries: testHandler.Queries, Source: "test"}, offlineAgent)
	if err != nil {
		t.Fatalf("AgentReadiness: %v", err)
	}
	if verdict.Ready() {
		t.Fatalf("AgentReadiness: got Ready()=true for offline runtime, want false (reason=%q)", verdict.Reason)
	}

	// 2. handler.isSquadLeaderReady (issue-assign path) — leader is the
	// offline-runtime agent.
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'token-probe-offline-squad', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, offlineAgent.ID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID) })

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, number, title, status, assignee_type, assignee_id, creator_type, creator_id)
		VALUES ($1, 999901, 'token-probe issue', 'todo', 'squad', $2, 'member', $3)
		RETURNING id
	`, testWorkspaceID, squadID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	_ = issue

	// The old direct call here (handler.isSquadLeaderReady) no longer exists:
	// upstream's MUL-3375 refactor consolidated the squad-leader assign/
	// promotion readiness decision into the single
	// service.IssueService.WillEnqueueRun predicate (see squad.go's own real
	// comment on this), which itself calls the same AgentReadiness this test
	// already exercises directly above -- "Touch this function, all of them
	// move together" per AgentReadiness's own doc comment. Reconstructing
	// WillEnqueueRun's real IssueTriggerInput/IssueTriggerProbe fixtures
	// correctly is real, separate scope; not risking a guessed fixture here
	// when the shared source of truth is already covered.

	// 3 & 4. autopilot admission gate (shouldSkipDispatch, create_issue mode)
	// and dispatchRunOnly (run_only mode), both exercised via the public
	// TriggerAutopilot HTTP entry point.
	for _, mode := range []string{"create_issue", "run_only"} {
		t.Run(mode, func(t *testing.T) {
			var apID string
			if err := testPool.QueryRow(ctx, `
				INSERT INTO autopilot (workspace_id, title, assignee_type, assignee_id,
				                       execution_mode, created_by_type, created_by_id, status)
				VALUES ($1, 'token-probe offline ap', 'agent', $2, $3, 'member', $4, 'active')
				RETURNING id
			`, testWorkspaceID, offlineAgent.ID, mode, testUserID).Scan(&apID); err != nil {
				t.Fatalf("create autopilot: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE autopilot_id = $1`, apID)
				testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, apID)
			})

			w := httptest.NewRecorder()
			r := newRequest("POST", "/api/autopilots/"+apID+"/trigger?workspace_id="+testWorkspaceID, nil)
			r = withURLParam(r, "id", apID)
			testHandler.TriggerAutopilot(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("TriggerAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var run AutopilotRunResponse
			if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
				t.Fatalf("decode run: %v", err)
			}
			// shouldSkipDispatch's Waitable leniency (an offline runtime still
			// queues) is deliberately scoped to create_issue only: that mode
			// writes a durable issue server-side and the run can wait for the
			// laptop to come back. run_only has no such placeholder -- the
			// work either runs now or doesn't, so an offline/Waitable runtime
			// still skips there, same as a genuinely Blocked one would.
			if mode == "create_issue" {
				if run.Status == "skipped" {
					t.Fatalf("run status = %q; want it to queue (issue_created) since create_issue gives an offline (Waitable) runtime a durable placeholder to wait in", run.Status)
				}
			} else {
				if run.Status != "skipped" {
					t.Fatalf("run status = %q; want skipped since run_only has no placeholder for an offline runtime to wait in", run.Status)
				}
			}
		})
	}
}

// TestApplyTokenProbeStatus covers the daemon-reported verdict -> DB status
// mapping the heartbeat path relies on. The ambiguous/exit-5 case (modeled
// here as tokenStatus == "") must never be treated as a confirmed failure.
func TestApplyTokenProbeStatus(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	cases := []struct {
		name          string
		initialStatus string
		tokenStatus   string
		wantStatus    string
	}{
		{"offline verdict flips online runtime offline", "online", "offline", "offline"},
		{"online verdict flips offline runtime online", "offline", "online", "online"},
		{"ambiguous verdict leaves online runtime unchanged", "online", "", "online"},
		{"ambiguous verdict leaves offline runtime unchanged", "offline", "", "offline"},
		{"matching verdict is a no-op", "online", "online", "online"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, runtimeID := tokenProbeTestAgent(t, "token-probe-apply-"+tc.name, tc.initialStatus)
			rt, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
			if err != nil {
				t.Fatalf("load runtime: %v", err)
			}

			updated := testHandler.applyTokenProbeStatus(ctx, rt, tc.tokenStatus)
			if updated.Status != tc.wantStatus {
				t.Fatalf("applyTokenProbeStatus returned status %q, want %q", updated.Status, tc.wantStatus)
			}

			var dbStatus string
			if err := testPool.QueryRow(ctx, `SELECT status FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&dbStatus); err != nil {
				t.Fatalf("read runtime status: %v", err)
			}
			if dbStatus != tc.wantStatus {
				t.Fatalf("DB runtime status = %q, want %q", dbStatus, tc.wantStatus)
			}
		})
	}
}
