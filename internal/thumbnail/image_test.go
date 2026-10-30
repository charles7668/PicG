package thumbnail

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderImageCapsLongEdge(t *testing.T) {
	work := t.TempDir()
	source := filepath.Join(work, "photo.jpg")
	writeJPEG(t, source, 800, 600)

	output := filepath.Join(work, "thumb.jpg")
	if err := RenderImage(source, output, 640, 78); err != nil {
		t.Fatal(err)
	}
	width, height := jpegSize(t, output)
	if width != 640 || height != 480 {
		t.Fatalf("thumbnail = %dx%d, want 640x480", width, height)
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
