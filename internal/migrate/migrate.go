package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

// Host is the application state a migration may change: the database and
// related files such as the thumbnail directory.
type Host struct {
	DB           *sql.DB
	DatabasePath string
	ThumbnailDir string
}

func (h Host) DataDir() string {
	if h.DatabasePath == "" {
		return ""
	}
	return filepath.Dir(h.DatabasePath)
}

func (h Host) Thumbnails() string {
	if h.ThumbnailDir != "" {
		return h.ThumbnailDir
	}
	if dir := h.DataDir(); dir != "" {
		return filepath.Join(dir, "thumbnails")
	}
	return ""
}

// Migration is one upgrade step, similar to an EF Core migration class.
// Versions must be consecutive from 1. Up may change schema, data, or files.
type Migration interface {
	Version() int
	Up(ctx context.Context, host Host) error
}

func Run(ctx context.Context, host Host, steps []Migration) error {
	if host.DB == nil {
		return fmt.Errorf("migration host is missing a database")
	}
	if err := validateMigrations(steps); err != nil {
		return err
	}
	if _, err := host.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var current int
	if err := host.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	target := 0
	if len(steps) > 0 {
		target = steps[len(steps)-1].Version()
	}
	if current > target {
		return fmt.Errorf("database schema version %d is newer than this PicG build supports", current)
	}
	if current == target {
		return nil
	}

	for _, step := range steps {
		version := step.Version()
		if version <= current {
			continue
		}
		if version != current+1 {
			return fmt.Errorf("cannot apply migration %d; next required version is %d", version, current+1)
		}
		if err := applyMigration(ctx, host, step); err != nil {
			return err
		}
		current = version
	}
	return nil
}

func validateMigrations(steps []Migration) error {
	for i, step := range steps {
		want := i + 1
		if step == nil {
			return fmt.Errorf("migration %d is nil", want)
		}
		if got := step.Version(); got != want {
			return fmt.Errorf("migration versions must be consecutive from 1, index %d has version %d", i, got)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, host Host, step Migration) error {
	version := step.Version()
	if err := step.Up(ctx, host); err != nil {
		return fmt.Errorf("apply migration %d (%T): %w", version, step, err)
	}
	if _, err := host.DB.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, unixepoch())`, version); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	return nil
}
