package thumbnail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if len(store.cache) != 0 {
		t.Fatal("new Store eagerly loaded thumbnail mappings")
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

func TestCachedFileSkipsStaleStamp(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, filepath.Join(work, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "photo.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(source)
	path, err := store.GetOrCreate(context.Background(), Request{
		Kind: "image", Identity: "album/photo.jpg", SourcePath: source, Info: info,
	}, writeThumbnail("thumb"))
	if err != nil {
		t.Fatal(err)
	}

	cached, ok, err := store.CachedFile(context.Background(), "image", "album/photo.jpg", info.ModTime().UnixNano(), info.Size(), true)
	if err != nil || !ok || cached != path {
		t.Fatalf("expected cached thumbnail, got path=%q ok=%v err=%v", cached, ok, err)
	}
	if _, ok, err := store.CachedFile(context.Background(), "image", "album/photo.jpg", info.ModTime().UnixNano()+1, info.Size(), true); err != nil || ok {
		t.Fatalf("stale stamp should miss cache, ok=%v err=%v", ok, err)
	}
	if cached, ok, err := store.CachedFile(context.Background(), "image", "album/photo.jpg", 0, 0, false); err != nil || !ok || cached != path {
		t.Fatalf("cover-style lookup should ignore stamp, path=%q ok=%v err=%v", cached, ok, err)
	}

	if err := os.WriteFile(source, []byte("updated source"), 0o644); err != nil {
		t.Fatal(err)
	}
	updatedInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	updatedPath, err := store.GetOrCreate(context.Background(), Request{
		Kind: "image", Identity: "album/photo.jpg", SourcePath: source, Info: updatedInfo,
	}, writeThumbnail("updated thumb"))
	if err != nil {
		t.Fatal(err)
	}
	if cached, ok, err := store.CachedFile(context.Background(), "image", "album/photo.jpg", updatedInfo.ModTime().UnixNano(), updatedInfo.Size(), true); err != nil || !ok || cached != updatedPath {
		t.Fatalf("updated mapping was not cached: path=%q ok=%v err=%v", cached, ok, err)
	}
}

func TestCacheReadsDoNotRequireDatabase(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, filepath.Join(work, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "photo.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Kind: "image", Identity: "photo.jpg", SourcePath: source, Info: info}
	expected, err := store.GetOrCreate(context.Background(), request, writeThumbnail("thumbnail"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if path, ok, err := store.CachedFile(context.Background(), "image", "photo.jpg", info.ModTime().UnixNano(), info.Size(), true); err != nil || !ok || path != expected {
		t.Fatalf("CachedFile did not use memory cache: path=%q ok=%v err=%v", path, ok, err)
	}
	if path, ok, err := store.Lookup(context.Background(), request); err != nil || !ok || path != expected {
		t.Fatalf("Lookup did not use memory cache: path=%q ok=%v err=%v", path, ok, err)
	}
}

func TestMappingCacheEvictsLeastRecentlyUsed(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, filepath.Join(work, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	store.cacheLimit = 2

	requests := make([]Request, 3)
	for index := range requests {
		name := fmt.Sprintf("photo-%d.jpg", index)
		source := filepath.Join(work, name)
		if err := os.WriteFile(source, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		requests[index] = Request{Kind: "image", Identity: name, SourcePath: source, Info: info}
		if index == 2 {
			if _, ok, err := store.Lookup(context.Background(), requests[0]); err != nil || !ok {
				t.Fatalf("could not refresh LRU entry: ok=%v err=%v", ok, err)
			}
		}
		if _, err := store.GetOrCreate(context.Background(), requests[index], writeThumbnail(name)); err != nil {
			t.Fatal(err)
		}
	}

	store.mu.RLock()
	_, firstCached := store.cache[mappingKey("image", requests[0].Identity)]
	_, secondCached := store.cache[mappingKey("image", requests[1].Identity)]
	_, thirdCached := store.cache[mappingKey("image", requests[2].Identity)]
	cacheSize := len(store.cache)
	store.mu.RUnlock()
	if cacheSize != 2 || !firstCached || secondCached || !thirdCached {
		t.Fatalf("unexpected LRU contents: size=%d first=%v second=%v third=%v", cacheSize, firstCached, secondCached, thirdCached)
	}

	// An evicted entry remains available from SQLite and is cached again on use.
	if _, ok, err := store.Lookup(context.Background(), requests[1]); err != nil || !ok {
		t.Fatalf("evicted mapping did not reload: ok=%v err=%v", ok, err)
	}
	store.mu.RLock()
	_, reloaded := store.cache[mappingKey("image", requests[1].Identity)]
	cacheSize = len(store.cache)
	store.mu.RUnlock()
	if cacheSize != 2 || !reloaded {
		t.Fatalf("reloaded mapping was not cached: size=%d cached=%v", cacheSize, reloaded)
	}
}

func TestCacheReadsShareStoreLock(t *testing.T) {
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, filepath.Join(work, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "photo.jpg")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Kind: "image", Identity: "photo.jpg", SourcePath: source, Info: info}
	if _, err := store.GetOrCreate(context.Background(), request, writeThumbnail("thumbnail")); err != nil {
		t.Fatal(err)
	}

	// Holding one read lock must not block other cache hits. This would time out
	// if either read path regressed to taking the exclusive Store lock.
	store.mu.RLock()
	done := make(chan error, 2)
	go func() {
		_, ok, err := store.CachedFile(context.Background(), "image", "photo.jpg", info.ModTime().UnixNano(), info.Size(), true)
		if err == nil && !ok {
			err = fmt.Errorf("CachedFile missed an existing thumbnail")
		}
		done <- err
	}()
	go func() {
		_, ok, err := store.Lookup(context.Background(), request)
		if err == nil && !ok {
			err = fmt.Errorf("Lookup missed an existing thumbnail")
		}
		done <- err
	}()

	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				store.mu.RUnlock()
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			store.mu.RUnlock()
			t.Fatal("cache read blocked on another reader")
		}
	}
	store.mu.RUnlock()
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
	store.mu.RLock()
	_, mappingFound := store.cache[mappingKey("image", "gone.jpg")]
	store.mu.RUnlock()
	if mappingFound {
		t.Fatal("cleanup did not evict the removed mapping from memory")
	}
}

func writeThumbnail(content string) func(string) error {
	return func(path string) error {
		return os.WriteFile(path, []byte(content), 0o644)
	}
}
