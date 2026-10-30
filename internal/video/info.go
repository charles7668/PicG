package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

type cachedInfo struct {
	filePath string
	size     int64
	mtimeNS  int64
	info     Info
}

type inflight struct {
	done chan struct{}
	info Info
	err  error
}

type InfoStore struct {
	db      *sql.DB
	mu      sync.Mutex
	mem     map[string]cachedInfo
	pending map[string]*inflight
}

func NewInfoStore(db *sql.DB) *InfoStore {
	return &InfoStore{db: db, mem: make(map[string]cachedInfo), pending: make(map[string]*inflight)}
}

func (s *InfoStore) Get(ctx context.Context, identity, filePath string, file os.FileInfo, probe func(context.Context) (Info, error)) (Info, error) {
	if identity == "" || file == nil {
		return probe(ctx)
	}
	size := file.Size()
	mtimeNS := file.ModTime().UnixNano()
	s.mu.Lock()
	if cached, ok := s.mem[identity]; ok && cached.filePath == filePath && cached.size == size && cached.mtimeNS == mtimeNS {
		s.mu.Unlock()
		return cached.info, nil
	}
	if wait := s.pending[identity]; wait != nil {
		s.mu.Unlock()
		return waitForInflight(ctx, wait)
	}
	wait := &inflight{done: make(chan struct{})}
	s.pending[identity] = wait
	s.mu.Unlock()

	finish := func(info Info, err error) (Info, error) {
		s.mu.Lock()
		delete(s.pending, identity)
		if err == nil {
			s.mem[identity] = cachedInfo{filePath: filePath, size: size, mtimeNS: mtimeNS, info: info}
		}
		wait.info = info
		wait.err = err
		close(wait.done)
		s.mu.Unlock()
		return info, err
	}

	stored, ok, err := s.lookup(ctx, identity, filePath, size, mtimeNS)
	if err != nil {
		return finish(Info{}, err)
	}
	if ok {
		return finish(stored, nil)
	}
	info, err := probe(ctx)
	if err != nil {
		return finish(Info{}, err)
	}
	if err := s.save(ctx, identity, filePath, size, mtimeNS, info); err != nil {
		return finish(Info{}, err)
	}
	return finish(info, nil)
}

func waitForInflight(ctx context.Context, wait *inflight) (Info, error) {
	select {
	case <-ctx.Done():
		return Info{}, ctx.Err()
	case <-wait.done:
		return wait.info, wait.err
	}
}

func (s *InfoStore) lookup(ctx context.Context, identity, filePath string, size, mtimeNS int64) (Info, bool, error) {
	var storedPath string
	var storedSize, storedMTime int64
	var info Info
	var direct int
	err := s.db.QueryRowContext(ctx, `
SELECT source_file_path, source_size, source_mtime_ns, duration, direct
FROM video_info WHERE source_path=?`, identity).Scan(&storedPath, &storedSize, &storedMTime, &info.Duration, &direct)
	if errors.Is(err, sql.ErrNoRows) {
		return Info{}, false, nil
	}
	if err != nil {
		return Info{}, false, fmt.Errorf("find video info: %w", err)
	}
	if storedPath != filePath || storedSize != size || storedMTime != mtimeNS {
		return Info{}, false, nil
	}
	info.Direct = direct != 0
	return info, true, nil
}

func (s *InfoStore) save(ctx context.Context, identity, filePath string, size, mtimeNS int64, info Info) error {
	direct := 0
	if info.Direct {
		direct = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO video_info(source_path, source_file_path, source_size, source_mtime_ns, duration, direct, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_path) DO UPDATE SET
    source_file_path=excluded.source_file_path,
    source_size=excluded.source_size,
    source_mtime_ns=excluded.source_mtime_ns,
    duration=excluded.duration,
    direct=excluded.direct,
    updated_at=excluded.updated_at`,
		identity, filePath, size, mtimeNS, info.Duration, direct, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save video info: %w", err)
	}
	return nil
}
