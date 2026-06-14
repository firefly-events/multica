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
