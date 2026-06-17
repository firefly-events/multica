package hive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// Router returns a chi sub-router for all Hive plugin endpoints.
// It is mounted inside the RequireWorkspaceMember group in router.go,
// so every route requires both authentication and workspace membership.
// hub is used to publish realtime events; pass nil to disable publishing.
func Router(store *Store, hub realtime.Broadcaster) chi.Router {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "ok"})
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
	r.Get("/hermes-bridge-status", handleGetHermesBridgeStatus())

	r.Get("/skill-catalog", handleGetSkillCatalog(store))
	r.Post("/skill-catalog/materialize", handleMaterializeCatalogSkill(store))

	return r
}

// trustedWorkspaceID returns the workspace ID from the middleware-injected context.
// Returns ("", false) when no workspace ID is in context (should not happen under
// RequireWorkspaceMember, but we guard defensively).
func trustedWorkspaceID(r *http.Request) (string, bool) {
	id := middleware.WorkspaceIDFromContext(r.Context())
	return id, id != ""
}

// rejectWorkspaceMismatch writes a 403 and returns false when clientID is non-empty
// and differs from ctxID. Call before using any client-supplied workspace_id.
func rejectWorkspaceMismatch(w http.ResponseWriter, ctxID, clientID string) bool {
	if clientID != "" && clientID != ctxID {
		http.Error(w, `{"error":"workspace_id mismatch"}`, http.StatusForbidden)
		return false
	}
	return true
}

type createEpicNodeRequest struct {
	WorkspaceID string          `json:"workspace_id"`
	EpicID      string          `json:"epic_id"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
}

func handleCreateEpicNode(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		var req createEpicNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}
		if req.EpicID == "" {
			http.Error(w, `{"error":"epic_id is required"}`, http.StatusBadRequest)
			return
		}

		payload := []byte(req.Payload)
		if len(payload) == 0 {
			payload = []byte("{}")
		}

		node, err := store.CreateEpicNode(r.Context(), wsID, req.EpicID, req.Kind, payload)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, node)
	}
}

func handleGetEpicNode(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}

		node, err := store.GetEpicNode(r.Context(), wsID, id)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, node)
	}
}

func handleListReviewGates(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
			return
		}
		epicID := r.URL.Query().Get("epic_id")
		if epicID == "" {
			http.Error(w, `{"error":"epic_id is required"}`, http.StatusBadRequest)
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
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		var req createReviewGateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}
		if req.EpicID == "" || req.GateKey == "" {
			http.Error(w, `{"error":"epic_id and gate_key are required"}`, http.StatusBadRequest)
			return
		}

		// updatedBy comes from the authenticated user, not the request body.
		updatedBy := r.Header.Get("X-User-ID")
		evidence := []byte(req.Evidence)
		gate, err := store.CreateReviewGate(r.Context(), wsID, req.EpicID, req.GateKey, req.State, updatedBy, evidence)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, gate)
	}
}

func handleGetReviewGate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
			return
		}
		gate, err := store.GetReviewGate(r.Context(), wsID, id)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, gate)
	}
}

type updateReviewGateRequest struct {
	State    string          `json:"state"`
	Evidence json.RawMessage `json:"evidence"`
}

func handleUpdateReviewGate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

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

		updatedBy := r.Header.Get("X-User-ID")
		evidence := []byte(req.Evidence)
		gate, err := store.UpdateReviewGate(r.Context(), wsID, id, req.State, updatedBy, evidence)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, gate)
	}
}

func handleListPersonalQueueItems(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
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
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

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
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}
		meta := []byte(req.Meta)
		item, err := store.CreatePersonalQueueItem(r.Context(), wsID, userID, req.RefKind, req.RefID, req.Title, meta)
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
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
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
}

func handleCreateThread(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		var req createThreadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}

		// Derive creator from authenticated user, not request body.
		createdBy := r.Header.Get("X-User-ID")
		thread, err := store.CreateThread(r.Context(), wsID, req.Title, createdBy)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, thread)
	}
}

func handleListMessages(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
			return
		}
		threadID := r.URL.Query().Get("thread_id")
		if threadID == "" {
			http.Error(w, `{"error":"thread_id is required"}`, http.StatusBadRequest)
			return
		}
		beforeTS := r.URL.Query().Get("before")
		beforeID := r.URL.Query().Get("before_id")
		if (beforeTS == "") != (beforeID == "") {
			http.Error(w, `{"error":"before and before_id must be provided together"}`, http.StatusBadRequest)
			return
		}
		limit := 30
		if ls := r.URL.Query().Get("limit"); ls != "" {
			if n, err := strconv.Atoi(ls); err == nil && n > 0 {
				limit = n
			}
		}
		msgs, err := store.ListMessages(r.Context(), wsID, threadID, beforeTS, beforeID, limit)
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
	Body        string `json:"body"`
}

func handleCreateMessage(store *Store, hub realtime.Broadcaster) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		var req createMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}
		if req.ThreadID == "" {
			http.Error(w, `{"error":"thread_id is required"}`, http.StatusBadRequest)
			return
		}
		if req.Body == "" {
			http.Error(w, `{"error":"body is required"}`, http.StatusBadRequest)
			return
		}

		// Derive author from authenticated user, not request body.
		authorID := r.Header.Get("X-User-ID")
		msg, err := store.CreateMessage(r.Context(), wsID, req.ThreadID, authorID, req.Body)
		if err != nil {
			if errors.Is(err, ErrThreadNotInWorkspace) {
				http.Error(w, `{"error":"thread not found"}`, http.StatusNotFound)
				return
			}
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
				hub.BroadcastToWorkspace(wsID, data)
			}
		}
		writeJSON(w, http.StatusCreated, msg)
	}
}

func handleGetHermesBridgeStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
			return
		}

		threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
		writeJSON(w, http.StatusOK, readHermesBridgeStatus(wsID, threadID))
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
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		if !rejectWorkspaceMismatch(w, wsID, r.URL.Query().Get("workspace_id")) {
			return
		}

		// Record browse event asynchronously with a bounded timeout so it never
		// blocks the response and does not leak goroutines under DB stalls.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = store.TouchCatalogBrowse(ctx, wsID, catalog.Version)
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

// isValidFilePath mirrors the validateFilePath logic from server/internal/handler/skill.go
// so catalog materialization applies the same path-safety rules without creating
// a cross-package import cycle.
func isValidFilePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	cleaned := filepath.Clean(p)
	return !strings.HasPrefix(cleaned, "..")
}

// buildCatalogSkillContent constructs the SKILL.md body for a catalog skill.
func buildCatalogSkillContent(s CatalogSkill) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nversion: %s\n---\n\n%s",
		s.Name, s.Description, s.Version, s.WhenToUse)
}

type materializeCatalogSkillRequest struct {
	WorkspaceID string `json:"workspace_id"`
	CatalogKey  string `json:"catalog_key"`
	Overwrite   bool   `json:"overwrite"`
}

func handleMaterializeCatalogSkill(store *Store) http.HandlerFunc {
	catalog := loadCatalog()
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, ok := trustedWorkspaceID(r)
		if !ok {
			http.Error(w, `{"error":"workspace context missing"}`, http.StatusUnauthorized)
			return
		}

		var req materializeCatalogSkillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if !rejectWorkspaceMismatch(w, wsID, req.WorkspaceID) {
			return
		}
		if req.CatalogKey == "" {
			http.Error(w, `{"error":"catalog_key is required"}`, http.StatusBadRequest)
			return
		}

		// Locate catalog skill by name (catalog_key is the skill Name).
		var found *CatalogSkill
		for i := range catalog.Skills {
			if catalog.Skills[i].Name == req.CatalogKey {
				found = &catalog.Skills[i]
				break
			}
		}
		if found == nil {
			http.Error(w, `{"error":"catalog skill not found"}`, http.StatusNotFound)
			return
		}

		materializedBy := r.Header.Get("X-User-ID")
		content := buildCatalogSkillContent(*found)
		config, _ := json.Marshal(map[string]any{
			"origin": map[string]any{
				"type":            "hive_catalog",
				"catalog_key":     found.Name,
				"catalog_version": catalog.Version,
			},
		})

		mat, err := store.MaterializeCatalogSkill(r.Context(), MaterializeCatalogSkillParams{
			WorkspaceID:    wsID,
			CatalogKey:     found.Name,
			CatalogVersion: catalog.Version,
			SkillName:      found.Name,
			Description:    found.Description,
			Content:        content,
			Config:         config,
			MaterializedBy: materializedBy,
			Overwrite:      req.Overwrite,
		})
		if err != nil {
			if errors.Is(err, ErrCatalogSkillCollision) {
				http.Error(w, `{"error":"skill with this name already exists; set overwrite=true to replace"}`, http.StatusConflict)
				return
			}
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, mat)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
