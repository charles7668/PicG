package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
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

type probeStream struct {
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	Duration  string `json:"duration"`
}

type probeOutput struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

var (
	ffmpegDurationPattern = regexp.MustCompile(`Duration:\s*(\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)
	ffmpegInputPattern    = regexp.MustCompile(`Input #0,\s*(.+?),\s*from`)
	ffmpegVideoPattern    = regexp.MustCompile(`Video:\s*([A-Za-z0-9_]+)`)
	ffmpegAudioPattern    = regexp.MustCompile(`Audio:\s*([A-Za-z0-9_]+)`)
)

// Probe reports the container duration and whether the browser can play the
// file without transcoding. ffprobe is preferred, but it is not always
// installed next to ffmpeg and it reports "N/A" for some fragmented MP4 files,
// so ffmpeg's own metadata output is used as a fallback.
func (f *FFmpeg) Probe(ctx context.Context, input string) (Info, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, probeErr := exec.CommandContext(probeCtx, f.ffprobePath(),
		"-v", "error",
		"-show_entries", "format=duration,format_name",
		"-show_entries", "stream=codec_type,codec_name,duration",
		"-of", "json",
		input,
	).Output()
	if probeErr == nil {
		var parsed probeOutput
		if err := json.Unmarshal(output, &parsed); err == nil {
			info := Info{Duration: longestDuration(parsed), Direct: directMP4(parsed)}
			if info.Duration > 0 || info.Direct {
				return info, nil
			}
		}
	}
	return f.probeWithFFmpeg(ctx, input, probeErr)
}

// probeWithFFmpeg reads the metadata ffmpeg prints for its input. FFmpeg exits
// with an error when no output is requested, which is expected here.
func (f *FFmpeg) probeWithFFmpeg(ctx context.Context, input string, probeErr error) (Info, error) {
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, _ := exec.CommandContext(runCtx, f.path(), "-hide_banner", "-nostdin", "-i", input).CombinedOutput()
	if info, ok := parseFFmpegInfo(string(output)); ok {
		return info, nil
	}
	if probeErr != nil {
		return Info{}, fmt.Errorf("ffprobe: %w", probeErr)
	}
	return Info{}, errors.New("ffprobe: no duration reported")
}

func parseDurationText(value string) float64 {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return seconds
}

// longestDuration picks the longest duration reported for the file. Some
// fragmented MP4 files only carry the real duration on their streams.
func longestDuration(parsed probeOutput) float64 {
	longest := parseDurationText(parsed.Format.Duration)
	for _, stream := range parsed.Streams {
		if duration := parseDurationText(stream.Duration); duration > longest {
			longest = duration
		}
	}
	return longest
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

// parseFFmpegInfo extracts the duration and codecs from `ffmpeg -i` output.
func parseFFmpegInfo(output string) (Info, bool) {
	matches := ffmpegDurationPattern.FindStringSubmatch(output)
	if matches == nil {
		return Info{}, false
	}
	hours, _ := strconv.ParseFloat(matches[1], 64)
	minutes, _ := strconv.ParseFloat(matches[2], 64)
	seconds, err := strconv.ParseFloat(matches[3], 64)
	if err != nil {
		return Info{}, false
	}
	duration := hours*3600 + minutes*60 + seconds
	if duration <= 0 {
		return Info{}, false
	}
	formatName := "mp4"
	if input := ffmpegInputPattern.FindStringSubmatch(output); input != nil {
		formatName = strings.TrimSpace(input[1])
	}
	direct := strings.Contains(formatName, "mp4")
	hasVideo := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "attached pic") {
			continue
		}
		if video := ffmpegVideoPattern.FindStringSubmatch(line); video != nil {
			if video[1] != "h264" {
				direct = false
				continue
			}
			hasVideo = true
		}
		if audio := ffmpegAudioPattern.FindStringSubmatch(line); audio != nil && audio[1] != "aac" {
			direct = false
		}
	}
	return Info{Duration: duration, Direct: direct && hasVideo}, true
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
