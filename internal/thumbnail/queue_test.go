package thumbnail

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
)

func TestQueuePrefersFocusedFolder(t *testing.T) {
	store := testStore(t)
	queue := NewQueue(store)
	defer queue.Close()

	started := make(chan string, 8)
	release := make(chan struct{})
	generate := func(name string) func(string) error {
		return func(path string) error {
			started <- name
			<-release
			return os.WriteFile(path, []byte(name), 0o644)
		}
	}

	queue.Focus("album-a")
	var wg sync.WaitGroup
	enqueue := func(identity, name string) {
		t.Helper()
		request := testRequest(t, store, identity)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := queue.GetOrCreate(context.Background(), request, generate(name)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}()
	}

	enqueue("album-a/one.jpg", "a1")
	if got := recv(t, started); got != "a1" {
		t.Fatalf("first job should be current folder, got %q", got)
	}

	enqueue("album-a/two.jpg", "a2")
	enqueue("album-b/one.jpg", "b1")
	waitPending(t, queue, 3)

	queue.Focus("album-b")
	release <- struct{}{}
	if got := recv(t, started); got != "b1" {
		t.Fatalf("after entering album-b, expected that folder next, got %q", got)
	}

	release <- struct{}{}
	if got := recv(t, started); got != "a2" {
		t.Fatalf("remaining previous-folder job should run after, got %q", got)
	}
	release <- struct{}{}
	wg.Wait()
}

func TestQueueCacheHitDoesNotWaitForOtherFolder(t *testing.T) {
	store := testStore(t)
	queue := NewQueue(store)
	defer queue.Close()

	cached := testRequest(t, store, "album-a/ready.jpg")
	if _, err := store.GetOrCreate(context.Background(), cached, writeThumbnail("ready")); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	block := make(chan struct{})
	slow := testRequest(t, store, "album-b/slow.jpg")
	go func() {
		_, _ = queue.GetOrCreate(context.Background(), slow, func(path string) error {
			close(started)
			<-block
			return os.WriteFile(path, []byte("slow"), 0o644)
		})
	}()
	recv(t, started)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := queue.GetOrCreate(context.Background(), cached, func(string) error {
			t.Error("cached thumbnail should not regenerate")
			return nil
		}); err != nil {
			t.Errorf("cache hit: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cached thumbnail waited behind another folder")
	}
	close(block)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	work := t.TempDir()
	db, err := database.Open(filepath.Join(work, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, filepath.Join(work, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testRequest(t *testing.T, store *Store, identity string) Request {
	t.Helper()
	source := filepath.Join(store.dir, "..", filepath.FromSlash(identity))
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(identity), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Kind: "image", Identity: identity, SourcePath: source, Info: info}
}

func waitPending(t *testing.T, queue *Queue, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for queue.pendingCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d pending jobs, have %d", n, queue.pendingCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for generator")
	}
	var zero T
	return zero
}
