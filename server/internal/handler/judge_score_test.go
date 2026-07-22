package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestListJudgeScoresByTask(t *testing.T) {
	taskID := createJudgeScoreHandlerTask(t, testWorkspaceID)
	insertJudgeScoreHandlerScore(t, taskID, "anthropic", "claude-sonnet-4", "1.230000")

	w := listJudgeScoresByTask(t, taskID, testWorkspaceID)
	if w.Code != http.StatusOK {
		t.Fatalf("ListJudgeScoresByTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got []protocol.JudgeScorePayload
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode judge score response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 judge score, got %d: %#v", len(got), got)
	}
	score := got[0]
	if score.TaskID != taskID {
		t.Fatalf("expected task_id %q, got %q", taskID, score.TaskID)
	}
	if score.JudgeProvider != "anthropic" || score.JudgeModel != "claude-sonnet-4" {
		t.Fatalf("unexpected judge identity: %#v", score)
	}
	if score.CorrectnessScore != 91 || score.AdherenceScore != 82 || score.ToneScore != 73 ||
		score.ClarityScore != 64 || score.TrajectoryScore != 55 || score.OverallScore != 88 {
		t.Fatalf("unexpected rubric scores: %#v", score)
	}
	if score.Rationale != "looks good" || score.TrajectoryRationale != "steady progress" {
		t.Fatalf("unexpected rationales: %#v", score)
	}
	if score.CalibrationStatus != "MODELED" || score.InputTokens != 1234 || score.OutputTokens != 567 {
		t.Fatalf("unexpected metadata fields: %#v", score)
	}
	if score.CostUsd != "1.230000" {
		t.Fatalf("expected cost_usd %q, got %q", "1.230000", score.CostUsd)
	}
	if _, err := time.Parse(time.RFC3339Nano, score.CreatedAt); err != nil {
		t.Fatalf("created_at is not RFC3339Nano: %q: %v", score.CreatedAt, err)
	}
}

func TestListJudgeScoresByTaskNoRowsReturnsEmptyList(t *testing.T) {
	taskID := createJudgeScoreHandlerTask(t, testWorkspaceID)

	w := listJudgeScoresByTask(t, taskID, testWorkspaceID)
	if w.Code != http.StatusOK {
		t.Fatalf("ListJudgeScoresByTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertJSONEqual(t, w.Body.Bytes(), `[]`)
}

func TestListJudgeScoresByTaskRejectsDifferentWorkspace(t *testing.T) {
	otherWorkspaceID := createJudgeScoreHandlerWorkspace(t)
	taskID := createJudgeScoreHandlerTask(t, otherWorkspaceID)
	insertJudgeScoreHandlerScore(t, taskID, "anthropic", "claude-sonnet-4", "0.500000")

	w := listJudgeScoresByTask(t, taskID, testWorkspaceID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListJudgeScoresByTask: expected 404 for cross-workspace task, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListJudgeScoresByTaskRejectsInvalidTaskID(t *testing.T) {
	w := listJudgeScoresByTask(t, "not-a-uuid", testWorkspaceID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ListJudgeScoresByTask: expected 400 for invalid task id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListJudgeScoresByTaskMissingTaskReturnsNotFound(t *testing.T) {
	w := listJudgeScoresByTask(t, "00000000-0000-0000-0000-000000000001", testWorkspaceID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("ListJudgeScoresByTask: expected 404 for missing task, got %d: %s", w.Code, w.Body.String())
	}
}

func listJudgeScoresByTask(t *testing.T, taskID, workspaceID string) *httptest.ResponseRecorder {
	t.Helper()

	req := newRequest(http.MethodGet, "/api/tasks/"+taskID+"/judge-scores", nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	req = withURLParam(req, "taskId", taskID)

	r := chi.NewRouter()
	r.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
	r.Get("/api/tasks/{taskId}/judge-scores", testHandler.ListJudgeScoresByTask)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func createJudgeScoreHandlerTask(t *testing.T, workspaceID string) string {
	t.Helper()

	issueID := createJudgeScoreHandlerIssue(t, workspaceID)

	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at ASC LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("failed to load handler test agent: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at, completed_at)
		VALUES ($1, $2, $3, 'completed', 0, now(), now())
		RETURNING id
	`, agentID, handlerTestRuntimeID(t), issueID).Scan(&taskID); err != nil {
		t.Fatalf("failed to create judge score handler task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	return taskID
}

func createJudgeScoreHandlerIssue(t *testing.T, workspaceID string) string {
	t.Helper()

	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES (
			$1, 'judge score handler test', 'todo', 'medium', 'member', $2,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
			0
		)
		RETURNING id
	`, workspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("failed to create judge score handler issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	return issueID
}

func createJudgeScoreHandlerWorkspace(t *testing.T) string {
	t.Helper()

	var workspaceID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Judge Score Other Workspace', 'judge-score-other-' || gen_random_uuid(), '', 'JSO')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("failed to create judge score handler workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	return workspaceID
}

func insertJudgeScoreHandlerScore(t *testing.T, taskID, provider, model, costUSD string) {
	t.Helper()

	var scoreID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO judge_score (
			task_id, judge_provider, judge_model,
			correctness_score, adherence_score, tone_score, clarity_score, trajectory_score, overall_score,
			rationale, trajectory_rationale, calibration_status,
			input_tokens, output_tokens, cost_usd
		) VALUES (
			$1, $2, $3,
			91, 82, 73, 64, 55, 88,
			'looks good', 'steady progress', 'MODELED',
			1234, 567, $4
		)
		RETURNING id
	`, taskID, provider, model, costUSD).Scan(&scoreID); err != nil {
		t.Fatalf("failed to insert judge score: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM judge_score WHERE id = $1`, scoreID)
	})
}
