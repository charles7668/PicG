package migrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteCoverThumbnailsRemovesCoverRowsAndFiles(t *testing.T) {
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.sqlite")
	thumbnailDir := filepath.Join(work, "thumbnails")
	if err := os.MkdirAll(thumbnailDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openNamedDB(t, dbPath)
	host := Host{DB: db, DatabasePath: dbPath, ThumbnailDir: thumbnailDir}
	if err := Run(context.Background(), host, All[:3]); err != nil {
		t.Fatal(err)
	}
	insertMapping(t, host, "cover", "album", "cover.jpg")
	insertMapping(t, host, "image", "album/photo.jpg", "photo.jpg")

	if err := Run(context.Background(), host, All); err != nil {
		t.Fatal(err)
	}
	assertMappingCount(t, db, "cover", 0)
	assertMappingCount(t, db, "image", 1)
	if _, err := os.Stat(filepath.Join(thumbnailDir, "cover.jpg")); !os.IsNotExist(err) {
		t.Fatalf("cover file still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(thumbnailDir, "photo.jpg")); err != nil {
		t.Fatal(err)
	}

	insertMapping(t, host, "cover", "album", "cover-new.jpg")
	if err := Run(context.Background(), host, All); err != nil {
		t.Fatal(err)
	}
	assertMappingCount(t, db, "cover", 1)
}

func TestDeleteCoverThumbnailsKeepsRowsWhenFileRemovalFails(t *testing.T) {
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.sqlite")
	thumbnailDir := filepath.Join(work, "thumbnails")
	if err := os.MkdirAll(thumbnailDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openNamedDB(t, dbPath)
	host := Host{DB: db, DatabasePath: dbPath, ThumbnailDir: thumbnailDir}
	if err := Run(context.Background(), host, All[:3]); err != nil {
		t.Fatal(err)
	}
	insertMapping(t, host, "cover", "album", "cover.jpg")
	if err := os.Chmod(thumbnailDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(thumbnailDir, 0o755) })

	if err := (DeleteCoverThumbnails{}).Up(context.Background(), host); err == nil {
		t.Fatal("expected file removal to fail")
	}
	assertMappingCount(t, db, "cover", 1)
	if _, err := os.Stat(filepath.Join(thumbnailDir, "cover.jpg")); err != nil {
		t.Fatal(err)
	}
}

func insertMapping(t *testing.T, host Host, kind, path, fileName string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(host.Thumbnails(), fileName), []byte(kind), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := host.DB.Exec(`
INSERT INTO thumbnail_mappings (
    source_kind, source_path, source_file_path, source_hash, file_name,
    source_size, source_mtime_ns, created_at, updated_at, last_access_at
) VALUES (?, ?, ?, 'hash', ?, 1, 1, 1, 1, 1)`, kind, path, path, fileName)
	if err != nil {
		t.Fatal(err)
	}
}

func assertMappingCount(t *testing.T, db *sql.DB, kind string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thumbnail_mappings WHERE source_kind = ?`, kind).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s mappings = %d, want %d", kind, got, want)
	}
}
