package hive

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EpicNode is a single row from hive.epic_nodes.
type EpicNode struct {
	ID          string
	WorkspaceID string
	EpicID      string
	Kind        string
	Payload     []byte
}

// Store is the typed boundary over hive.* tables.
// It only ever touches the hive schema — never the core public schema.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateEpicNode inserts one row into hive.epic_nodes and returns the new row.
func (s *Store) CreateEpicNode(ctx context.Context, workspaceID, epicID, kind string, payload []byte) (EpicNode, error) {
	if kind == "" {
		kind = "epic"
	}
	if payload == nil {
		payload = []byte("{}")
	}

	var node EpicNode
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.epic_nodes (workspace_id, epic_id, kind, payload)
		VALUES ($1::uuid, $2, $3, $4::jsonb)
		RETURNING id::text, workspace_id::text, epic_id, kind, payload
	`, workspaceID, epicID, kind, payload).Scan(
		&node.ID, &node.WorkspaceID, &node.EpicID, &node.Kind, &node.Payload,
	)
	if err != nil {
		return EpicNode{}, fmt.Errorf("hive: create epic node: %w", err)
	}
	return node, nil
}

// GetEpicNode fetches one row from hive.epic_nodes by UUID.
func (s *Store) GetEpicNode(ctx context.Context, id string) (EpicNode, error) {
	var node EpicNode
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, epic_id, kind, payload
		FROM hive.epic_nodes
		WHERE id = $1::uuid
	`, id).Scan(
		&node.ID, &node.WorkspaceID, &node.EpicID, &node.Kind, &node.Payload,
	)
	if err != nil {
		return EpicNode{}, fmt.Errorf("hive: get epic node %s: %w", id, err)
	}
	return node, nil
}

// ReviewGate is a single row from hive.review_gates.
type ReviewGate struct {
	ID          string
	WorkspaceID string
	EpicID      string
	GateKey     string
	State       string
	Evidence    []byte
	UpdatedBy   string
}

// ListReviewGates returns all gates for a given workspace + epic.
func (s *Store) ListReviewGates(ctx context.Context, workspaceID, epicID string) ([]ReviewGate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
		FROM hive.review_gates
		WHERE workspace_id = $1::uuid AND epic_id = $2
		ORDER BY created_at
	`, workspaceID, epicID)
	if err != nil {
		return nil, fmt.Errorf("hive: list review gates: %w", err)
	}
	defer rows.Close()

	var gates []ReviewGate
	for rows.Next() {
		var g ReviewGate
		if err := rows.Scan(&g.ID, &g.WorkspaceID, &g.EpicID, &g.GateKey, &g.State, &g.Evidence, &g.UpdatedBy); err != nil {
			return nil, fmt.Errorf("hive: scan review gate: %w", err)
		}
		gates = append(gates, g)
	}
	return gates, rows.Err()
}

// GetReviewGate fetches one row from hive.review_gates by UUID.
func (s *Store) GetReviewGate(ctx context.Context, id string) (ReviewGate, error) {
	var g ReviewGate
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
		FROM hive.review_gates
		WHERE id = $1::uuid
	`, id).Scan(&g.ID, &g.WorkspaceID, &g.EpicID, &g.GateKey, &g.State, &g.Evidence, &g.UpdatedBy)
	if err != nil {
		return ReviewGate{}, fmt.Errorf("hive: get review gate %s: %w", id, err)
	}
	return g, nil
}

// UpdateReviewGate sets state, evidence, and updated_by on an existing gate.
func (s *Store) UpdateReviewGate(ctx context.Context, id, state, updatedBy string, evidence []byte) (ReviewGate, error) {
	if evidence == nil {
		evidence = []byte("{}")
	}
	var g ReviewGate
	err := s.pool.QueryRow(ctx, `
		UPDATE hive.review_gates
		SET state = $2, evidence = $3::jsonb, updated_by = $4, updated_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
	`, id, state, evidence, updatedBy).Scan(
		&g.ID, &g.WorkspaceID, &g.EpicID, &g.GateKey, &g.State, &g.Evidence, &g.UpdatedBy,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ReviewGate{}, fmt.Errorf("hive: review gate %s: not found", id)
		}
		return ReviewGate{}, fmt.Errorf("hive: update review gate %s: %w", id, err)
	}
	return g, nil
}

// PersonalQueueItem is a single row from hive.personal_queue_items.
type PersonalQueueItem struct {
	ID          string
	WorkspaceID string
	AssigneeID  string
	RefKind     string
	RefID       string
	Title       string
	Status      string
	Meta        []byte
}

// ListPersonalQueueItems returns all queue items for a given workspace + assignee.
// Both predicates are required — a user never sees another user's items.
func (s *Store) ListPersonalQueueItems(ctx context.Context, workspaceID, assigneeID string) ([]PersonalQueueItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, workspace_id::text, assignee_id::text, ref_kind, ref_id, title, status, meta
		FROM hive.personal_queue_items
		WHERE workspace_id = $1::uuid AND assignee_id = $2::uuid
		ORDER BY created_at
	`, workspaceID, assigneeID)
	if err != nil {
		return nil, fmt.Errorf("hive: list personal queue items: %w", err)
	}
	defer rows.Close()

	var items []PersonalQueueItem
	for rows.Next() {
		var it PersonalQueueItem
		if err := rows.Scan(&it.ID, &it.WorkspaceID, &it.AssigneeID, &it.RefKind, &it.RefID, &it.Title, &it.Status, &it.Meta); err != nil {
			return nil, fmt.Errorf("hive: scan personal queue item: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// CreatePersonalQueueItem inserts a new queue item for the given assignee.
func (s *Store) CreatePersonalQueueItem(ctx context.Context, workspaceID, assigneeID, refKind, refID, title string, meta []byte) (PersonalQueueItem, error) {
	if meta == nil {
		meta = []byte("{}")
	}
	var it PersonalQueueItem
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.personal_queue_items (workspace_id, assignee_id, ref_kind, ref_id, title, meta)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb)
		RETURNING id::text, workspace_id::text, assignee_id::text, ref_kind, ref_id, title, status, meta
	`, workspaceID, assigneeID, refKind, refID, title, meta).Scan(
		&it.ID, &it.WorkspaceID, &it.AssigneeID, &it.RefKind, &it.RefID, &it.Title, &it.Status, &it.Meta,
	)
	if err != nil {
		return PersonalQueueItem{}, fmt.Errorf("hive: create personal queue item: %w", err)
	}
	return it, nil
}

// UpdatePersonalQueueItem updates status and meta on an existing item.
// The assigneeID predicate enforces that only the item's owner may update it —
// the UPDATE matches zero rows if assignee_id differs, returning pgx.ErrNoRows.
func (s *Store) UpdatePersonalQueueItem(ctx context.Context, id, assigneeID, status string, meta []byte) (PersonalQueueItem, error) {
	if meta == nil {
		meta = []byte("{}")
	}
	var it PersonalQueueItem
	err := s.pool.QueryRow(ctx, `
		UPDATE hive.personal_queue_items
		SET status = $3, meta = $4::jsonb, updated_at = now()
		WHERE id = $1::uuid AND assignee_id = $2::uuid
		RETURNING id::text, workspace_id::text, assignee_id::text, ref_kind, ref_id, title, status, meta
	`, id, assigneeID, status, meta).Scan(
		&it.ID, &it.WorkspaceID, &it.AssigneeID, &it.RefKind, &it.RefID, &it.Title, &it.Status, &it.Meta,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return PersonalQueueItem{}, fmt.Errorf("hive: personal queue item %s: not found or not authorized", id)
		}
		return PersonalQueueItem{}, fmt.Errorf("hive: update personal queue item %s: %w", id, err)
	}
	return it, nil
}

// HermesThread is a single row from hive.hermes_threads.
type HermesThread struct {
	ID          string
	WorkspaceID string
	Title       string
	CreatedBy   string
	CreatedAt   string
}

// HermesMessage is a single row from hive.hermes_messages.
type HermesMessage struct {
	ID          string
	ThreadID    string
	WorkspaceID string
	AuthorID    string
	Body        string
	CreatedAt   string
}

// ListThreads returns all threads for a workspace, newest first.
func (s *Store) ListThreads(ctx context.Context, workspaceID string) ([]HermesThread, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, workspace_id::text, title, created_by, created_at::text
		FROM hive.hermes_threads
		WHERE workspace_id = $1::uuid
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("hive: list threads: %w", err)
	}
	defer rows.Close()

	var threads []HermesThread
	for rows.Next() {
		var t HermesThread
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Title, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("hive: scan thread: %w", err)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// CreateThread inserts a new thread row.
func (s *Store) CreateThread(ctx context.Context, workspaceID, title, createdBy string) (HermesThread, error) {
	var t HermesThread
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.hermes_threads (workspace_id, title, created_by)
		VALUES ($1::uuid, $2, $3)
		RETURNING id::text, workspace_id::text, title, created_by, created_at::text
	`, workspaceID, title, createdBy).Scan(
		&t.ID, &t.WorkspaceID, &t.Title, &t.CreatedBy, &t.CreatedAt,
	)
	if err != nil {
		return HermesThread{}, fmt.Errorf("hive: create thread: %w", err)
	}
	return t, nil
}

// ListMessages returns up to limit messages for a thread in DESC order (newest first).
// If before is non-empty it is used as a created_at upper-bound (exclusive) for pagination.
// limit is clamped to 100.
func (s *Store) ListMessages(ctx context.Context, threadID, before string, limit int) ([]HermesMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	var rows pgx.Rows
	var qErr error

	if before != "" {
		rows, qErr = s.pool.Query(ctx, `
			SELECT id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text
			FROM hive.hermes_messages
			WHERE thread_id = $1::uuid AND created_at < $2::timestamptz
			ORDER BY created_at DESC
			LIMIT $3
		`, threadID, before, limit)
	} else {
		rows, qErr = s.pool.Query(ctx, `
			SELECT id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text
			FROM hive.hermes_messages
			WHERE thread_id = $1::uuid
			ORDER BY created_at DESC
			LIMIT $2
		`, threadID, limit)
	}
	if qErr != nil {
		return nil, fmt.Errorf("hive: list messages: %w", qErr)
	}
	defer rows.Close()

	var msgs []HermesMessage
	for rows.Next() {
		var m HermesMessage
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.WorkspaceID, &m.AuthorID, &m.Body, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("hive: scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// CreateMessage inserts a new message into a thread.
func (s *Store) CreateMessage(ctx context.Context, threadID, workspaceID, authorID, body string) (HermesMessage, error) {
	var m HermesMessage
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.hermes_messages (thread_id, workspace_id, author_id, body)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		RETURNING id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text
	`, threadID, workspaceID, authorID, body).Scan(
		&m.ID, &m.ThreadID, &m.WorkspaceID, &m.AuthorID, &m.Body, &m.CreatedAt,
	)
	if err != nil {
		return HermesMessage{}, fmt.Errorf("hive: create message: %w", err)
	}
	return m, nil
}

// PluginSkillCatalogState is a single row from hive.plugin_skill_catalog_state.
type PluginSkillCatalogState struct {
	ID             string
	WorkspaceID    string
	CatalogVersion string
	LastBrowsedAt  *string // nil if never browsed
}

// GetOrCreateCatalogState returns the catalog state row for the workspace,
// inserting a default row if none exists yet.
func (s *Store) GetOrCreateCatalogState(ctx context.Context, workspaceID string) (PluginSkillCatalogState, error) {
	var st PluginSkillCatalogState
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.plugin_skill_catalog_state (workspace_id)
		VALUES ($1::uuid)
		ON CONFLICT (workspace_id) DO UPDATE
			SET workspace_id = EXCLUDED.workspace_id
		RETURNING id::text, workspace_id::text, catalog_version,
		          last_browsed_at::text
	`, workspaceID).Scan(&st.ID, &st.WorkspaceID, &st.CatalogVersion, &st.LastBrowsedAt)
	if err != nil {
		return PluginSkillCatalogState{}, fmt.Errorf("hive: get or create catalog state: %w", err)
	}
	return st, nil
}

// TouchCatalogBrowse updates last_browsed_at and catalog_version for the
// workspace's catalog state row (upserts if missing). Called on each browse.
func (s *Store) TouchCatalogBrowse(ctx context.Context, workspaceID, catalogVersion string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hive.plugin_skill_catalog_state (workspace_id, catalog_version, last_browsed_at)
		VALUES ($1::uuid, $2, now())
		ON CONFLICT (workspace_id) DO UPDATE
			SET catalog_version  = EXCLUDED.catalog_version,
			    last_browsed_at  = now(),
			    updated_at       = now()
	`, workspaceID, catalogVersion)
	if err != nil {
		return fmt.Errorf("hive: touch catalog browse: %w", err)
	}
	return nil
}

// CreateReviewGate inserts a new gate row (upsert on workspace+epic+key).
func (s *Store) CreateReviewGate(ctx context.Context, workspaceID, epicID, gateKey, state, updatedBy string, evidence []byte) (ReviewGate, error) {
	if evidence == nil {
		evidence = []byte("{}")
	}
	if state == "" {
		state = "pending"
	}
	var g ReviewGate
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.review_gates (workspace_id, epic_id, gate_key, state, evidence, updated_by)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (workspace_id, epic_id, gate_key) DO UPDATE
			SET state = EXCLUDED.state, evidence = EXCLUDED.evidence,
			    updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
	`, workspaceID, epicID, gateKey, state, evidence, updatedBy).Scan(
		&g.ID, &g.WorkspaceID, &g.EpicID, &g.GateKey, &g.State, &g.Evidence, &g.UpdatedBy,
	)
	if err != nil {
		return ReviewGate{}, fmt.Errorf("hive: create review gate: %w", err)
	}
	return g, nil
}
