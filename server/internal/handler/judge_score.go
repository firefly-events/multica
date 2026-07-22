package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ListJudgeScoresByTask returns every LLM-as-judge scoring pass recorded
// for a single completed task (DOS-860). Mirrors the auth pattern used by
// ListTaskMessagesByUser: the task id is opaque to the caller until we've
// confirmed it belongs to their workspace, so an unauthorized lookup
// returns 404 rather than leaking existence via a 403.
func (h *Handler) ListJudgeScoresByTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	wsID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	scores, err := h.Queries.GetJudgeScoresByTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list judge scores")
		return
	}

	resp := make([]protocol.JudgeScorePayload, len(scores))
	for i, s := range scores {
		costUsd := "0"
		if v, err := s.CostUsd.Value(); err == nil && v != nil {
			if str, ok := v.(string); ok {
				costUsd = str
			}
		}
		resp[i] = protocol.JudgeScorePayload{
			ID:                  uuidToString(s.ID),
			TaskID:              uuidToString(s.TaskID),
			JudgeProvider:       s.JudgeProvider,
			JudgeModel:          s.JudgeModel,
			CorrectnessScore:    int(s.CorrectnessScore),
			AdherenceScore:      int(s.AdherenceScore),
			ToneScore:           int(s.ToneScore),
			ClarityScore:        int(s.ClarityScore),
			TrajectoryScore:     int(s.TrajectoryScore),
			OverallScore:        int(s.OverallScore),
			Rationale:           s.Rationale,
			TrajectoryRationale: s.TrajectoryRationale,
			CalibrationStatus:   s.CalibrationStatus,
			InputTokens:         s.InputTokens,
			OutputTokens:        s.OutputTokens,
			CostUsd:             costUsd,
			CreatedAt:           s.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
