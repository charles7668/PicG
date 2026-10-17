package video

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// RenderThumbnail renders one JPEG frame. Cache identity and persistence are
// managed separately by the thumbnail store.
func (f *FFmpeg) RenderThumbnail(ctx context.Context, input, output string, isVideo bool) error {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	baseArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	outputArgs := []string{"-frames:v", "1", "-vf", "scale=640:-2:force_original_aspect_ratio=decrease", "-q:v", "4", "-y", output}
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
