package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStatusReportsMissingBinary(t *testing.T) {
	ffmpeg := New(func() string { return filepath.Join(t.TempDir(), "missing-ffmpeg") })
	status := ffmpeg.Status(context.Background())
	if status.Available || status.Error == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestRenderThumbnailCreatesJPEG(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed in this test environment")
	}
	work := t.TempDir()
	input := filepath.Join(work, "clip.mp4")
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=2", "-c:v", "mpeg4", "-y", input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create video fixture: %v: %s", err, output)
	}
	output := filepath.Join(work, "thumbnail.jpg")
	ffmpeg := New(func() string { return binary })
	if err := ffmpeg.RenderThumbnail(context.Background(), input, output, true); err != nil {
		t.Fatal(err)
	}
	thumbnail, err := os.Stat(output)
	if err != nil || thumbnail.Size() == 0 {
		t.Fatalf("invalid thumbnail: %v", err)
	}
}
