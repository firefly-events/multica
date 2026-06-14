package hive

import (
	"context"
	"fmt"

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
