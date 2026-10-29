package migrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCreateSettingsImportsLegacyJSON(t *testing.T) {
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.sqlite")
	if err := os.WriteFile(filepath.Join(work, "settings.json"), []byte(`{"ffmpeg_path":"/legacy/ffmpeg"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openNamedDB(t, dbPath)
	host := Host{DB: db, DatabasePath: dbPath}
	if err := Run(context.Background(), host, All); err != nil {
		t.Fatal(err)
	}
	assertSetting(t, db, "/legacy/ffmpeg")
}

func TestCreateSettingsWithoutLegacyFile(t *testing.T) {
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.sqlite")
	db := openNamedDB(t, dbPath)
	if err := Run(context.Background(), Host{DB: db, DatabasePath: dbPath}, All); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected empty settings table, got %d rows", count)
	}
}

func openNamedDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func assertSetting(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'ffmpeg_path'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != want {
		t.Fatalf("ffmpeg_path %q, want %q", value, want)
	}
}
