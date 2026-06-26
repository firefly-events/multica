package hive

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// GetEpicNode fetches one row from hive.epic_nodes by UUID, scoped to workspaceID.
func (s *Store) GetEpicNode(ctx context.Context, workspaceID, id string) (EpicNode, error) {
	var node EpicNode
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, epic_id, kind, payload
		FROM hive.epic_nodes
		WHERE id = $1::uuid AND workspace_id = $2::uuid
	`, id, workspaceID).Scan(
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

// GetReviewGate fetches one row from hive.review_gates by UUID, scoped to workspaceID.
func (s *Store) GetReviewGate(ctx context.Context, workspaceID, id string) (ReviewGate, error) {
	var g ReviewGate
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
		FROM hive.review_gates
		WHERE id = $1::uuid AND workspace_id = $2::uuid
	`, id, workspaceID).Scan(&g.ID, &g.WorkspaceID, &g.EpicID, &g.GateKey, &g.State, &g.Evidence, &g.UpdatedBy)
	if err != nil {
		return ReviewGate{}, fmt.Errorf("hive: get review gate %s: %w", id, err)
	}
	return g, nil
}

// UpdateReviewGate sets state, evidence, and updated_by on an existing gate,
// scoped to workspaceID to prevent cross-workspace mutations.
func (s *Store) UpdateReviewGate(ctx context.Context, workspaceID, id, state, updatedBy string, evidence []byte) (ReviewGate, error) {
	if evidence == nil {
		evidence = []byte("{}")
	}
	var g ReviewGate
	err := s.pool.QueryRow(ctx, `
		UPDATE hive.review_gates
		SET state = $3, evidence = $4::jsonb, updated_by = $5, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid
		RETURNING id::text, workspace_id::text, epic_id, gate_key, state, evidence, updated_by
	`, id, workspaceID, state, evidence, updatedBy).Scan(
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
	Model       *string
	TokensTotal *int
}

// HermesMessage is a single row from hive.hermes_messages.
type HermesMessage struct {
	ID            string
	ThreadID      string
	WorkspaceID   string
	AuthorID      string
	Body          string
	CreatedAt     string
	Role          string
	TokensUsed    *int
	ContextWindow *int
}

// ListThreads returns all threads for a workspace, newest first.
func (s *Store) ListThreads(ctx context.Context, workspaceID string) ([]HermesThread, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, workspace_id::text, title, created_by, created_at::text, model, tokens_total
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
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Title, &t.CreatedBy, &t.CreatedAt, &t.Model, &t.TokensTotal); err != nil {
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
		RETURNING id::text, workspace_id::text, title, created_by, created_at::text, model, tokens_total
	`, workspaceID, title, createdBy).Scan(
		&t.ID, &t.WorkspaceID, &t.Title, &t.CreatedBy, &t.CreatedAt, &t.Model, &t.TokensTotal,
	)
	if err != nil {
		return HermesThread{}, fmt.Errorf("hive: create thread: %w", err)
	}
	return t, nil
}

// ListMessages returns up to limit messages for a thread in DESC order (newest first),
// scoped to workspaceID to prevent cross-workspace reads. beforeTS and beforeID form
// a tuple cursor — both must be non-empty to activate — using
// (created_at, id) < (beforeTS, beforeID) for deterministic pagination that
// handles ties on created_at. limit is clamped to 100.
func (s *Store) ListMessages(ctx context.Context, workspaceID, threadID, beforeTS, beforeID string, limit int) ([]HermesMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	var rows pgx.Rows
	var qErr error

	if beforeTS != "" && beforeID != "" {
		rows, qErr = s.pool.Query(ctx, `
			SELECT id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text, role, tokens_used, context_window
			FROM hive.hermes_messages
			WHERE thread_id = $1::uuid
			  AND workspace_id = $2::uuid
			  AND (created_at, id) < ($3::timestamptz, $4::uuid)
			ORDER BY created_at DESC, id DESC
			LIMIT $5
		`, threadID, workspaceID, beforeTS, beforeID, limit)
	} else {
		rows, qErr = s.pool.Query(ctx, `
			SELECT id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text, role, tokens_used, context_window
			FROM hive.hermes_messages
			WHERE thread_id = $1::uuid
			  AND workspace_id = $2::uuid
			ORDER BY created_at DESC, id DESC
			LIMIT $3
		`, threadID, workspaceID, limit)
	}
	if qErr != nil {
		return nil, fmt.Errorf("hive: list messages: %w", qErr)
	}
	defer rows.Close()

	var msgs []HermesMessage
	for rows.Next() {
		var m HermesMessage
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.WorkspaceID, &m.AuthorID, &m.Body, &m.CreatedAt, &m.Role, &m.TokensUsed, &m.ContextWindow); err != nil {
			return nil, fmt.Errorf("hive: scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ErrThreadNotInWorkspace is returned when the target thread does not belong
// to the caller's workspace, preventing cross-workspace message insertion.
var ErrThreadNotInWorkspace = errors.New("hive: thread not found in workspace")

// CreateMessage inserts a new message into a thread. It atomically verifies that
// the thread belongs to workspaceID before inserting, preventing cross-workspace writes.
// role defaults to "assistant" if empty. tokensUsed and contextWindow are optional
// (nil leaves the DB column at its default/NULL).
func (s *Store) CreateMessage(ctx context.Context, workspaceID, threadID, authorID, body, role string, tokensUsed, contextWindow *int) (HermesMessage, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "assistant"
	}
	var m HermesMessage
	// The INSERT selects workspace_id directly from hermes_threads scoped by both
	// thread id and workspace, so a thread in another workspace returns zero rows.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hive.hermes_messages (thread_id, workspace_id, author_id, body, role, tokens_used, context_window)
		SELECT $1::uuid, t.workspace_id, $3, $4, $5, $6, $7
		FROM hive.hermes_threads t
		WHERE t.id = $1::uuid AND t.workspace_id = $2::uuid
		RETURNING id::text, thread_id::text, workspace_id::text, author_id, body, created_at::text, role, tokens_used, context_window
	`, threadID, workspaceID, authorID, body, role, tokensUsed, contextWindow).Scan(
		&m.ID, &m.ThreadID, &m.WorkspaceID, &m.AuthorID, &m.Body, &m.CreatedAt, &m.Role, &m.TokensUsed, &m.ContextWindow,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return HermesMessage{}, ErrThreadNotInWorkspace
		}
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

// ErrCatalogSkillCollision is returned when a skill with the same name exists
// and the caller did not request an overwrite.
var ErrCatalogSkillCollision = errors.New("hive: skill name collision")

// CatalogMaterialization is a single row from hive.plugin_skill_catalog_materializations.
type CatalogMaterialization struct {
	ID             string
	WorkspaceID    string
	SkillID        string
	CatalogKey     string
	CatalogVersion string
	State          string
	MaterializedBy string
	CreatedAt      string
	UpdatedAt      string
}

// MaterializeCatalogSkillParams is the input for MaterializeCatalogSkill.
type MaterializeCatalogSkillParams struct {
	WorkspaceID    string
	CatalogKey     string
	CatalogVersion string
	SkillName      string
	Description    string
	Content        string
	Config         []byte // JSONB for public.skill.config
	MaterializedBy string // user/agent UUID; empty → NULL creator
	Overwrite      bool
}

// isUniqueViolation returns true when err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// MaterializeCatalogSkill materializes a catalog skill into Multica's native
// public.skill + public.skill_file rows and records provenance in
// hive.plugin_skill_catalog_materializations, all within one transaction.
//
// Name collision (workspace_id, name) in public.skill returns
// ErrCatalogSkillCollision unless Overwrite is true. ON CONFLICT is used for the
// skill INSERT so concurrent callers never race into a 500 on unique violation.
func (s *Store) MaterializeCatalogSkill(ctx context.Context, p MaterializeCatalogSkillParams) (CatalogMaterialization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if !isValidFilePath("SKILL.md") {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: invalid file path")
	}

	var skillID string
	if p.Overwrite {
		// Upsert: insert or update on name collision.
		err = tx.QueryRow(ctx, `
			INSERT INTO skill (workspace_id, name, description, content, config, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5::jsonb, NULLIF($6, '')::uuid)
			ON CONFLICT (workspace_id, name) DO UPDATE
				SET description = EXCLUDED.description,
				    content     = EXCLUDED.content,
				    config      = EXCLUDED.config,
				    updated_at  = now()
			RETURNING id::text
		`, p.WorkspaceID, p.SkillName, p.Description, p.Content, p.Config, p.MaterializedBy).Scan(&skillID)
	} else {
		// Insert only; DO NOTHING on collision → ErrNoRows → 409.
		err = tx.QueryRow(ctx, `
			INSERT INTO skill (workspace_id, name, description, content, config, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5::jsonb, NULLIF($6, '')::uuid)
			ON CONFLICT (workspace_id, name) DO NOTHING
			RETURNING id::text
		`, p.WorkspaceID, p.SkillName, p.Description, p.Content, p.Config, p.MaterializedBy).Scan(&skillID)
		if err == pgx.ErrNoRows || isUniqueViolation(err) {
			return CatalogMaterialization{}, ErrCatalogSkillCollision
		}
	}
	if err != nil {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: upsert skill: %w", err)
	}

	// Upsert the SKILL.md file (works for both insert and overwrite paths).
	_, err = tx.Exec(ctx, `
		INSERT INTO skill_file (skill_id, path, content)
		VALUES ($1::uuid, 'SKILL.md', $2)
		ON CONFLICT (skill_id, path) DO UPDATE
			SET content = EXCLUDED.content, updated_at = now()
	`, skillID, p.Content)
	if err != nil {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: upsert skill_file: %w", err)
	}

	// Upsert provenance in hive schema.
	var mat CatalogMaterialization
	err = tx.QueryRow(ctx, `
		INSERT INTO hive.plugin_skill_catalog_materializations
			(workspace_id, skill_id, catalog_key, catalog_version, state, materialized_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'active', $5)
		ON CONFLICT (workspace_id, catalog_key) DO UPDATE
			SET skill_id        = EXCLUDED.skill_id,
			    catalog_version = EXCLUDED.catalog_version,
			    materialized_by = EXCLUDED.materialized_by,
			    updated_at      = now()
		RETURNING id::text, workspace_id::text, skill_id::text, catalog_key,
		          catalog_version, state, materialized_by,
		          created_at::text, updated_at::text
	`, p.WorkspaceID, skillID, p.CatalogKey, p.CatalogVersion, p.MaterializedBy).Scan(
		&mat.ID, &mat.WorkspaceID, &mat.SkillID, &mat.CatalogKey,
		&mat.CatalogVersion, &mat.State, &mat.MaterializedBy,
		&mat.CreatedAt, &mat.UpdatedAt,
	)
	if err != nil {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: upsert provenance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CatalogMaterialization{}, fmt.Errorf("hive: materialize catalog skill: commit: %w", err)
	}

	return mat, nil
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
