package thumbnail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
)

func TestStorePersistsMappingAcrossRestart(t *testing.T) {
	work := t.TempDir()
	databasePath := filepath.Join(work, "app.sqlite")
	thumbnailDir := filepath.Join(work, "thumbnails")
	source := filepath.Join(work, "photo.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(source)

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, thumbnailDir)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Kind: "image", Identity: "album/photo.jpg", SourcePath: source, Info: info}
	first, err := store.GetOrCreate(context.Background(), request, writeThumbnail("first"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = database.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err = NewStore(db, thumbnailDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.GetOrCreate(context.Background(), request, func(string) error {
		t.Fatal("persistent thumbnail should not be regenerated")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("mapping changed after restart: %q != %q", first, second)
	}
}

func TestStorePreventsFullHashCollision(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	collidingHash := strings.Repeat("a", 64)
	store, err := newStore(db, filepath.Join(work, "thumbnails"), func(string) string { return collidingHash })
	if err != nil {
		t.Fatal(err)
	}

	makeRequest := func(name string) Request {
		source := filepath.Join(work, name)
		if err := os.WriteFile(source, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(source)
		return Request{Kind: "image", Identity: name, SourcePath: source, Info: info}
	}
	paths := make(map[string]struct{})
	var final string
	// Seven mappings consume digest prefixes of 16 through 64 characters. The
	// eighth simulates a full SHA-256 collision and must receive a suffix.
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf("photo-%d.jpg", index)
		path, err := store.GetOrCreate(context.Background(), makeRequest(name), writeThumbnail(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := paths[path]; exists {
			t.Fatalf("colliding paths share thumbnail %q", path)
		}
		paths[path] = struct{}{}
		final = path
	}
	if !strings.Contains(filepath.Base(final), "-1.jpg") {
		t.Fatalf("expected full collision suffix, got %q", final)
	}
}

func TestCleanupRemovesMissingSourcesAndOrphans(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := filepath.Join(work, "thumbnails")
	store, err := NewStore(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "gone.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(source)
	thumbnailPath, err := store.GetOrCreate(context.Background(), Request{
		Kind: "image", Identity: "gone.jpg", SourcePath: source, Info: info,
	}, writeThumbnail("thumbnail"))
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "orphan.jpg")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Cleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected two removed thumbnails, got %d", removed)
	}
	for _, name := range []string{thumbnailPath, orphan} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("expected %q to be removed, got %v", name, err)
		}
	}
}

func writeThumbnail(content string) func(string) error {
	return func(path string) error {
		return os.WriteFile(path, []byte(content), 0o644)
	}
}
