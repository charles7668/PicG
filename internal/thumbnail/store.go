package thumbnail

import (
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

type Store struct {
	db   *sql.DB
	dir  string
	hash func(string) string
	mu   sync.Mutex
	now  func() time.Time
}

func NewStore(db *sql.DB, dir string) (*Store, error) {
	return newStore(db, dir, pathHash)
}

func newStore(db *sql.DB, dir string, hash func(string) string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create thumbnail directory: %w", err)
	}
	return &Store{db: db, dir: filepath.Clean(dir), hash: hash, now: time.Now}, nil
}

func pathHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// GetOrCreate returns the persistent thumbnail mapped to a logical gallery
// path. The full digest and the original path are both stored: the digest picks
// a compact filename, while the path proves identity if two hashes collide.
func (s *Store) GetOrCreate(ctx context.Context, request Request, generate func(string) error) (string, error) {
	if request.Info == nil || !request.Info.Mode().IsRegular() {
		return "", errors.New("thumbnail source is not a regular file")
	}
	if request.Kind != "image" && request.Kind != "video" && request.Kind != "cover" {
		return "", errors.New("invalid thumbnail kind")
	}
	request.Identity = filepath.ToSlash(filepath.Clean(request.Identity))
	if request.Identity == "." {
		request.Identity = ""
	}
	digest := s.hash(request.Kind + "\x00" + request.Identity)
	if len(digest) < 16 {
		return "", errors.New("thumbnail hash is too short")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	item, found, err := s.find(ctx, request.Kind, request.Identity)
	if err != nil {
		return "", err
	}
	now := s.now().Unix()
	if found {
		output := filepath.Join(s.dir, item.FileName)
		if item.SourceFilePath == request.SourcePath && item.SourceSize == request.Info.Size() &&
			item.SourceMTimeNS == request.Info.ModTime().UnixNano() {
			if info, statErr := os.Stat(output); statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				_, _ = s.db.ExecContext(ctx, `UPDATE thumbnail_mappings SET last_access_at=? WHERE id=?`, now, item.ID)
				return output, nil
			}
		}
		if err := s.render(output, generate); err != nil {
			return "", err
		}
		_, err = s.db.ExecContext(ctx, `
UPDATE thumbnail_mappings
SET source_file_path=?, source_hash=?, source_size=?, source_mtime_ns=?, updated_at=?, last_access_at=?
WHERE id=?`, request.SourcePath, digest, request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, item.ID)
		return output, err
	}

	fileName, err := s.allocateFileName(ctx, digest, request.Kind, request.Identity)
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO thumbnail_mappings
    (source_kind, source_path, source_file_path, source_hash, file_name, source_size, source_mtime_ns, created_at, updated_at, last_access_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.Kind, request.Identity, request.SourcePath, digest, fileName,
		request.Info.Size(), request.Info.ModTime().UnixNano(), now, now, now)
	if err != nil {
		return "", fmt.Errorf("reserve thumbnail mapping: %w", err)
	}
	id, _ := result.LastInsertId()
	output := filepath.Join(s.dir, fileName)
	if err := s.render(output, generate); err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, id)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx, `SELECT id, source_file_path, file_name FROM thumbnail_mappings`)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id       int64
		source   string
		fileName string
	}
	var mappings []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.source, &item.fileName); err != nil {
			rows.Close()
			return 0, err
		}
		mappings = append(mappings, item)
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
		if _, err := s.db.ExecContext(ctx, `DELETE FROM thumbnail_mappings WHERE id=?`, item.id); err != nil {
			return removed, err
		}
		if err := os.Remove(filepath.Join(s.dir, item.fileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
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
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
