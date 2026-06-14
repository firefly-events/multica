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
