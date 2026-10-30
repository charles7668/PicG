package thumbnail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type jobResult struct {
	path string
	err  error
}

type job struct {
	key     string
	folder  string
	seq     uint64
	running bool
	work    func() jobResult
	waiters []chan jobResult
}

type tabView struct {
	folder string
	seen   time.Time
}

// Queue serializes thumbnail generation and prefers folders currently open in
// a browser tab. A job that has been queued is not dropped; viewed folders
// run first, and the rest follow in enqueue order.
type Queue struct {
	store   *Store
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]*job
	viewers map[string]tabView
	now     func() time.Time
	seq     uint64
	closed  bool
}

const (
	defaultWorkers = 6
	viewerTTL      = 2 * time.Minute
)

func NewQueue(store *Store) *Queue {
	return newQueue(store, defaultWorkers)
}

func newQueue(store *Store, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}
	queue := &Queue{store: store, pending: make(map[string]*job), viewers: make(map[string]tabView), now: time.Now}
	queue.cond = sync.NewCond(&queue.mu)
	for range workers {
		go queue.loop()
	}
	return queue
}

// View records the folder one browser tab is showing. It is a heartbeat:
// each call refreshes that tab and forgets tabs silent longer than viewerTTL.
// That only changes which waiting jobs are preferred. Queued work is not dropped.
func (q *Queue) View(tab, folder string) {
	folder = strings.Trim(folder, "/")
	q.mu.Lock()
	q.expireViewersLocked()
	q.viewers[tab] = tabView{folder: folder, seen: q.now()}
	q.mu.Unlock()
}

// Leave removes a tab. It does not drop queued work. A reload can enqueue
// before the new page sends View.
func (q *Queue) Leave(tab string) {
	q.mu.Lock()
	delete(q.viewers, tab)
	q.mu.Unlock()
}

func (q *Queue) GetOrCreate(ctx context.Context, request Request, generate func(string) error) (string, error) {
	request, key, err := prepareRequest(request)
	if err != nil {
		return "", err
	}
	path, ok, err := q.store.Lookup(ctx, request)
	if err != nil {
		return "", err
	}
	if ok {
		return path, nil
	}

	wait := make(chan jobResult, 1)
	if err := q.schedule(key, request.Identity, func() jobResult {
		path, err := q.store.GetOrCreate(context.Background(), request, generate)
		return jobResult{path: path, err: err}
	}, wait); err != nil {
		return "", err
	}

	select {
	case result := <-wait:
		return result.path, result.err
	case <-ctx.Done():
		q.removeWaiter(key, wait)
		return "", ctx.Err()
	}
}

// Enqueue starts work for a cache miss and returns without waiting. Same
// identity shares one pending job. HTTP handlers use this so a miss can 503
// and free the browser connection while a worker generates.
func (q *Queue) Enqueue(kind, identity string, work func() error) error {
	kind, identity, err := normalizeKey(kind, identity)
	if err != nil {
		return err
	}
	return q.schedule(mappingKey(kind, identity), identity, func() jobResult {
		return jobResult{err: work()}
	}, nil)
}

func (q *Queue) schedule(key, identity string, work func() jobResult, wait chan jobResult) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errors.New("thumbnail queue closed")
	}
	if existing, found := q.pending[key]; found {
		if wait != nil {
			existing.waiters = append(existing.waiters, wait)
		}
		return nil
	}
	q.seq++
	item := &job{key: key, folder: jobFolder(identity), seq: q.seq, work: work}
	if wait != nil {
		item.waiters = []chan jobResult{wait}
	}
	q.pending[key] = item
	q.cond.Broadcast()
	return nil
}

func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *Queue) loop() {
	for {
		q.mu.Lock()
		for {
			if q.closed {
				q.mu.Unlock()
				return
			}
			if job := q.pickLocked(); job != nil {
				job.running = true
				work := job.work
				q.mu.Unlock()
				result := work()
				q.mu.Lock()
				waiters := job.waiters
				delete(q.pending, job.key)
				q.mu.Unlock()
				for _, wait := range waiters {
					wait <- result
				}
				break
			}
			q.cond.Wait()
		}
	}
}

func (q *Queue) pickLocked() *job {
	var best *job
	for _, item := range q.pending {
		if item.running {
			continue
		}
		if best == nil || q.aheadLocked(item, best) {
			best = item
		}
	}
	return best
}

func (q *Queue) aheadLocked(item, other *job) bool {
	itemHot := q.viewingLocked(item.folder)
	otherHot := q.viewingLocked(other.folder)
	if itemHot != otherHot {
		return itemHot
	}
	return item.seq < other.seq
}

func (q *Queue) viewingLocked(folder string) bool {
	for _, viewed := range q.viewers {
		if viewed.folder == folder {
			return true
		}
	}
	return false
}

func (q *Queue) expireViewersLocked() {
	now := q.now()
	for tab, viewed := range q.viewers {
		if now.Sub(viewed.seen) >= viewerTTL {
			delete(q.viewers, tab)
		}
	}
}

func (q *Queue) removeWaiter(key string, wait chan jobResult) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item := q.pending[key]
	if item == nil {
		return
	}
	item.waiters = withoutWaiter(item.waiters, wait)
}

func withoutWaiter(waiters []chan jobResult, wait chan jobResult) []chan jobResult {
	kept := waiters[:0]
	for _, candidate := range waiters {
		if candidate != wait {
			kept = append(kept, candidate)
		}
	}
	return kept
}

func (q *Queue) pendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func jobFolder(identity string) string {
	identity = strings.Trim(identity, "/")
	index := strings.LastIndex(identity, "/")
	if index < 0 {
		return ""
	}
	return identity[:index]
}
