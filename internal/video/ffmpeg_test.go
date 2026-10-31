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
	parsed.Streams = []probeStream{
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

func TestLongestDurationPrefersStreams(t *testing.T) {
	parsed := probeOutput{}
	parsed.Format.Duration = "N/A"
	parsed.Streams = []probeStream{
		{CodecType: "video", Duration: "60.050000"},
		{CodecType: "audio", Duration: "59.900000"},
	}
	if got := longestDuration(parsed); got != 60.05 {
		t.Fatalf("duration = %v, want 60.05", got)
	}
	parsed.Format.Duration = "75.5"
	if got := longestDuration(parsed); got != 75.5 {
		t.Fatalf("duration = %v, want 75.5", got)
	}
}

func TestParseFFmpegInfoFallsBackToFFmpegOutput(t *testing.T) {
	output := `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'clip.mp4':
  Duration: 00:01:00.05, start: 0.000000, bitrate: 1200 kb/s
  Stream #0:0[0x1](und): Video: h264 (High) (avc1 / 0x31637661), yuv420p, 1920x1080
  Stream #0:1[0x2](und): Audio: aac (LC) (mp4a / 0x6134706D), 44100 Hz, stereo
`
	info, ok := parseFFmpegInfo(output)
	if !ok {
		t.Fatal("expected metadata to parse")
	}
	if info.Duration != 60.05 || !info.Direct {
		t.Fatalf("info = %#v, want 60.05 and direct", info)
	}
}

func TestParseFFmpegInfoHandlesUnsupportedCodecs(t *testing.T) {
	output := `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'clip.mp4':
  Duration: 00:00:12.00, start: 0.000000, bitrate: 900 kb/s
  Stream #0:0[0x1](und): Video: hevc (Main), yuv420p, 1920x1080
  Stream #0:1[0x2](und): Audio: aac (LC), 44100 Hz, stereo
`
	info, ok := parseFFmpegInfo(output)
	if !ok {
		t.Fatal("expected metadata to parse")
	}
	if info.Duration != 12 || info.Direct {
		t.Fatalf("info = %#v, want 12s and transcoding", info)
	}
	if _, ok := parseFFmpegInfo("Duration: N/A, start: 0.000000"); ok {
		t.Fatal("expected N/A duration to be rejected")
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
