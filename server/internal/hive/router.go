package hive

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Router returns a chi sub-router for all Hive plugin endpoints.
// It is mounted under /api/plugins/hive inside the existing authenticated
// chi group, so it inherits auth middleware — no separate auth here.
func Router(store *Store) chi.Router {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Post("/epic-nodes", handleCreateEpicNode(store))
	r.Get("/epic-nodes/{id}", handleGetEpicNode(store))

	return r
}

type createEpicNodeRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	EpicID      string          `json:"epic_id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
}

func handleCreateEpicNode(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createEpicNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" || req.EpicID == "" {
			http.Error(w, `{"error":"workspace_id and epic_id are required"}`, http.StatusBadRequest)
			return
		}

		payload := []byte(req.Payload)
		if len(payload) == 0 {
			payload = []byte("{}")
		}

		node, err := store.CreateEpicNode(r.Context(), req.WorkspaceID, req.EpicID, req.Kind, payload)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, node)
	}
}

func handleGetEpicNode(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		node, err := store.GetEpicNode(r.Context(), id)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, node)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
