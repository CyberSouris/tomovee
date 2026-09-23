// Package thumbnail extracts a representative still frame from a video file
// so items without online artwork still have something to show in the UI.
package thumbnail

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Local_marker is stored in catalog_entry.poster_path to flag a poster that
// was generated locally from a video frame instead of being fetched from TMDB.
const Local_marker = "local://frame.jpg"

// Frame_suffix distinguishes generated frame posters from downloaded ones in
// the poster cache directory ("<id>-frame.jpg" vs "<id>.jpg").
const Frame_suffix = "-frame"

const (
	first_offset = 10 * 60.0 // start looking 10 minutes into the file
	step_offset  = 5 * 60.0  // then step 5 minutes while frames look flat
	max_attempts = 12

	flat_stddev  = 8.0 // luminance std-dev below this counts as almost one color
	sample_limit = 20000
)

// Ffmpeg_binary is the executable invoked for frame extraction. It defaults to
// "ffmpeg" (resolved through PATH) and can be overridden for testing.
var Ffmpeg_binary = "ffmpeg"

// Err_flat is returned when no candidate frame had enough visual variation.
var Err_flat = errors.New("thumbnail: no non-uniform frame found")

// frame_timeout bounds one frame-extraction run so a crafted media file that
// makes ffmpeg spin is cut off instead of pinning a core during the scan.
const frame_timeout = 30 * time.Second

// Err_no_ffmpeg is returned when the ffmpeg binary cannot be found.
var Err_no_ffmpeg = errors.New("thumbnail: ffmpeg not available")

// Is_local reports whether poster_path refers to a generated frame poster.
func Is_local(poster_path string) bool {
	return strings.HasPrefix(poster_path, Local_marker)
}

// Local_path returns the on-disk path of an entry's generated frame poster.
func Local_path(dir string, entry_id int64) string {
	return filepath.Join(dir, strconv.FormatInt(entry_id, 10)+Frame_suffix+".jpg")
}

// Parse_local_name extracts the catalog entry id from a generated frame poster
// file name. It reports false for names that are not frame posters.
func Parse_local_name(name string) (int64, bool) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	stem = strings.TrimSuffix(stem, Frame_suffix)
	id, err := strconv.ParseInt(stem, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Extract writes a JPEG still from video_path to out_path. It starts ten
// minutes in and, whenever the frame is nearly a single color, advances by
// five minutes until it finds a more varied frame. duration_seconds bounds the
// search; when it is zero or implausibly short the midpoint is used.
func Extract(ctx context.Context, video_path string, duration_seconds float64, out_path string) error {
	bin, err := exec.LookPath(Ffmpeg_binary)
	if err != nil {
		return fmt.Errorf("%w (%s)", Err_no_ffmpeg, Ffmpeg_binary)
	}
	if err := os.MkdirAll(filepath.Dir(out_path), 0o755); err != nil {
		return err
	}

	var last_err error
	for _, at := range candidate_times(duration_seconds) {
		tmp := out_path + ".tmp.jpg"
		if err := grab_frame(ctx, bin, video_path, at, tmp); err != nil {
			last_err = err
			_ = os.Remove(tmp)
			continue
		}
		flat, err := is_flat(tmp)
		if err == nil && flat {
			_ = os.Remove(tmp)
			last_err = Err_flat
			continue
		}
		if err := os.Rename(tmp, out_path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	if last_err != nil {
		return last_err
	}
	return Err_flat
}

// candidate_times lists the timestamps to sample, in seconds.
func candidate_times(duration float64) []float64 {
	start := first_offset
	if duration <= 0 {
		return []float64{start}
	}
	if start >= duration {
		start = duration / 2
	}
	times := make([]float64, 0, max_attempts)
	for at := start; len(times) < max_attempts && at < duration; at += step_offset {
		times = append(times, at)
	}
	if len(times) == 0 {
		times = append(times, duration/2)
	}
	return times
}

func grab_frame(ctx context.Context, bin, video_path string, at float64, out_path string) error {
	cmd_ctx, cancel := context.WithTimeout(ctx, frame_timeout)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", strconv.FormatFloat(at, 'f', 3, 64),
		"-i", video_path,
		"-frames:v", "1",
		"-vf", "scale=500:-2",
		"-q:v", "3",
		"-y", out_path,
	}
	cmd := exec.CommandContext(cmd_ctx, bin, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("ffmpeg frame at %.0fs: %s", at, detail)
	}
	return nil
}

// is_flat reports whether an image is almost a single color. It samples the
// luminance across the frame and compares the standard deviation to a
// threshold, so both black and other uniform frames are rejected.
func is_flat(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return false, err
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == 0 || height == 0 {
		return true, nil
	}

	step := int(math.Sqrt(float64(width*height) / sample_limit))
	if step < 1 {
		step = 1
	}
	var sum, sum_squares float64
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			luminance := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			sum += luminance
			sum_squares += luminance * luminance
			count++
		}
	}
	if count == 0 {
		return true, nil
	}
	mean := sum / float64(count)
	variance := sum_squares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance) < flat_stddev, nil
}
