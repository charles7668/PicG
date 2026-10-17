package gallery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowseUsesDirectoryStructure(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "旅行", "日本"))
	mustWrite(t, filepath.Join(root, "旅行", "cover.jpg"))
	mustWrite(t, filepath.Join(root, "旅行", "日本", "京都.png"))
	mustWrite(t, filepath.Join(root, "首頁.webp"))
	mustWrite(t, filepath.Join(root, "片段.mp4"))
	mustWrite(t, filepath.Join(root, ".hidden.jpg"))
	mustWrite(t, filepath.Join(root, "notes.txt"))

	browser := NewBrowser(root)
	data := browser.Browse("", "", "")
	if !data.RootAvailable || data.Error != "" {
		t.Fatalf("expected readable root, got error %q", data.Error)
	}
	if len(data.Directories) != 1 || data.Directories[0].Name != "旅行" {
		t.Fatalf("unexpected directories: %#v", data.Directories)
	}
	if len(data.Photos) != 2 {
		t.Fatalf("unexpected photos: %#v", data.Photos)
	}
	var foundVideo bool
	for _, media := range data.Photos {
		if media.Name == "片段" && media.Kind == "video" && strings.HasPrefix(media.ThumbnailURL, "/thumbnail?") && strings.Contains(media.ThumbnailURL, "v=") {
			foundVideo = true
		}
	}
	if !foundVideo {
		t.Fatalf("expected video media with deferred thumbnail: %#v", data.Photos)
	}
	if data.Directories[0].CoverURL == "" || data.Directories[0].CoverOriginalURL == "" || !strings.Contains(data.Directories[0].CoverURL, "v=") {
		t.Fatal("expected generated and original directory cover URLs")
	}

	nested := browser.Browse("旅行/日本", "京都", "")
	if len(nested.Breadcrumbs) != 3 || len(nested.Photos) != 1 {
		t.Fatalf("unexpected nested result: %#v", nested)
	}
}

func TestFindCoverFallsBackToNestedVideo(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "clips", "day1"))
	mustWrite(t, filepath.Join(root, "clips", "day1", "shot.mp4"))
	cover, err := NewBrowser(root).findCover("clips")
	if err != nil {
		t.Fatal(err)
	}
	if cover != "clips/day1/shot.mp4" {
		t.Fatalf("expected nested video cover, got %q", cover)
	}
}

func TestFindCoverPrefersImageOverVideo(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "album"))
	mustWrite(t, filepath.Join(root, "album", "clip.mp4"))
	mustWrite(t, filepath.Join(root, "album", "photo.jpg"))
	cover, err := NewBrowser(root).findCover("album")
	if err != nil {
		t.Fatal(err)
	}
	if cover != "album/photo.jpg" {
		t.Fatalf("expected image cover, got %q", cover)
	}
}

func TestBrowseSortsPhotosByDateAndSize(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "alpha.jpg")
	newer := filepath.Join(root, "beta.jpg")
	mustWrite(t, older)
	mustWrite(t, newer)
	if err := os.WriteFile(newer, []byte{0xff, 0xd8, 0xff, 0xd9, 0x00, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	browser := NewBrowser(root)
	byDate := browser.Browse("", "", "date-desc")
	if len(byDate.Photos) != 2 || byDate.Photos[0].Name != "beta" {
		t.Fatalf("date-desc: %#v", byDate.Photos)
	}
	bySize := browser.Browse("", "", "size-desc")
	if len(bySize.Photos) != 2 || bySize.Photos[0].Name != "beta" {
		t.Fatalf("size-desc: %#v", bySize.Photos)
	}
	byName := browser.Browse("", "", "name")
	if len(byName.Photos) != 2 || byName.Photos[0].Name != "alpha" {
		t.Fatalf("name: %#v", byName.Photos)
	}
}

func TestBrowseRejectsTraversal(t *testing.T) {
	data := NewBrowser(t.TempDir()).Browse("../secret", "", "")
	if data.Error == "" {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestBrowseRejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.jpg"))
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	data := NewBrowser(root).Browse("outside", "", "")
	if data.Error == "" {
		t.Fatal("expected external symlink to be rejected")
	}
}

func TestOpenOnlyReturnsImagesInsideRoot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "photo.jpg"))
	mustWrite(t, filepath.Join(root, "notes.txt"))
	browser := NewBrowser(root)

	file, _, err := browser.Open("photo.jpg")
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	file.Close()
	if _, _, err := browser.Open("notes.txt"); err == nil {
		t.Fatal("expected non-image file to be rejected")
	}
	if _, _, err := browser.Open("../photo.jpg"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte{0xff, 0xd8, 0xff, 0xd9}, 0o644); err != nil {
		t.Fatal(err)
	}
}
