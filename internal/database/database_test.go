package database

import (
	"path/filepath"
	"testing"
)

func TestOpenAppliesSchemaMigrations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("expected schema version 1, got %d", version)
	}
	var table string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='thumbnail_mappings'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
}
