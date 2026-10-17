package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE thumbnail_mappings (
    id                INTEGER PRIMARY KEY,
    source_kind       TEXT NOT NULL CHECK (source_kind IN ('image', 'video', 'cover')),
    source_path       TEXT NOT NULL,
    source_file_path  TEXT NOT NULL,
    source_hash       TEXT NOT NULL,
    file_name         TEXT NOT NULL UNIQUE,
    source_size       INTEGER NOT NULL,
    source_mtime_ns   INTEGER NOT NULL,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    last_access_at    INTEGER NOT NULL,
    UNIQUE (source_kind, source_path)
);
CREATE INDEX thumbnail_mappings_source_file_idx ON thumbnail_mappings (source_file_path);
CREATE INDEX thumbnail_mappings_last_access_idx ON thumbnail_mappings (last_access_at);
`,
	},
}

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection keeps connection-local PRAGMAs deterministic. WAL still
	// allows readers in other PicG processes while a writer is active.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > migrations[len(migrations)-1].version {
		return fmt.Errorf("database schema version %d is newer than this PicG build supports", current)
	}
	for _, item := range migrations {
		if item.version <= current {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", item.version, err)
		}
		if _, err = tx.ExecContext(ctx, item.sql); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, unixepoch())`, item.version)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", item.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", item.version, err)
		}
		current = item.version
	}
	return nil
}
