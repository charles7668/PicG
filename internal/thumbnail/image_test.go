package thumbnail

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderCoverIsSmallerThanMediaThumbnail(t *testing.T) {
	work := t.TempDir()
	source := filepath.Join(work, "photo.jpg")
	writeJPEG(t, source, 800, 600)

	coverOut := filepath.Join(work, "cover.jpg")
	if err := RenderImage(source, coverOut, 240, 62); err != nil {
		t.Fatal(err)
	}
	coverW, coverH := jpegSize(t, coverOut)
	if coverW > 240 || coverH > 240 {
		t.Fatalf("cover %dx%d exceeds 240", coverW, coverH)
	}

	mediaOut := filepath.Join(work, "media.jpg")
	if err := RenderImage(source, mediaOut, 640, 78); err != nil {
		t.Fatal(err)
	}
	mediaW, mediaH := jpegSize(t, mediaOut)
	if mediaW > 640 || mediaH > 640 {
		t.Fatalf("media %dx%d exceeds 640", mediaW, mediaH)
	}
	if coverW >= mediaW || coverH >= mediaH {
		t.Fatalf("cover %dx%d should be smaller than media %dx%d", coverW, coverH, mediaW, mediaH)
	}
	coverInfo, err := os.Stat(coverOut)
	if err != nil {
		t.Fatal(err)
	}
	mediaInfo, err := os.Stat(mediaOut)
	if err != nil {
		t.Fatal(err)
	}
	if coverInfo.Size() >= mediaInfo.Size() {
		t.Fatalf("cover %d bytes should be smaller than media %d bytes", coverInfo.Size(), mediaInfo.Size())
	}
}

func writeJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.RGBA{R: 40, G: 80, B: 160, A: 255})
		}
	}
	if err := jpeg.Encode(file, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
}

func jpegSize(t *testing.T, path string) (int, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, err := jpeg.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}
