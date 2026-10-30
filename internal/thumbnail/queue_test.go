package thumbnail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
)

func TestQueuePrefersViewedFolder(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
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

	queue.View("tab", "album-a")
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

	queue.View("tab", "album-b")
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

func TestPickKeepsSmallerSeqWhenNeitherFolderIsFocused(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
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

	queue.View("tab", "album-a")
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

	enqueue("album-a/zero.jpg", "a0")
	if got := recv(t, started); got != "a0" {
		t.Fatalf("first job should be current folder, got %q", got)
	}
	enqueue("album-a/one.jpg", "a1")
	waitPending(t, queue, 2)
	enqueue("album-b/one.jpg", "b1")
	waitPending(t, queue, 3)

	queue.View("tab", "album-c")
	release <- struct{}{}
	if got := recv(t, started); got != "a1" {
		t.Fatalf("off-focus jobs should keep smaller seq first, got %q", got)
	}
	release <- struct{}{}
	if got := recv(t, started); got != "b1" {
		t.Fatalf("expected remaining waiter job next, got %q", got)
	}
	release <- struct{}{}
	wg.Wait()
}

func TestViewDropsAbandonedEnqueueJobs(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
	defer queue.Close()

	started := make(chan string, 8)
	release := make(chan struct{})
	work := func(name string) func() error {
		return func() error {
			started <- name
			<-release
			return nil
		}
	}

	queue.View("tab", "album-a")
	if err := queue.Enqueue("image", "album-a/one.jpg", work("a1")); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, started); got != "a1" {
		t.Fatalf("first job should be current folder, got %q", got)
	}
	if err := queue.Enqueue("image", "album-a/two.jpg", work("a2")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue("image", "album-b/one.jpg", work("b1")); err != nil {
		t.Fatal(err)
	}
	waitPending(t, queue, 3)

	queue.View("tab", "album-b")
	if got := queue.pendingCount(); got != 2 {
		t.Fatalf("abandoned enqueue should be dropped, pending %d", got)
	}

	release <- struct{}{}
	if got := recv(t, started); got != "b1" {
		t.Fatalf("after entering album-b, expected that folder next, got %q", got)
	}
	release <- struct{}{}
	select {
	case name := <-started:
		t.Fatalf("dropped job should not run, got %q", name)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestViewKeepsJobsForOtherTabs(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
	defer queue.Close()

	started := make(chan string, 8)
	release := make(chan struct{})
	work := func(name string) func() error {
		return func() error {
			started <- name
			<-release
			return nil
		}
	}

	queue.View("tab-a", "album-a")
	if err := queue.Enqueue("image", "album-a/one.jpg", work("a1")); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, started); got != "a1" {
		t.Fatalf("first job should be current folder, got %q", got)
	}
	if err := queue.Enqueue("image", "album-a/two.jpg", work("a2")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue("image", "album-b/one.jpg", work("b1")); err != nil {
		t.Fatal(err)
	}
	waitPending(t, queue, 3)

	queue.View("tab-b", "album-b")
	if got := queue.pendingCount(); got != 3 {
		t.Fatalf("other tab folder should stay queued, pending %d", got)
	}

	queue.Leave("tab-a")
	if got := queue.pendingCount(); got != 2 {
		t.Fatalf("left tab folder should drop, pending %d", got)
	}

	release <- struct{}{}
	if got := recv(t, started); got != "b1" {
		t.Fatalf("remaining tab folder should run next, got %q", got)
	}
	release <- struct{}{}
	select {
	case name := <-started:
		t.Fatalf("dropped job should not run, got %q", name)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestViewExpiresStaleTab(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
	defer queue.Close()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	started := make(chan string, 8)
	release := make(chan struct{})
	work := func(name string) func() error {
		return func() error {
			started <- name
			<-release
			return nil
		}
	}

	queue.View("tab-a", "album-a")
	if err := queue.Enqueue("image", "album-a/one.jpg", work("a1")); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, started); got != "a1" {
		t.Fatalf("first job should be current folder, got %q", got)
	}
	if err := queue.Enqueue("image", "album-a/two.jpg", work("a2")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue("image", "album-b/one.jpg", work("b1")); err != nil {
		t.Fatal(err)
	}
	waitPending(t, queue, 3)

	now = now.Add(viewerTTL + time.Second)
	queue.View("tab-b", "album-b")
	if got := queue.pendingCount(); got != 2 {
		t.Fatalf("stale tab folder should drop, pending %d", got)
	}

	release <- struct{}{}
	if got := recv(t, started); got != "b1" {
		t.Fatalf("live tab folder should run next, got %q", got)
	}
	release <- struct{}{}
	select {
	case name := <-started:
		t.Fatalf("expired tab job should not run, got %q", name)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCancelledWaiterDropsOffFocusJob(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
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

	queue.View("tab", "album-a")
	a1 := testRequest(t, store, "album-a/one.jpg")
	go func() {
		_, _ = queue.GetOrCreate(context.Background(), a1, generate("a1"))
	}()
	if got := recv(t, started); got != "a1" {
		t.Fatalf("got %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	b1 := testRequest(t, store, "album-b/one.jpg")
	done := make(chan error, 1)
	go func() {
		_, err := queue.GetOrCreate(ctx, b1, generate("b1"))
		done <- err
	}()
	waitPending(t, queue, 2)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for queue.pendingCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("canceled off-focus job still pending, have %d", queue.pendingCount())
		}
		time.Sleep(5 * time.Millisecond)
	}

	release <- struct{}{}
	select {
	case name := <-started:
		t.Fatalf("canceled job should not run, got %q", name)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEnqueueDoesNotWaitForWork(t *testing.T) {
	store := testStore(t)
	queue := newQueue(store, 1)
	defer queue.Close()

	started := make(chan struct{})
	block := make(chan struct{})
	request := testRequest(t, store, "album-a/slow.jpg")
	begin := time.Now()
	if err := queue.Enqueue(request.Kind, request.Identity, func() error {
		close(started)
		<-block
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) > 200*time.Millisecond {
		t.Fatalf("Enqueue waited for work: %s", time.Since(begin))
	}
	recv(t, started)
	if err := queue.Enqueue(request.Kind, request.Identity, func() error {
		t.Error("duplicate enqueue should share the pending job")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(block)
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
