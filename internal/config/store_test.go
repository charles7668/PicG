package config

import (
	"path/filepath"
	"testing"
)

func TestStorePersistsFFmpegPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().EffectiveFFmpegPath(); got != DefaultFFmpegPath {
		t.Fatalf("expected default path, got %q", got)
	}
	if err := store.Save(Settings{FFmpegPath: "/opt/ffmpeg/bin/ffmpeg"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get().EffectiveFFmpegPath(); got != "/opt/ffmpeg/bin/ffmpeg" {
		t.Fatalf("unexpected persisted path %q", got)
	}
}
