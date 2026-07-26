package pg

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaFS holds the migrations, embedded so that a binary carries its own
// schema and Open needs nothing on disk.
//
//go:embed schema/*.sql
var schemaFS embed.FS

// migrationLockID is the advisory-lock key migrate takes while it works, so two
// processes (or two parallel tests bringing up two schemas) cannot run
// CREATE TABLE against the same database at the same time. The number is
// arbitrary; it only has to be ours.
const migrationLockID = 0x63627300 // "cbs\0"

// migrate applies every embedded migration that has not been applied yet, in
// filename order, each in its own transaction.
//
// This is deliberately about forty lines rather than a migration-tool
// dependency: applied-set, sorted files, one transaction each, and a lock. The
// interesting part of a schema is the schema.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("pg: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := fs.ReadFile(schemaFS, "schema/"+name)
		if err != nil {
			return fmt.Errorf("pg: read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, pool, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one file, and the bookkeeping row that records it, in a
// single transaction: a migration that half-applied would be worse than one
// that never ran.
func applyMigration(ctx context.Context, pool *pgxpool.Pool, name, body string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin migration %s: %w", name, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op after a successful commit

	// Taken first, so the applied-set check below and the DDL that follows it
	// are one serialized step. Released automatically at COMMIT or ROLLBACK.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(migrationLockID)); err != nil {
		return fmt.Errorf("pg: lock for migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("pg: create schema_migrations: %w", err)
	}

	var applied bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", name,
	).Scan(&applied); err != nil {
		return fmt.Errorf("pg: check migration %s: %w", name, err)
	}
	if applied {
		return tx.Rollback(ctx)
	}

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("pg: apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
		return fmt.Errorf("pg: record migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: commit migration %s: %w", name, err)
	}
	return nil
}
