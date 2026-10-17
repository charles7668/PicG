package thumbnail

import (
	"context"
	"strings"
	"sync"
)

type jobResult struct {
	path string
	err  error
}

type job struct {
	key      string
	folder   string
	seq      uint64
	running  bool
	request  Request
	generate func(string) error
	waiters  []chan jobResult
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
// Jobs whose parent folder matches run before everything else still waiting.
func (q *Queue) Focus(folder string) {
	folder = strings.Trim(folder, "/")
	q.mu.Lock()
	q.focus = folder
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
	q.mu.Lock()
	if existing, found := q.pending[key]; found {
		existing.waiters = append(existing.waiters, wait)
	} else {
		q.seq++
		q.pending[key] = &job{
			key:      key,
			folder:   jobFolder(request.Identity),
			seq:      q.seq,
			request:  request,
			generate: generate,
			waiters:  []chan jobResult{wait},
		}
		q.cond.Broadcast()
	}
	q.mu.Unlock()

	select {
	case result := <-wait:
		return result.path, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
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
				request, generate := job.request, job.generate
				q.mu.Unlock()
				path, err := q.store.GetOrCreate(context.Background(), request, generate)
				q.mu.Lock()
				waiters := job.waiters
				delete(q.pending, job.key)
				q.mu.Unlock()
				result := jobResult{path: path, err: err}
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
