package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type CreateSettings struct{}

func (CreateSettings) Version() int {
	return 2
}

func (m CreateSettings) Up(ctx context.Context, host Host) error {
	if _, err := host.DB.ExecContext(ctx, `
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
`); err != nil {
		return err
	}
	return m.importLegacySettings(ctx, host)
}

func (CreateSettings) legacyConfigPath(host Host) string {
	if host.DatabasePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(host.DatabasePath), "settings.json")
}

func (m CreateSettings) importLegacySettings(ctx context.Context, host Host) error {
	path := m.legacyConfigPath(host)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings struct {
		FFmpegPath string `json:"ffmpeg_path"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse legacy settings %q: %w", path, err)
	}
	if settings.FFmpegPath == "" {
		return nil
	}
	_, err = host.DB.ExecContext(ctx, `
INSERT INTO settings(key, value, updated_at) VALUES (?, ?, unixepoch())
`, "ffmpeg_path", settings.FFmpegPath)
	return err
}
