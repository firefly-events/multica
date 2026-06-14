package hive

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// RunMigrations ensures the hive schema and all Hive migrations are applied.
// It uses hive.schema_migrations as its own ledger — it never touches the core
// public.schema_migrations table. Returns a non-nil error on any failure; the
// caller should treat this as fatal and abort startup.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	// Bootstrap: create hive schema + its own ledger table atomically.
	_, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS hive;
		CREATE TABLE IF NOT EXISTS hive.schema_migrations (
			version    TEXT        PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
	`)
	if err != nil {
		return fmt.Errorf("hive migrate: bootstrap schema: %w", err)
	}

	files, err := upMigrationFiles()
	if err != nil {
		return fmt.Errorf("hive migrate: list files: %w", err)
	}

	for _, name := range files {
		version := extractVersion(name)

		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM hive.schema_migrations WHERE version = $1)`,
			version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("hive migrate: check %s: %w", version, err)
		}
		if exists {
			slog.Debug("hive migrate: skip (already applied)", "version", version)
			continue
		}

		sql, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("hive migrate: read %s: %w", name, err)
		}

		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("hive migrate: apply %s: %w", version, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO hive.schema_migrations (version) VALUES ($1)`,
			version,
		); err != nil {
			return fmt.Errorf("hive migrate: record %s: %w", version, err)
		}
		slog.Info("hive migrate: applied", "version", version)
	}
	return nil
}

// LatestVersion returns the version string of the highest-numbered migration file.
func LatestVersion() (string, error) {
	files, err := upMigrationFiles()
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("hive migrate: no migration files found")
	}
	return extractVersion(files[len(files)-1]), nil
}

func upMigrationFiles() ([]string, error) {
	var names []string
	err := fs.WalkDir(migrationFiles, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".up.sql") {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func extractVersion(name string) string {
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, ".up.sql")
	base = strings.TrimSuffix(base, ".down.sql")
	return base
}
