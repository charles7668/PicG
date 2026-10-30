package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type FFmpeg struct {
	path func() string
}

type Status struct {
	Available bool
	Path      string
	Version   string
	Error     string
}

func New(path func() string) *FFmpeg {
	return &FFmpeg{path: path}
}

func (f *FFmpeg) Status(ctx context.Context) Status {
	binary := f.path()
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return Status{Path: binary, Error: "找不到 FFmpeg 執行檔"}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, resolved, "-version").Output()
	if err != nil {
		return Status{Path: resolved, Error: "FFmpeg 無法執行"}
	}
	firstLine := strings.SplitN(string(output), "\n", 2)[0]
	return Status{Available: true, Path: resolved, Version: firstLine}
}

// RenderThumbnail renders one JPEG frame. maxDimension is the long-edge cap;
// qv is FFmpeg -q:v. Cache identity and persistence are managed separately.
func (f *FFmpeg) RenderThumbnail(ctx context.Context, input, output string, isVideo bool, maxDimension, qv int) error {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	baseArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1", "-an"}
	filter := fmt.Sprintf("scale=%d:-2:force_original_aspect_ratio=decrease", maxDimension)
	outputArgs := []string{"-frames:v", "1", "-vf", filter, "-q:v", strconv.Itoa(qv), "-y", output}
	attempts := [][]string{append(append(append([]string{}, baseArgs...), "-i", input), outputArgs...)}
	if isVideo {
		attempts = append([][]string{append(append(append([]string{}, baseArgs...), "-ss", "1", "-i", input), outputArgs...)}, attempts...)
	}
	var lastError error
	for _, args := range attempts {
		combined, commandErr := exec.CommandContext(runCtx, f.path(), args...).CombinedOutput()
		if commandErr == nil {
			return nil
		}
		lastError = fmt.Errorf("%w: %s", commandErr, strings.TrimSpace(string(combined)))
	}
	if lastError == nil {
		lastError = errors.New("ffmpeg did not create a thumbnail")
	}
	return fmt.Errorf("ffmpeg thumbnail: %w", lastError)
}

type Info struct {
	Duration float64
	Direct   bool
}

type probeOutput struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
	} `json:"streams"`
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func (f *FFmpeg) Probe(ctx context.Context, input string) (Info, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, f.ffprobePath(),
		"-v", "error",
		"-show_entries", "format=duration,format_name",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "json",
		input,
	).Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe: %w", err)
	}
	var parsed probeOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		return Info{}, fmt.Errorf("ffprobe: %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(parsed.Format.Duration), 64)
	if err != nil || duration <= 0 {
		return Info{}, fmt.Errorf("ffprobe duration: invalid value %q", strings.TrimSpace(parsed.Format.Duration))
	}
	return Info{Duration: duration, Direct: directMP4(parsed)}, nil
}

func directMP4(parsed probeOutput) bool {
	if !strings.Contains(parsed.Format.FormatName, "mp4") {
		return false
	}
	hasVideo := false
	for _, stream := range parsed.Streams {
		switch stream.CodecType {
		case "video":
			if stream.CodecName != "h264" {
				return false
			}
			hasVideo = true
		case "audio":
			if stream.CodecName != "aac" {
				return false
			}
		}
	}
	return hasVideo
}

func (f *FFmpeg) ffprobePath() string {
	ffmpegPath := f.path()
	directory := filepath.Dir(ffmpegPath)
	extension := filepath.Ext(ffmpegPath)
	probe := "ffprobe" + extension
	if directory == "." {
		return probe
	}
	return filepath.Join(directory, probe)
}

// StreamMP4 transcodes a video from start to a fragmented MP4 stream that
// browsers can play before FFmpeg reaches the end of the input file.
func (f *FFmpeg) StreamMP4(ctx context.Context, input string, start float64, output io.Writer) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
	}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args,
		"-i", input,
		"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "23",
		"-profile:v", "baseline", "-level:v", "4.2", "-pix_fmt", "yuv420p",
		"-vf", "scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-force_key_frames", "expr:gte(t,n_forced*2)",
		"-c:a", "aac", "-b:a", "160k", "-ac", "2",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)
	command := exec.CommandContext(ctx, f.path(), args...)
	var stderr bytes.Buffer
	command.Stdout = output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg stream: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
