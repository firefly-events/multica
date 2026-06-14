package hive

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// Router returns a chi sub-router for all Hive plugin endpoints.
// It is mounted under /api/plugins/hive inside the existing authenticated
// chi group, so it inherits auth middleware — no separate auth here.
// hub is used to publish realtime events; pass nil to disable publishing.
func Router(store *Store, hub realtime.Broadcaster) chi.Router {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Post("/epic-nodes", handleCreateEpicNode(store))
	r.Get("/epic-nodes/{id}", handleGetEpicNode(store))

	r.Get("/review-gates", handleListReviewGates(store))
	r.Post("/review-gates", handleCreateReviewGate(store))
	r.Get("/review-gates/{id}", handleGetReviewGate(store))
	r.Patch("/review-gates/{id}", handleUpdateReviewGate(store))

	r.Get("/personal-queue-items", handleListPersonalQueueItems(store))
	r.Post("/personal-queue-items", handleCreatePersonalQueueItem(store))
	r.Patch("/personal-queue-items/{id}", handleUpdatePersonalQueueItem(store))

	r.Get("/hermes-threads", handleListThreads(store))
	r.Post("/hermes-threads", handleCreateThread(store))
	r.Get("/hermes-messages", handleListMessages(store))
	r.Post("/hermes-messages", handleCreateMessage(store, hub))

	r.Get("/skill-catalog", handleGetSkillCatalog(store))

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

func handleListReviewGates(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		epicID := r.URL.Query().Get("epic_id")
		if wsID == "" || epicID == "" {
			http.Error(w, `{"error":"workspace_id and epic_id are required"}`, http.StatusBadRequest)
			return
		}
		gates, err := store.ListReviewGates(r.Context(), wsID, epicID)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if gates == nil {
			gates = []ReviewGate{}
		}
		writeJSON(w, http.StatusOK, gates)
	}
}

type createReviewGateRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	EpicID      string          `json:"epic_id"`
	GateKey     string          `json:"gate_key"`
	State       string          `json:"state"`
	UpdatedBy   string          `json:"updated_by"`
	Evidence    json.RawMessage `json:"evidence"`
}

func handleCreateReviewGate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createReviewGateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" || req.EpicID == "" || req.GateKey == "" {
			http.Error(w, `{"error":"workspace_id, epic_id, and gate_key are required"}`, http.StatusBadRequest)
			return
		}
		evidence := []byte(req.Evidence)
		gate, err := store.CreateReviewGate(r.Context(), req.WorkspaceID, req.EpicID, req.GateKey, req.State, req.UpdatedBy, evidence)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, gate)
	}
}

func handleGetReviewGate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}
		gate, err := store.GetReviewGate(r.Context(), id)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, gate)
	}
}

type updateReviewGateRequest struct {
	State     string          `json:"state"`
	UpdatedBy string          `json:"updated_by"`
	Evidence  json.RawMessage `json:"evidence"`
}

func handleUpdateReviewGate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}
		var req updateReviewGateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.State == "" {
			http.Error(w, `{"error":"state is required"}`, http.StatusBadRequest)
			return
		}
		evidence := []byte(req.Evidence)
		gate, err := store.UpdateReviewGate(r.Context(), id, req.State, req.UpdatedBy, evidence)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, gate)
	}
}

func handleListPersonalQueueItems(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		if wsID == "" {
			http.Error(w, `{"error":"workspace_id is required"}`, http.StatusBadRequest)
			return
		}
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		items, err := store.ListPersonalQueueItems(r.Context(), wsID, userID)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if items == nil {
			items = []PersonalQueueItem{}
		}
		writeJSON(w, http.StatusOK, items)
	}
}

type createPersonalQueueItemRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	RefKind     string          `json:"ref_kind"`
	RefID       string          `json:"ref_id"`
	Title       string          `json:"title"`
	Meta        json.RawMessage `json:"meta"`
}

func handleCreatePersonalQueueItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req createPersonalQueueItemRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" {
			http.Error(w, `{"error":"workspace_id is required"}`, http.StatusBadRequest)
			return
		}
		meta := []byte(req.Meta)
		item, err := store.CreatePersonalQueueItem(r.Context(), req.WorkspaceID, userID, req.RefKind, req.RefID, req.Title, meta)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, item)
	}
}

type updatePersonalQueueItemRequest struct {
	Status string          `json:"status"`
	Meta   json.RawMessage `json:"meta"`
}

func handleUpdatePersonalQueueItem(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req updatePersonalQueueItemRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.Status == "" {
			http.Error(w, `{"error":"status is required"}`, http.StatusBadRequest)
			return
		}
		meta := []byte(req.Meta)
		item, err := store.UpdatePersonalQueueItem(r.Context(), id, userID, req.Status, meta)
		if err != nil {
			http.Error(w, `{"error":"not found or not authorized"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func handleListThreads(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		if wsID == "" {
			http.Error(w, `{"error":"workspace_id is required"}`, http.StatusBadRequest)
			return
		}
		threads, err := store.ListThreads(r.Context(), wsID)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if threads == nil {
			threads = []HermesThread{}
		}
		writeJSON(w, http.StatusOK, threads)
	}
}

type createThreadRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	CreatedBy   string `json:"created_by"`
}

func handleCreateThread(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createThreadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" {
			http.Error(w, `{"error":"workspace_id is required"}`, http.StatusBadRequest)
			return
		}
		thread, err := store.CreateThread(r.Context(), req.WorkspaceID, req.Title, req.CreatedBy)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, thread)
	}
}

func handleListMessages(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		threadID := r.URL.Query().Get("thread_id")
		wsID := r.URL.Query().Get("workspace_id")
		if threadID == "" || wsID == "" {
			http.Error(w, `{"error":"thread_id and workspace_id are required"}`, http.StatusBadRequest)
			return
		}
		before := r.URL.Query().Get("before")
		limit := 30
		if ls := r.URL.Query().Get("limit"); ls != "" {
			if n, err := strconv.Atoi(ls); err == nil && n > 0 {
				limit = n
			}
		}
		msgs, err := store.ListMessages(r.Context(), threadID, before, limit)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if msgs == nil {
			msgs = []HermesMessage{}
		}
		writeJSON(w, http.StatusOK, msgs)
	}
}

type createMessageRequest struct {
	ThreadID    string `json:"thread_id"`
	WorkspaceID string `json:"workspace_id"`
	AuthorID    string `json:"author_id"`
	Body        string `json:"body"`
}

func handleCreateMessage(store *Store, hub realtime.Broadcaster) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if req.ThreadID == "" || req.WorkspaceID == "" {
			http.Error(w, `{"error":"thread_id and workspace_id are required"}`, http.StatusBadRequest)
			return
		}
		if req.Body == "" {
			http.Error(w, `{"error":"body is required"}`, http.StatusBadRequest)
			return
		}
		msg, err := store.CreateMessage(r.Context(), req.ThreadID, req.WorkspaceID, req.AuthorID, req.Body)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if hub != nil {
			event := map[string]any{
				"type": "hive:message:created",
				"payload": map[string]any{
					"thread_id": msg.ThreadID,
					"message":   msg,
				},
			}
			if data, jerr := json.Marshal(event); jerr == nil {
				hub.BroadcastToWorkspace(req.WorkspaceID, data)
			}
		}
		writeJSON(w, http.StatusCreated, msg)
	}
}

type skillCatalogResponse struct {
	Version string         `json:"version"`
	Skills  []CatalogSkill `json:"skills"`
	State   any            `json:"state"`
}

// handleGetSkillCatalog serves the embedded versioned Hive skill catalog.
// It is read-only and works entirely offline — no runtime or daemon needed.
// On each call it records a browse event in hive.plugin_skill_catalog_state
// without touching the core public skill/skill_file tables.
func handleGetSkillCatalog(store *Store) http.HandlerFunc {
	catalog := loadCatalog()
	return func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		if wsID == "" {
			http.Error(w, `{"error":"workspace_id is required"}`, http.StatusBadRequest)
			return
		}

		// Record browse event asynchronously so it never blocks the response.
		// Use context.Background() — r.Context() is cancelled after handler returns.
		go func() {
			_ = store.TouchCatalogBrowse(context.Background(), wsID, catalog.Version)
		}()

		state, err := store.GetOrCreateCatalogState(r.Context(), wsID)
		if err != nil {
			// Non-fatal: serve catalog even if state tracking fails.
			writeJSON(w, http.StatusOK, skillCatalogResponse{
				Version: catalog.Version,
				Skills:  catalog.Skills,
			})
			return
		}

		writeJSON(w, http.StatusOK, skillCatalogResponse{
			Version: catalog.Version,
			Skills:  catalog.Skills,
			State:   state,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
