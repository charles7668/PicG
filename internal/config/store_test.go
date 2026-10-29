package config

import (
	"os"
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

func TestStoreImportsLegacyJSONWhenEmpty(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	legacy := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(legacy, []byte(`{"ffmpeg_path":"/legacy/ffmpeg"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyFile(legacy); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().EffectiveFFmpegPath(); got != "/legacy/ffmpeg" {
		t.Fatalf("expected imported path, got %q", got)
	}
	if err := store.Save(Settings{FFmpegPath: ""}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"ffmpeg_path":"/other/ffmpeg"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyFile(legacy); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().EffectiveFFmpegPath(); got != DefaultFFmpegPath {
		t.Fatalf("legacy import overwrote stored settings: %q", got)
	}
}
