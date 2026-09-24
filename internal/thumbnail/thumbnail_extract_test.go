package thumbnail

import (
	"context"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func Test_is_local(t *testing.T) {
	if !Is_local(Local_marker) {
		t.Error("local marker should be reported as local")
	}
	if Is_local("tmdb/foo.jpg") || Is_local("") {
		t.Error("non-local poster reported as local")
	}
}

func Test_local_path(t *testing.T) {
	if got, want := Local_path("/dir", 42), filepath.Join("/dir", "42"+Frame_suffix+".jpg"); got != want {
		t.Errorf("Local_path = %q, want %q", got, want)
	}
}

func Test_is_flat_missing_file(t *testing.T) {
	if _, err := is_flat(filepath.Join(t.TempDir(), "missing.jpg")); err == nil {
		t.Error("is_flat on a missing file succeeded")
	}
}

func Test_extract_missing_ffmpeg(t *testing.T) {
	old := Ffmpeg_binary
	Ffmpeg_binary = filepath.Join(t.TempDir(), "no-such-ffmpeg")
	t.Cleanup(func() { Ffmpeg_binary = old })

	err := Extract(context.Background(), "video.mkv", 3600, filepath.Join(t.TempDir(), "out.jpg"))
	if !errors.Is(err, Err_no_ffmpeg) {
		t.Errorf("err = %v, want Err_no_ffmpeg", err)
	}
}

func write_fake_ffmpeg(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	old := Ffmpeg_binary
	Ffmpeg_binary = bin
	t.Cleanup(func() { Ffmpeg_binary = old })
	return bin
}

func Test_extract_generates_frame(t *testing.T) {
	dir := t.TempDir()
	varied := filepath.Join(dir, "varied.jpg")
	write_image(t, varied, func(x, y int) color.Color {
		if (x/8+y/8)%2 == 0 {
			return color.RGBA{0, 0, 0, 255}
		}
		return color.RGBA{240, 240, 240, 255}
	})
	write_fake_ffmpeg(t, "cp "+varied+" \"${16}\"\n")

	out := filepath.Join(dir, "out.jpg")
	if err := Extract(context.Background(), "video.mkv", 3600, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("output not written: %v", err)
	}
	if _, err := os.Stat(out + ".tmp.jpg"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary frame not cleaned up (stat err %v)", err)
	}
}

func Test_extract_all_frames_flat(t *testing.T) {
	dir := t.TempDir()
	flat := filepath.Join(dir, "flat.jpg")
	write_image(t, flat, func(int, int) color.Color { return color.RGBA{12, 12, 12, 255} })
	write_fake_ffmpeg(t, "cp "+flat+" \"${16}\"\n")

	err := Extract(context.Background(), "video.mkv", 600, filepath.Join(dir, "out.jpg"))
	if !errors.Is(err, Err_flat) {
		t.Errorf("err = %v, want Err_flat", err)
	}
}

func Test_extract_ffmpeg_failure(t *testing.T) {
	write_fake_ffmpeg(t, "echo boom >&2\nexit 1\n")

	err := Extract(context.Background(), "video.mkv", 600, filepath.Join(t.TempDir(), "out.jpg"))
	if err == nil {
		t.Fatal("expected an error from the failing ffmpeg")
	}
	if errors.Is(err, Err_flat) {
		t.Errorf("err = %v, want the underlying ffmpeg error", err)
	}
}
