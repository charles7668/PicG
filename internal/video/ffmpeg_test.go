package video

import (
	"context"
	"image/jpeg"
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

func TestDirectMP4RequiresH264(t *testing.T) {
	parsed := probeOutput{}
	parsed.Format.FormatName = "mov,mp4,m4a,3gp,3g2,mj2"
	parsed.Streams = []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
	}{
		{CodecName: "h264", CodecType: "video"},
		{CodecName: "aac", CodecType: "audio"},
	}
	if !directMP4(parsed) {
		t.Fatal("h264/aac mp4 should play directly")
	}
	parsed.Streams[1].CodecName = "ac3"
	if directMP4(parsed) {
		t.Fatal("ac3 audio should transcode")
	}
	parsed.Streams = parsed.Streams[:1]
	if !directMP4(parsed) {
		t.Fatal("h264 without audio should play directly")
	}
	parsed.Format.FormatName = "matroska,webm"
	if directMP4(parsed) {
		t.Fatal("mkv should transcode")
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
	if err := ffmpeg.RenderThumbnail(context.Background(), input, output, true, 640, 5); err != nil {
		t.Fatal(err)
	}
	thumbnail, err := os.Stat(output)
	if err != nil || thumbnail.Size() == 0 {
		t.Fatalf("invalid thumbnail: %v", err)
	}
}

func TestRenderThumbnailScalesToLongEdge(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is not installed in this test environment")
	}
	work := t.TempDir()
	input := filepath.Join(work, "photo.jpg")
	command := exec.Command(binary, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=800x600", "-frames:v", "1", "-y", input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create image fixture: %v: %s", err, output)
	}
	output := filepath.Join(work, "thumb.jpg")
	ffmpeg := New(func() string { return binary })
	if err := ffmpeg.RenderThumbnail(context.Background(), input, output, false, 640, 5); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, err := jpeg.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 640 || cfg.Height != 480 {
		t.Fatalf("thumbnail = %dx%d, want 640x480", cfg.Width, cfg.Height)
	}
}
