package thumbnail

import (
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Request struct {
	Kind       string
	SourcePath string
	Identity   string
	Info       os.FileInfo
}

type mapping struct {
	ID             int64
	SourceFilePath string
	SourceHash     string
	FileName       string
	SourceSize     int64
	SourceMTimeNS  int64
}

type cachedMapping struct {
	key  string
	item mapping
}

const defaultMappingCacheLimit = 4096

type Store struct {
	db         *sql.DB
	dir        string
	hash       func(string) string
	mu         sync.RWMutex
	cache      map[string]*list.Element
	cacheLRU   *list.List
	cacheLimit int
	revision   uint64
	cleanupMu  sync.Mutex
	now        func() time.Time
}

func NewStore(db *sql.DB, dir string) (*Store, error) {
	return newStore(db, dir, pathHash)
}

func newStore(db *sql.DB, dir string, hash func(string) string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create thumbnail directory: %w", err)
	}
	store := &Store{
		db:         db,
		dir:        filepath.Clean(dir),
		hash:       hash,
		cache:      make(map[string]*list.Element),
		cacheLRU:   list.New(),
		cacheLimit: defaultMappingCacheLimit,
		now:        time.Now,
	}
	return store, nil
}

func pathHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func normalizeKey(kind, identity string) (string, string, error) {
	if kind != "image" && kind != "video" && kind != "cover" {
		return "", "", errors.New("invalid thumbnail kind")
	}
	identity = filepath.ToSlash(filepath.Clean(identity))
	if identity == "." {
		identity = ""
	}
	return kind, identity, nil
}

func mappingKey(kind, identity string) string {
	return kind + "\x00" + identity
}

func prepareRequest(request Request) (Request, string, error) {
	if request.Info == nil || !request.Info.Mode().IsRegular() {
		return Request{}, "", errors.New("thumbnail source is not a regular file")
	}
	kind, identity, err := normalizeKey(request.Kind, request.Identity)
	if err != nil {
		return Request{}, "", err
	}
	request.Kind = kind
	request.Identity = identity
	return request, mappingKey(request.Kind, request.Identity), nil
}

func (s *Store) digestFor(request Request) (string, error) {
	digest := s.hash(request.Kind + "\x00" + request.Identity)
	if len(digest) < 16 {
		return "", errors.New("thumbnail hash is too short")
	}
	return digest, nil
}

func (s *Store) cached(key string) (mapping, bool) {
	s.mu.RLock()
	element, found := s.cache[key]
	if !found {
		s.mu.RUnlock()
		return mapping{}, false
	}
	item := element.Value.(cachedMapping).item
	s.mu.RUnlock()

	// LRU promotion is best-effort so concurrent cache hits never queue behind
	// each other merely to update recency metadata.
	if s.mu.TryLock() {
		if current, stillCached := s.cache[key]; stillCached && current == element {
			s.cacheLRU.MoveToFront(element)
		}
		s.mu.Unlock()
	}
	return item, true
}

func (s *Store) mappingFor(ctx context.Context, key, kind, identity string) (mapping, bool, error) {
	for {
		if item, found := s.cached(key); found {
			return item, true, nil
		}

		s.mu.RLock()
		revision := s.revision
		s.mu.RUnlock()
		item, found, err := s.find(ctx, kind, identity)
		if err != nil {
			return mapping{}, false, err
		}

		s.mu.Lock()
		if element, cached := s.cache[key]; cached {
			s.cacheLRU.MoveToFront(element)
			item = element.Value.(cachedMapping).item
			s.mu.Unlock()
			return item, true, nil
		}
		if revision != s.revision {
			s.mu.Unlock()
			continue
		}
		if found {
			s.cacheMappingLocked(key, item)
		}
		s.mu.Unlock()
		return item, found, nil
	}
}

// cacheMappingLocked adds or refreshes an item while s.mu is held.
func (s *Store) cacheMappingLocked(key string, item mapping) {
	if element, found := s.cache[key]; found {
		element.Value = cachedMapping{key: key, item: item}
		s.cacheLRU.MoveToFront(element)
		return
	}
	element := s.cacheLRU.PushFront(cachedMapping{key: key, item: item})
	s.cache[key] = element
	for len(s.cache) > s.cacheLimit {
		oldest := s.cacheLRU.Back()
		entry := oldest.Value.(cachedMapping)
		delete(s.cache, entry.key)
		s.cacheLRU.Remove(oldest)
	}
}

// evictMappingLocked removes an item while s.mu is held.
func (s *Store) evictMappingLocked(key string) {
	if element, found := s.cache[key]; found {
		delete(s.cache, key)
		s.cacheLRU.Remove(element)
	}
}

func freshThumbnail(item mapping, request Request, dir string) (string, bool) {
	output := filepath.Join(dir, item.FileName)
	if item.SourceFilePath != request.SourcePath || item.SourceSize != request.Info.Size() ||
		item.SourceMTimeNS != request.Info.ModTime().UnixNano() {
		return output, false
	}
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return output, false
	}
	return output, true
}

// Lookup returns a cached thumbnail without generating a new one.
func (s *Store) Lookup(ctx context.Context, request Request) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	request, key, err := prepareRequest(request)
	if err != nil {
		return "", false, err
	}
	item, found, err := s.mappingFor(ctx, key, request.Kind, request.Identity)
	if err != nil || !found {
		return "", false, err
	}
	output, ok := freshThumbnail(item, request, s.dir)
	if !ok {
		return "", false, nil
	}
	return output, true, nil
}

// CachedFile returns a persisted JPEG when the mapping and file exist. When
// matchStamp is true, the stored source size and mtime must match.
func (s *Store) CachedFile(ctx context.Context, kind, identity string, mtimeNS, size int64, matchStamp bool) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	kind, identity, err := normalizeKey(kind, identity)
	if err != nil {
		return "", false, err
	}
	item, found, err := s.mappingFor(ctx, mappingKey(kind, identity), kind, identity)
	if err != nil || !found {
		return "", false, err
	}
	if matchStamp && (item.SourceSize != size || item.SourceMTimeNS != mtimeNS) {
		return "", false, nil
	}
	output := filepath.Join(s.dir, item.FileName)
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", false, nil
	}
	return output, true, nil
}

// GetOrCreate returns the persistent thumbnail mapped to a logical gallery
// path. The full digest and the original path are both stored: the digest picks
// a compact filename, while the path proves identity if two hashes collide.
func (s *Store) GetOrCreate(ctx context.Context, request Request, generate func(string) error) (string, error) {
	request, key, err := prepareRequest(request)
	if err != nil {
		return "", err
	}
	digest, err := s.digestFor(request)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	var item mapping
	var found bool
	if element, cached := s.cache[key]; cached {
		item = element.Value.(cachedMapping).item
		found = true
		s.cacheLRU.MoveToFront(element)
	} else {
		item, found, err = s.find(ctx, request.Kind, request.Identity)
		if err != nil {
			s.mu.Unlock()
			return "", err
		}
		if found {
			s.cacheMappingLocked(key, item)
		}
	}
	now := s.now().Unix()
	if found {
		output, ok := freshThumbnail(item, request, s.dir)
		if ok {
			s.mu.Unlock()
			return output, nil
		}
		s.mu.Unlock()
		if err := s.render(output, generate); err != nil {
			return "", err
		}
		s.mu.Lock()
		_, err = s.db.ExecContext(ctx, `
UPDATE thumbnail_mappings
SET source_file_path=?, source_hash=?, source_size=?, source_mtime_ns=?, updated_at=?, last_access_at=?
WHERE id=?`, request.SourcePath, digest, request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, item.ID)
		if err == nil {
			item.SourceFilePath = request.SourcePath
			item.SourceHash = digest
			item.SourceSize = request.Info.Size()
			item.SourceMTimeNS = request.Info.ModTime().UnixNano()
			s.cacheMappingLocked(key, item)
			s.revision++
		}
		s.mu.Unlock()
		return output, err
	}

	fileName, err := s.allocateFileName(ctx, digest, request.Kind, request.Identity)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO thumbnail_mappings
    (source_kind, source_path, source_file_path, source_hash, file_name, source_size, source_mtime_ns, created_at, updated_at, last_access_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.Kind, request.Identity, request.SourcePath, digest, fileName,
		request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, now)
	if err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("reserve thumbnail mapping: %w", err)
	}
	id, _ := result.LastInsertId()
	s.revision++
	item = mapping{
		ID:             id,
		SourceFilePath: request.SourcePath,
		SourceHash:     digest,
		FileName:       fileName,
		SourceSize:     request.Info.Size(),
		SourceMTimeNS:  request.Info.ModTime().UnixNano(),
	}
	s.cacheMappingLocked(key, item)
	output := filepath.Join(s.dir, fileName)
	s.mu.Unlock()
	if err := s.render(output, generate); err != nil {
		s.mu.Lock()
		if _, deleteErr := s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, id); deleteErr == nil {
			s.evictMappingLocked(key)
			s.revision++
		}
		s.mu.Unlock()
		return "", err
	}
	return output, nil
}

func (s *Store) find(ctx context.Context, kind, identity string) (mapping, bool, error) {
	var item mapping
	err := s.db.QueryRowContext(ctx, `
SELECT id, source_file_path, source_hash, file_name, source_size, source_mtime_ns
FROM thumbnail_mappings WHERE source_kind=? AND source_path=?`, kind, identity).Scan(
		&item.ID, &item.SourceFilePath, &item.SourceHash, &item.FileName, &item.SourceSize, &item.SourceMTimeNS)
	if errors.Is(err, sql.ErrNoRows) {
		return mapping{}, false, nil
	}
	if err != nil {
		return mapping{}, false, fmt.Errorf("find thumbnail mapping: %w", err)
	}
	return item, true, nil
}

func (s *Store) allocateFileName(ctx context.Context, digest, kind, identity string) (string, error) {
	for length := 16; length <= len(digest); length += 8 {
		if length > len(digest) {
			length = len(digest)
		}
		candidate := digest[:length] + ".jpg"
		available, err := s.fileNameAvailable(ctx, candidate, kind, identity)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
		if length == len(digest) {
			break
		}
	}
	// Even a full cryptographic digest collision cannot overwrite another
	// thumbnail. A deterministic numeric suffix provides the final namespace.
	for suffix := 1; ; suffix++ {
		candidate := digest + "-" + strconv.Itoa(suffix) + ".jpg"
		available, err := s.fileNameAvailable(ctx, candidate, kind, identity)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
}

func (s *Store) fileNameAvailable(ctx context.Context, fileName, kind, identity string) (bool, error) {
	var existingKind, existingPath string
	err := s.db.QueryRowContext(ctx, `SELECT source_kind, source_path FROM thumbnail_mappings WHERE file_name=?`, fileName).Scan(&existingKind, &existingPath)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check thumbnail collision: %w", err)
	}
	return existingKind == kind && existingPath == identity, nil
}

func (s *Store) render(output string, generate func(string) error) error {
	temporary, err := os.CreateTemp(s.dir, ".thumbnail-*.jpg")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	if err := generate(temporaryPath); err != nil {
		return err
	}
	info, err := os.Stat(temporaryPath)
	if err != nil || info.Size() == 0 {
		return errors.New("thumbnail generator produced no output")
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish thumbnail: %w", err)
	}
	return nil
}

// Cleanup removes mappings whose source file no longer exists, then removes
// orphaned JPEGs that are not referenced by the database.
func (s *Store) Cleanup(ctx context.Context) (int, error) {
	// Cleanup calls may scan in parallel with cache reads and generation. Only
	// serialize cleanups with each other; s.mu is acquired below for the short
	// check-and-delete sections that must not race with GetOrCreate.
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()

	rows, err := s.db.QueryContext(ctx, `
SELECT source_kind, source_path, id, source_file_path, file_name FROM thumbnail_mappings`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		key      string
		id       int64
		source   string
		fileName string
	}
	var mappings []candidate
	for rows.Next() {
		var kind, identity string
		var item candidate
		if err := rows.Scan(&kind, &identity, &item.id, &item.source, &item.fileName); err != nil {
			rows.Close()
			return 0, err
		}
		item.key = mappingKey(kind, identity)
		mappings = append(mappings, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	removed := 0
	for _, item := range mappings {
		_, statErr := os.Stat(item.source)
		if !errors.Is(statErr, fs.ErrNotExist) {
			continue
		}

		// The mapping or source may have changed since the unlocked scan. Check
		// both again while cache mutations are excluded before removing either.
		s.mu.Lock()
		var kind, identity, source, fileName string
		err := s.db.QueryRowContext(ctx, `
SELECT source_kind, source_path, source_file_path, file_name
FROM thumbnail_mappings WHERE id=?`, item.id).Scan(&kind, &identity, &source, &fileName)
		if errors.Is(err, sql.ErrNoRows) {
			s.mu.Unlock()
			continue
		}
		if err != nil {
			s.mu.Unlock()
			return removed, err
		}
		if mappingKey(kind, identity) != item.key || source != item.source || fileName != item.fileName {
			s.mu.Unlock()
			continue
		}
		if _, err := os.Stat(source); !errors.Is(err, fs.ErrNotExist) {
			s.mu.Unlock()
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, item.id); err != nil {
			s.mu.Unlock()
			return removed, err
		}
		s.evictMappingLocked(item.key)
		s.revision++
		if err := os.Remove(filepath.Join(s.dir, fileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.mu.Unlock()
			return removed, err
		}
		s.mu.Unlock()
		removed++
	}

	referenced := make(map[string]struct{})
	rows, err = s.db.QueryContext(ctx, `SELECT file_name FROM thumbnail_mappings`)
	if err != nil {
		return removed, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return removed, err
		}
		referenced[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return removed, err
	}
	if err := rows.Close(); err != nil {
		return removed, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return removed, err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.ToLower(filepath.Ext(entry.Name())) != ".jpg" {
			continue
		}
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}

		// A thumbnail may have been reserved after the reference snapshot. Keep
		// the final database check and file removal atomic with cache mutations.
		s.mu.Lock()
		var referencedNow int
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thumbnail_mappings WHERE file_name=?)`, entry.Name()).Scan(&referencedNow)
		if err != nil {
			s.mu.Unlock()
			return removed, err
		}
		if referencedNow != 0 {
			s.mu.Unlock()
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.mu.Unlock()
			return removed, err
		}
		s.mu.Unlock()
		removed++
	}
	return removed, nil
}
