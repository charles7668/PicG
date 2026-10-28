package database

import (
	"context"
	"database/sql"
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

func TestOpenConfiguresEveryPooledConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != maxOpenConnections {
		t.Fatalf("expected %d open connections, got %d", maxOpenConnections, got)
	}

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, maxOpenConnections)
	defer func() {
		for _, connection := range connections {
			connection.Close()
		}
	}()
	for range maxOpenConnections {
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)

		var foreignKeys, busyTimeout int
		var journalMode string
		if err := connection.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
			t.Fatalf("unexpected connection settings: foreign_keys=%d busy_timeout=%d journal_mode=%q", foreignKeys, busyTimeout, journalMode)
		}
	}
}
