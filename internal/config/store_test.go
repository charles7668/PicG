package config

import (
	"path/filepath"
	"testing"

	"git.charleshnas.dscloud.me/charles7668/PicG/internal/database"
)

func TestStorePersistsFFmpegPath(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().EffectiveFFmpegPath(); got != DefaultFFmpegPath {
		t.Fatalf("expected default path, got %q", got)
	}
	if err := store.Save(Settings{FFmpegPath: "/opt/ffmpeg/bin/ffmpeg"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get().EffectiveFFmpegPath(); got != "/opt/ffmpeg/bin/ffmpeg" {
		t.Fatalf("unexpected persisted path %q", got)
	}
}
