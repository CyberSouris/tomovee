package metadata

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func skip_without_tool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed; skipping integration test", name)
	}
}

// Test_probe_on_real_file verifies the full Probe path (ffprobe on a real
// media container synthesised by ffmpeg). Skipped when ffmpeg is unavailable.
func Test_probe_on_real_file(t *testing.T) {
	skip_without_tool(t, "ffmpeg")
	if _, err := Locate_ffprobe(); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}

	dir := t.TempDir()
	video_path := filepath.Join(dir, "probe_sample.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24", "-t", "1",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", "1",
		"-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-metadata:s:a:0", "language=deu",
		video_path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg build fixture: %v (%s)", err, out)
	}

	ctx := context.Background()
	info, err := Probe(ctx, video_path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Path != video_path {
		t.Errorf("path = %q, want %q", info.Path, video_path)
	}
	if info.Video == nil {
		t.Fatal("expected a video stream")
	}
	if info.Video.Width == 0 || info.Video.Height == 0 {
		t.Errorf("video dimensions not parsed: %+v", info.Video)
	}
	if info.Video.Frame_rate == 0 {
		t.Errorf("frame rate not parsed: %+v", info.Video)
	}
	if info.Duration_seconds <= 0 {
		t.Errorf("duration = %v, want > 0", info.Duration_seconds)
	}
	if info.Container == "" {
		t.Error("container not parsed")
	}
	if info.Size_bytes <= 0 {
		t.Errorf("size = %d, want > 0", info.Size_bytes)
	}
	if len(info.Audio) < 1 {
		t.Fatal("expected at least one audio stream")
	}
	if info.Audio[0].Language != "deu" {
		t.Errorf("audio[0] language = %q, want deu", info.Audio[0].Language)
	}
}

func Test_probe_missing_file_is_error(t *testing.T) {
	if _, err := Locate_ffprobe(); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "does_not_exist.mkv")
	_, err := Probe(context.Background(), missing)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func Test_locate_ffprobe_error_when_missing(t *testing.T) {
	old := Probe_binary
	Probe_binary = "ffprobe_definitely_not_installed_xyz"
	defer func() { Probe_binary = old }()

	if p, err := Locate_ffprobe(); err == nil || p != "" {
		t.Errorf("expected error and empty path, got %q, %v", p, err)
	}
}
