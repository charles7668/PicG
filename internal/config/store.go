package config

import (
	"database/sql"
	"errors"
	"sync"
)

const DefaultFFmpegPath = "ffmpeg"

const ffmpegPathKey = "ffmpeg_path"

type Settings struct {
	FFmpegPath string
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
	if _, err := s.db.Exec(
		`INSERT INTO settings(key, value, updated_at) VALUES (?, ?, unixepoch())
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		ffmpegPathKey, settings.FFmpegPath,
	); err != nil {
		return err
	}
	s.settings = settings
	return nil
}
