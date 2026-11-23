package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// cancelStaleQueuedTasks cancels the previous assignee's not-yet-claimed tasks
// on an issue when the issue leaves in_review or its assignee changes
// (pantheon-v2#589). Without it a reviewer's queued run on an issue that has
// since moved to blocked, or been handed to someone else, still waits for a
// slot and eventually starts with nothing to do.
//
// Scope is deliberately narrow so it does not bring back the coarse
// cancel-on-reassign removed in MUL-4113: only the agent that held the issue
// (a squad's leader for a squad assignee), and only queued / deferred rows.
// Dispatched and running work, including the run making this change, and
// other agents' tasks on the issue are untouched. Call it BEFORE the update's
// own run is enqueued, so a fresh run for the same agent survives and does not
// coalesce into a stale row.
func (h *Handler) cancelStaleQueuedTasks(ctx context.Context, prev, issue db.Issue, assigneeChanged, statusChanged bool) {
	if h.TaskService == nil {
		return
	}
	if !assigneeChanged && !(statusChanged && leftInReview(ctx, h.Queries, prev, issue)) {
		return
	}
	agentID, ok := h.assigneeRunAgent(ctx, prev)
	if !ok {
		return
	}
	cancelled, err := h.TaskService.CancelQueuedTasksForIssueAgent(ctx, issue.ID, agentID)
	if err != nil {
		slog.Warn("cancel stale queued tasks failed",
			"issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return
	}
	if len(cancelled) > 0 {
		slog.Info("cancelled stale queued tasks",
			"issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID),
			"count", len(cancelled), "assignee_changed", assigneeChanged,
			"prev_status", prev.Status, "status", issue.Status)
	}
}

// leftInReview reports whether the update moved the issue out of in_review,
// resolving custom statuses to the built-in behavior they inherit.
func leftInReview(ctx context.Context, q issuestatus.Querier, prev, issue db.Issue) bool {
	return issuestatus.Effective(ctx, q, prev.WorkspaceID, prev.Status) == "in_review" &&
		issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) != "in_review"
}

// assigneeRunAgent returns the agent whose runs an issue's assignee owns: the
// agent itself, or a squad's leader. Members own no runs.
func (h *Handler) assigneeRunAgent(ctx context.Context, issue db.Issue) (pgtype.UUID, bool) {
	if !issue.AssigneeID.Valid {
		return pgtype.UUID{}, false
	}
	switch issue.AssigneeType.String {
	case "agent":
		return issue.AssigneeID, true
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return pgtype.UUID{}, false
		}
		return squad.LeaderID, squad.LeaderID.Valid
	}
	return pgtype.UUID{}, false
}
