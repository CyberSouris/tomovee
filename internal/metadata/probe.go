package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Probe_binary is the ffprobe executable invoked by Probe. It defaults to
// "ffprobe" (resolved through PATH) and can be overridden for testing.
var Probe_binary = "ffprobe"

// Locate_ffprobe returns the absolute path to the ffprobe binary, or an error
// explaining that ffprobe is unavailable.
func Locate_ffprobe() (string, error) {
	path, err := exec.LookPath(Probe_binary)
	if err != nil {
		return "", fmt.Errorf("ffprobe not found on PATH (%s); install ffmpeg to scan media", Probe_binary)
	}
	return path, nil
}

// Probe runs ffprobe on path and returns the parsed technical metadata.
func Probe(ctx context.Context, path string) (*File_info, error) {
	bin, err := Locate_ffprobe()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error", "-show_format", "-show_streams", "-of", "json", "--", path)
	out, err := cmd.Output()
	if err != nil {
		var exit_err *exec.ExitError
		if errors.As(err, &exit_err) {
			detail := strings.TrimSpace(string(exit_err.Stderr))
			if detail == "" {
				detail = exit_err.Error()
			}
			return nil, fmt.Errorf("ffprobe failed for %s: %s", path, detail)
		}
		return nil, fmt.Errorf("run ffprobe for %s: %w", path, err)
	}

	info, err := Parse_from_json(path, out)
	if err != nil {
		return nil, err
	}

	// Prefer the real filesystem size; fall back to the format-reported size.
	if size, err := file_size(path); err == nil {
		info.Size_bytes = size
	}
	return info, nil
}

func file_size(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
