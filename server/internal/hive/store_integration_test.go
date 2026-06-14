package hive_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/hive"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("database unavailable (%v) — skipping integration test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database ping failed (%v) — skipping integration test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestHiveMigrations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := hive.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	// Verify hive.schema_migrations table exists and the migration is recorded.
	latest, err := hive.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}

	var applied bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM hive.schema_migrations WHERE version = $1)`,
		latest,
	).Scan(&applied)
	if err != nil {
		t.Fatalf("query hive.schema_migrations: %v", err)
	}
	if !applied {
		t.Errorf("migration %q not recorded in hive.schema_migrations", latest)
	}
}

func TestEpicNodeRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := hive.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	store := hive.NewStore(pool)

	const workspaceID = "00000000-0000-0000-0000-000000000001"
	const epicID = "test-epic-roundtrip"

	created, err := store.CreateEpicNode(ctx, workspaceID, epicID, "epic", []byte(`{"test":true}`))
	if err != nil {
		t.Fatalf("CreateEpicNode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateEpicNode: empty ID returned")
	}

	fetched, err := store.GetEpicNode(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEpicNode(%q): %v", created.ID, err)
	}

	if fetched.ID != created.ID {
		t.Errorf("ID mismatch: got %q want %q", fetched.ID, created.ID)
	}
	if fetched.EpicID != epicID {
		t.Errorf("EpicID mismatch: got %q want %q", fetched.EpicID, epicID)
	}
	if fetched.Kind != "epic" {
		t.Errorf("Kind mismatch: got %q want %q", fetched.Kind, "epic")
	}

	// Cleanup
	_, _ = pool.Exec(ctx, `DELETE FROM hive.epic_nodes WHERE id = $1::uuid`, created.ID)
}

func TestReviewGateRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := hive.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	store := hive.NewStore(pool)

	const workspaceID = "00000000-0000-0000-0000-000000000002"
	const epicID = "test-epic-gates"
	const gateKey = "design-review"

	// Create
	created, err := store.CreateReviewGate(ctx, workspaceID, epicID, gateKey, "pending", "agent-1", []byte(`{"note":"initial"}`))
	if err != nil {
		t.Fatalf("CreateReviewGate: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateReviewGate: empty ID returned")
	}
	if created.State != "pending" {
		t.Errorf("State: got %q want %q", created.State, "pending")
	}

	// List
	gates, err := store.ListReviewGates(ctx, workspaceID, epicID)
	if err != nil {
		t.Fatalf("ListReviewGates: %v", err)
	}
	if len(gates) == 0 {
		t.Fatal("ListReviewGates: expected at least one gate")
	}
	found := false
	for _, g := range gates {
		if g.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("ListReviewGates: created gate %q not in results", created.ID)
	}

	// Get
	fetched, err := store.GetReviewGate(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetReviewGate: %v", err)
	}
	if fetched.GateKey != gateKey {
		t.Errorf("GateKey: got %q want %q", fetched.GateKey, gateKey)
	}

	// Update
	updated, err := store.UpdateReviewGate(ctx, created.ID, "approved", "agent-2", []byte(`{"note":"lgtm"}`))
	if err != nil {
		t.Fatalf("UpdateReviewGate: %v", err)
	}
	if updated.State != "approved" {
		t.Errorf("updated State: got %q want %q", updated.State, "approved")
	}
	if updated.UpdatedBy != "agent-2" {
		t.Errorf("UpdatedBy: got %q want %q", updated.UpdatedBy, "agent-2")
	}

	// Upsert idempotency
	upserted, err := store.CreateReviewGate(ctx, workspaceID, epicID, gateKey, "rejected", "agent-3", nil)
	if err != nil {
		t.Fatalf("CreateReviewGate upsert: %v", err)
	}
	if upserted.ID != created.ID {
		t.Errorf("upsert should return same row: got %q want %q", upserted.ID, created.ID)
	}
	if upserted.State != "rejected" {
		t.Errorf("upserted State: got %q want %q", upserted.State, "rejected")
	}

	// Cleanup
	_, _ = pool.Exec(ctx, `DELETE FROM hive.review_gates WHERE workspace_id = $1::uuid AND epic_id = $2`, workspaceID, epicID)
}
