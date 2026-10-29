package config

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

const DefaultFFmpegPath = "ffmpeg"

const ffmpegPathKey = "ffmpeg_path"

type Settings struct {
	FFmpegPath string `json:"ffmpeg_path,omitempty"`
}

func (s Settings) EffectiveFFmpegPath() string {
	if s.FFmpegPath == "" {
		return DefaultFFmpegPath
	}
	return s.FFmpegPath
}

type Store struct {
	db       *sql.DB
	mu       sync.RWMutex
	settings Settings
	loaded   bool
}

func NewStore(db *sql.DB) (*Store, error) {
	store := &Store{db: db}
	var path string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ffmpegPathKey).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	store.settings.FFmpegPath = path
	store.loaded = true
	return store, nil
}

func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Store) Save(settings Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(settings)
}

func (s *Store) saveLocked(settings Settings) error {
	if _, err := s.db.Exec(
		`INSERT INTO settings(key, value, updated_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		ffmpegPathKey, settings.FFmpegPath,
	); err != nil {
		return err
	}
	s.settings = settings
	s.loaded = true
	return nil
}

// ImportLegacyFile copies ffmpeg_path from an old settings.json when the
// database has no stored path yet.
func (s *Store) ImportLegacyFile(path string) error {
	if path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	if settings.FFmpegPath == "" {
		return nil
	}
	return s.saveLocked(settings)
}
