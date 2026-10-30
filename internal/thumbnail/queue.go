package thumbnail

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// Queue serializes thumbnail generation and prefers the folder the user just
// opened. In-flight work finishes; waiting jobs from other folders run later.
type Queue struct {
	store   *Store
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]*job
	focus   string
	seq     uint64
	closed  bool
}

const defaultWorkers = 6

func NewQueue(store *Store) *Queue {
	return newQueue(store, defaultWorkers)
}

func newQueue(store *Store, workers int) *Queue {
	if workers < 1 {
		workers = 1
	}
	queue := &Queue{store: store, pending: make(map[string]*job)}
	queue.cond = sync.NewCond(&queue.mu)
	for range workers {
		go queue.loop()
	}
	return queue
}

// Focus marks a gallery folder as the one the user is currently viewing.
// Pending jobs with no waiters from other folders are dropped. In-flight work
// is not cancelled. Remaining jobs whose parent folder matches run first.
func (q *Queue) Focus(folder string) {
	folder = strings.Trim(folder, "/")
	q.mu.Lock()
	q.focus = folder
	q.dropAbandonedLocked()
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
	itemHot := item.folder == q.focus
	otherHot := other.folder == q.focus
	if itemHot != otherHot {
		return itemHot
	}
	return item.seq < other.seq
}

func (q *Queue) dropAbandonedLocked() {
	for key, item := range q.pending {
		if item.running || len(item.waiters) > 0 || item.folder == q.focus {
			continue
		}
		delete(q.pending, key)
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
	if item.running || len(item.waiters) > 0 || item.folder == q.focus {
		return
	}
	delete(q.pending, key)
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
