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

type mappingLock struct {
	mu   sync.Mutex
	refs int
}

const defaultMappingCacheLimit = 4096

type Store struct {
	db          *sql.DB
	dir         string
	hash        func(string) string
	cacheMu     sync.RWMutex
	cache       map[string]*list.Element
	cacheLRU    *list.List
	cacheLimit  int
	revision    uint64
	keyMu       sync.Mutex
	keyLocks    map[string]*mappingLock
	namespaceMu sync.Mutex
	cleanupMu   sync.Mutex
	now         func() time.Time
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
		keyLocks:   make(map[string]*mappingLock),
		now:        time.Now,
	}
	return store, nil
}

func (s *Store) lockMapping(key string) func() {
	s.keyMu.Lock()
	lock := s.keyLocks[key]
	if lock == nil {
		lock = &mappingLock{}
		s.keyLocks[key] = lock
	}
	lock.refs++
	s.keyMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.keyMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.keyLocks, key)
		}
		s.keyMu.Unlock()
	}
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
	s.cacheMu.RLock()
	element, found := s.cache[key]
	if !found {
		s.cacheMu.RUnlock()
		return mapping{}, false
	}
	item := element.Value.(cachedMapping).item
	s.cacheMu.RUnlock()

	// LRU promotion is best-effort so concurrent cache hits never queue behind
	// each other merely to update recency metadata.
	if s.cacheMu.TryLock() {
		if current, stillCached := s.cache[key]; stillCached && current == element {
			s.cacheLRU.MoveToFront(element)
		}
		s.cacheMu.Unlock()
	}
	return item, true
}

func (s *Store) mappingFor(ctx context.Context, key, kind, identity string) (mapping, bool, error) {
	for {
		if item, found := s.cached(key); found {
			return item, true, nil
		}

		s.cacheMu.RLock()
		revision := s.revision
		s.cacheMu.RUnlock()
		item, found, err := s.find(ctx, kind, identity)
		if err != nil {
			return mapping{}, false, err
		}

		s.cacheMu.Lock()
		if element, cached := s.cache[key]; cached {
			s.cacheLRU.MoveToFront(element)
			item = element.Value.(cachedMapping).item
			s.cacheMu.Unlock()
			return item, true, nil
		}
		if revision != s.revision {
			s.cacheMu.Unlock()
			continue
		}
		if found {
			s.cacheMappingLocked(key, item)
		}
		s.cacheMu.Unlock()
		return item, found, nil
	}
}

// cacheMappingLocked adds or refreshes an item while s.cacheMu is held.
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

// evictMappingLocked removes an item while s.cacheMu is held.
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

// CachedFile returns a persisted JPEG when the mapping is still usable.
// Stamp, cover source, and JPEG checks live in usableMapping.
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
	if !s.usableMapping(item, Presence{Kind: kind, Identity: identity, MTimeNS: mtimeNS, Size: size, MatchStamp: matchStamp}) {
		return "", false, nil
	}
	return filepath.Join(s.dir, item.FileName), true, nil
}

type Presence struct {
	Kind       string
	Identity   string
	MTimeNS    int64
	Size       int64
	MatchStamp bool
}

func PresenceID(kind, identity string) string {
	kind, identity, err := normalizeKey(kind, identity)
	if err != nil {
		return ""
	}
	return mappingKey(kind, identity)
}

const presenceQueryChunk = 400

// Present reports which identities already have a usable JPEG. One SQL query
// per chunk. Usability is usableMapping, the same check CachedFile uses.
func (s *Store) Present(ctx context.Context, items []Presence) (map[string]bool, error) {
	found := make(map[string]bool, len(items))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var missing []Presence
	for _, item := range items {
		kind, identity, err := normalizeKey(item.Kind, item.Identity)
		if err != nil {
			continue
		}
		item.Kind = kind
		item.Identity = identity
		key := mappingKey(kind, identity)
		if mapped, ok := s.cached(key); ok {
			if s.usableMapping(mapped, item) {
				found[key] = true
			}
			continue
		}
		missing = append(missing, item)
	}
	for start := 0; start < len(missing); start += presenceQueryChunk {
		end := min(start+presenceQueryChunk, len(missing))
		if err := s.loadPresenceChunk(ctx, missing[start:end], found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (s *Store) loadPresenceChunk(ctx context.Context, items []Presence, found map[string]bool) error {
	if len(items) == 0 {
		return nil
	}
	var query strings.Builder
	args := make([]any, 0, len(items)*2)
	query.WriteString(`SELECT source_kind, source_path, id, source_file_path, source_hash, file_name, source_size, source_mtime_ns FROM thumbnail_mappings WHERE (source_kind, source_path) IN (`)
	for i, item := range items {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString("(?,?)")
		args = append(args, item.Kind, item.Identity)
	}
	query.WriteByte(')')
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return fmt.Errorf("find thumbnail mappings: %w", err)
	}
	defer rows.Close()
	wanted := make(map[string]Presence, len(items))
	for _, item := range items {
		wanted[mappingKey(item.Kind, item.Identity)] = item
	}
	for rows.Next() {
		var kind, identity string
		var item mapping
		if err := rows.Scan(&kind, &identity, &item.ID, &item.SourceFilePath, &item.SourceHash, &item.FileName, &item.SourceSize, &item.SourceMTimeNS); err != nil {
			return fmt.Errorf("scan thumbnail mappings: %w", err)
		}
		key := mappingKey(kind, identity)
		s.cacheMu.Lock()
		s.cacheMappingLocked(key, item)
		s.cacheMu.Unlock()
		queryItem, ok := wanted[key]
		if ok && s.usableMapping(item, queryItem) {
			found[key] = true
		}
	}
	return rows.Err()
}

func (s *Store) usableMapping(item mapping, query Presence) bool {
	if query.MatchStamp && (item.SourceSize != query.Size || item.SourceMTimeNS != query.MTimeNS) {
		return false
	}
	if query.Kind == "cover" && !sourceFileUsable(item.SourceFilePath) {
		return false
	}
	output := filepath.Join(s.dir, item.FileName)
	info, err := os.Stat(output)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func sourceFileUsable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
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

	unlockMapping := s.lockMapping(key)
	defer unlockMapping()

	item, found, err := s.mappingFor(ctx, key, request.Kind, request.Identity)
	if err != nil {
		return "", err
	}
	now := s.now().Unix()
	if found {
		output, ok := freshThumbnail(item, request, s.dir)
		if ok {
			return output, nil
		}
		if err := s.render(output, generate); err != nil {
			return "", err
		}
		_, err = s.db.ExecContext(ctx, `
UPDATE thumbnail_mappings
SET source_file_path=?, source_hash=?, source_size=?, source_mtime_ns=?, updated_at=?, last_access_at=?
WHERE id=?`, request.SourcePath, digest, request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, item.ID)
		if err == nil {
			item.SourceFilePath = request.SourcePath
			item.SourceHash = digest
			item.SourceSize = request.Info.Size()
			item.SourceMTimeNS = request.Info.ModTime().UnixNano()
			s.cacheMu.Lock()
			s.cacheMappingLocked(key, item)
			s.revision++
			s.cacheMu.Unlock()
		}
		return output, err
	}

	// Reserving a filename is short but must be atomic with insertion so two
	// different identities with colliding digests cannot choose the same file.
	s.namespaceMu.Lock()
	fileName, err := s.allocateFileName(ctx, digest, request.Kind, request.Identity)
	if err != nil {
		s.namespaceMu.Unlock()
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO thumbnail_mappings
    (source_kind, source_path, source_file_path, source_hash, file_name, source_size, source_mtime_ns, created_at, updated_at, last_access_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.Kind, request.Identity, request.SourcePath, digest, fileName,
		request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, now)
	if err != nil {
		s.namespaceMu.Unlock()
		return "", fmt.Errorf("reserve thumbnail mapping: %w", err)
	}
	id, _ := result.LastInsertId()
	s.namespaceMu.Unlock()

	item = mapping{
		ID:             id,
		SourceFilePath: request.SourcePath,
		SourceHash:     digest,
		FileName:       fileName,
		SourceSize:     request.Info.Size(),
		SourceMTimeNS:  request.Info.ModTime().UnixNano(),
	}
	s.cacheMu.Lock()
	s.cacheMappingLocked(key, item)
	s.revision++
	s.cacheMu.Unlock()
	output := filepath.Join(s.dir, fileName)
	if err := s.render(output, generate); err != nil {
		s.namespaceMu.Lock()
		if _, deleteErr := s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, id); deleteErr == nil {
			s.cacheMu.Lock()
			s.evictMappingLocked(key)
			s.revision++
			s.cacheMu.Unlock()
		}
		s.namespaceMu.Unlock()
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
	// serialize cleanups with each other. Per-mapping and namespace locks below
	// coordinate only with work that can affect the same mapping or filename.
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

		deleted, err := func() (bool, error) {
			unlockMapping := s.lockMapping(item.key)
			defer unlockMapping()
			s.namespaceMu.Lock()
			defer s.namespaceMu.Unlock()

			// The mapping or source may have changed since the unlocked scan.
			var kind, identity, source, fileName string
			err := s.db.QueryRowContext(ctx, `
SELECT source_kind, source_path, source_file_path, file_name
FROM thumbnail_mappings WHERE id=?`, item.id).Scan(&kind, &identity, &source, &fileName)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if mappingKey(kind, identity) != item.key || source != item.source || fileName != item.fileName {
				return false, nil
			}
			if _, err := os.Stat(source); !errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, item.id); err != nil {
				return false, err
			}
			s.cacheMu.Lock()
			s.evictMappingLocked(item.key)
			s.revision++
			s.cacheMu.Unlock()
			if err := os.Remove(filepath.Join(s.dir, fileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return false, err
			}
			return true, nil
		}()
		if err != nil {
			return removed, err
		}
		if deleted {
			removed++
		}
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
		// the final database check and file removal atomic with filename creation.
		s.namespaceMu.Lock()
		var referencedNow int
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thumbnail_mappings WHERE file_name=?)`, entry.Name()).Scan(&referencedNow)
		if err != nil {
			s.namespaceMu.Unlock()
			return removed, err
		}
		if referencedNow != 0 {
			s.namespaceMu.Unlock()
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.namespaceMu.Unlock()
			return removed, err
		}
		s.namespaceMu.Unlock()
		removed++
	}
	return removed, nil
}
