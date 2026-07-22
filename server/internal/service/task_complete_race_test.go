package service

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// mockRow implements pgx.Row, returning either a scanned task or pgx.ErrNoRows.
type mockRow struct {
	task *db.AgentTaskQueue
	err  error
}

func (r *mockRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	t := r.task
	ptrs := []any{
		&t.ID, &t.AgentID, &t.IssueID, &t.Status, &t.Priority,
		&t.DispatchedAt, &t.StartedAt, &t.CompletedAt, &t.Result,
		&t.Error, &t.CreatedAt, &t.Context, &t.RuntimeID,
		&t.SessionID, &t.WorkDir, &t.TriggerCommentID,
		&t.ChatSessionID, &t.AutopilotRunID,
		&t.Attempt, &t.MaxAttempts, &t.ParentTaskID, &t.FailureReason,
		&t.TriggerSummary, &t.ForceFreshSession, &t.IsLeaderTask,
		&t.WaitReason, &t.InitiatorUserID,
	}
	for i, p := range ptrs {
		if i >= len(dest) {
			break
		}
		// Copy value from source to dest by assigning through the pointer.
		switch d := dest[i].(type) {
		case *pgtype.UUID:
			*d = *(p.(*pgtype.UUID))
		case *string:
			*d = *(p.(*string))
		case *int32:
			*d = *(p.(*int32))
		case *pgtype.Timestamptz:
			*d = *(p.(*pgtype.Timestamptz))
		case *[]byte:
			*d = *(p.(*[]byte))
		case *pgtype.Text:
			*d = *(p.(*pgtype.Text))
		case *bool:
			*d = *(p.(*bool))
		}
	}
	return nil
}

// mockDBTX routes QueryRow calls: complete/fail queries return ErrNoRows,
// getAgentTask returns the stored task.
type mockDBTX struct {
	task  db.AgentTaskQueue
	child *db.AgentTaskQueue
}

func (m *mockDBTX) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}

func (m *mockDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (m *mockDBTX) QueryRow(_ context.Context, sql string, _ ...interface{}) pgx.Row {
	if strings.Contains(sql, "INSERT INTO agent_task_queue") {
		if m.child == nil {
			return &mockRow{err: pgx.ErrNoRows}
		}
		return &mockRow{task: m.child}
	}
	// CompleteAgentTask and FailAgentTask SQL contain "SET status ="
	if strings.Contains(sql, "SET status =") {
		return &mockRow{err: pgx.ErrNoRows}
	}
	// GetAgentTask — return the existing task
	return &mockRow{task: &m.task}
}

func testUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Valid = true
	u.Bytes[0] = b
	return u
}

func TestCompleteTask_AlreadyFinalized(t *testing.T) {
	taskID := testUUID(1)
	agentID := testUUID(2)

	tests := []struct {
		name   string
		status string
	}{
		{"already completed", "completed"},
		{"already cancelled", "cancelled"},
		{"already failed", "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDBTX{task: db.AgentTaskQueue{
				ID:      taskID,
				AgentID: agentID,
				Status:  tt.status,
			}}
			svc := &TaskService{
				Queries: db.New(mock),
				Bus:     events.New(),
			}

			got, err := svc.CompleteTask(context.Background(), taskID, nil, "", "")
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got == nil {
				t.Fatal("expected task, got nil")
			}
			if got.Status != tt.status {
				t.Errorf("expected status %q, got %q", tt.status, got.Status)
			}
			if got.ID != taskID {
				t.Error("returned task ID doesn't match")
			}
		})
	}
}

func TestFailTask_AlreadyFinalized(t *testing.T) {
	taskID := testUUID(1)
	agentID := testUUID(2)

	tests := []struct {
		name   string
		status string
	}{
		{"already completed", "completed"},
		{"already cancelled", "cancelled"},
		{"already failed", "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDBTX{task: db.AgentTaskQueue{
				ID:      taskID,
				AgentID: agentID,
				Status:  tt.status,
			}}
			svc := &TaskService{
				Queries: db.New(mock),
				Bus:     events.New(),
			}

			got, err := svc.FailTask(context.Background(), taskID, "agent crashed", "", "", "")
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got == nil {
				t.Fatal("expected task, got nil")
			}
			if got.Status != tt.status {
				t.Errorf("expected status %q, got %q", tt.status, got.Status)
			}
			if got.ID != taskID {
				t.Error("returned task ID doesn't match")
			}
		})
	}
}

func TestTaskFailureClassifiers(t *testing.T) {
	cases := []struct {
		reason       string
		wantType     string
		wantResumeOK bool
		wantRetry    bool
	}{
		{reason: "timeout", wantType: "timeout", wantResumeOK: true, wantRetry: true},
		{reason: "codex_semantic_inactivity", wantType: "timeout", wantResumeOK: false, wantRetry: true},
		{reason: "runtime_recovery", wantType: "runtime", wantResumeOK: true, wantRetry: true},
		{reason: taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: taskfailure.ReasonAgentProviderServerError.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: taskfailure.ReasonAgentProviderNetwork.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: true},
		{reason: taskfailure.ReasonAgentProviderQuotaLimit.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		{reason: taskfailure.ReasonAgentProviderAuthOrAccess.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		{reason: taskfailure.ReasonAgentContextOverflow.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		{reason: taskfailure.ReasonAgentProcessFailure.String(), wantType: "agent_error", wantResumeOK: true, wantRetry: false},
		{reason: "iteration_limit", wantType: "agent_output", wantResumeOK: false, wantRetry: false},
		{reason: "api_invalid_request", wantType: "agent_error", wantResumeOK: false, wantRetry: false},
		{reason: "agent_error", wantType: "agent_error", wantResumeOK: true, wantRetry: false},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if got := taskErrorType(tc.reason); got != tc.wantType {
				t.Fatalf("taskErrorType(%q) = %q, want %q", tc.reason, got, tc.wantType)
			}
			if got := !resumeUnsafeFailureReason(tc.reason); got != tc.wantResumeOK {
				t.Fatalf("resume-safe(%q) = %v, want %v", tc.reason, got, tc.wantResumeOK)
			}
			if got := retryableReasons[tc.reason]; got != tc.wantRetry {
				t.Fatalf("retryableReasons[%q] = %v, want %v", tc.reason, got, tc.wantRetry)
			}
		})
	}
}

func TestMaybeRetryFailedTaskProviderLimitEnqueuesRetry(t *testing.T) {
	parentID := testUUID(1)
	childID := testUUID(2)
	agentID := testUUID(3)
	issueID := testUUID(4)

	parent := db.AgentTaskQueue{
		ID:             parentID,
		AgentID:        agentID,
		IssueID:        issueID,
		Status:         "failed",
		Attempt:        1,
		MaxAttempts:    3,
		FailureReason:  pgtype.Text{String: taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(), Valid: true},
		AutopilotRunID: pgtype.UUID{Valid: false},
	}
	child := parent
	child.ID = childID
	child.IssueID = pgtype.UUID{Valid: false}
	child.Status = "queued"
	child.Attempt = 2
	child.ParentTaskID = pgtype.UUID{Bytes: parentID.Bytes, Valid: true}
	child.RuntimeID = pgtype.UUID{Valid: false}

	svc := &TaskService{
		Queries: db.New(&mockDBTX{task: parent, child: &child}),
		Bus:     events.New(),
	}

	got, err := svc.MaybeRetryFailedTask(context.Background(), parent)
	if err != nil {
		t.Fatalf("MaybeRetryFailedTask returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected retry child, got nil")
	}
	if got.ID != childID {
		t.Fatalf("retry child id = %v, want %v", got.ID, childID)
	}
	if got.Status != "queued" {
		t.Fatalf("retry child status = %q, want queued", got.Status)
	}
	if got.Attempt != 2 {
		t.Fatalf("retry child attempt = %d, want 2", got.Attempt)
	}
}

func TestMaybeRetryFailedTaskHardFailureDoesNotRetry(t *testing.T) {
	parent := db.AgentTaskQueue{
		ID:             testUUID(1),
		AgentID:        testUUID(2),
		IssueID:        testUUID(3),
		Status:         "failed",
		Attempt:        1,
		MaxAttempts:    3,
		FailureReason:  pgtype.Text{String: taskfailure.ReasonAgentProcessFailure.String(), Valid: true},
		AutopilotRunID: pgtype.UUID{Valid: false},
	}
	svc := &TaskService{
		Queries: db.New(&mockDBTX{task: parent}),
		Bus:     events.New(),
	}

	got, err := svc.MaybeRetryFailedTask(context.Background(), parent)
	if err != nil {
		t.Fatalf("MaybeRetryFailedTask returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no retry child, got %+v", got)
	}
}
