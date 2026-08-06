package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path"
	"sort"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// migrate applies every embedded migration that has not run yet, each in its own
// transaction, in filename order.
//
// # No advisory lock, and none is needed
//
// store/pg takes one, because several processes can reach one Postgres server
// and two of them migrating at once would apply the same file twice. SQLite has
// no equivalent and the situation does not arise: one writer at a time is the
// database's own guarantee, so the second migrator waits at the first statement
// and then finds the applied row already there.
//
// # Keyed on filename, with no checksum
//
// A migration that is edited after it has run stays applied and the edit is
// never seen. That is the limitation store/pg documents and it is inherited
// deliberately rather than fixed here: while there is one migration the
// difference is invisible, and Task 18 gives each shape its own file rather than
// layering new ones onto this.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    filename   TEXT PRIMARY KEY,
		    applied_at TEXT NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	applied, err := appliedSet(ctx, db)
	if err != nil {
		return err
	}

	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("sqlite: read schema dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && path.Ext(e.Name()) == ".sql" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if _, done := applied[name]; done {
			continue
		}
		body, err := schemaFS.ReadFile("schema/" + name)
		if err != nil {
			return fmt.Errorf("sqlite: read %s: %w", name, err)
		}
		if err := applyOne(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// appliedSet reads the filenames already applied.
func appliedSet(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, "SELECT filename FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sqlite: scan schema_migrations: %w", err)
		}
		applied[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyOne runs one migration and records it, in a single transaction, so a
// failure part-way leaves neither the statements nor the record.
func applyOne(ctx context.Context, db *sql.DB, name, body string) error {
	dbtx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin %s: %w", name, err)
	}
	defer func() { _ = dbtx.Rollback() }()

	if _, err := dbtx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("sqlite: apply %s: %w", name, err)
	}
	if _, err := dbtx.ExecContext(ctx,
		"INSERT INTO schema_migrations (filename, applied_at) VALUES (?, ?)",
		name, nowText(),
	); err != nil {
		return fmt.Errorf("sqlite: record %s: %w", name, err)
	}
	if err := dbtx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit %s: %w", name, err)
	}
	return nil
}
