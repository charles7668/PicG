package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const DefaultFFmpegPath = "ffmpeg"

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
	path     string
	mu       sync.RWMutex
	settings Settings
}

func NewStore(path string) (*Store, error) {
	store := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.settings); err != nil {
		return nil, err
	}
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return err
	}
	s.settings = settings
	return nil
}
