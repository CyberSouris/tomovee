package thumbnail

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func Test_candidate_times(t *testing.T) {
	if got, want := candidate_times(3600), []float64{600, 900, 1200, 1500, 1800, 2100, 2400, 2700, 3000, 3300}; !reflect.DeepEqual(got, want) {
		t.Errorf("long movie = %v, want %v", got, want)
	}
	if got, want := candidate_times(120), []float64{60}; !reflect.DeepEqual(got, want) {
		t.Errorf("short clip = %v, want %v", got, want)
	}
	if got, want := candidate_times(0), []float64{600}; !reflect.DeepEqual(got, want) {
		t.Errorf("unknown duration = %v, want %v", got, want)
	}
}

func Test_parse_local_name(t *testing.T) {
	if id, ok := Parse_local_name("42-frame.jpg"); !ok || id != 42 {
		t.Errorf("frame poster = %d, %v; want 42, true", id, ok)
	}
	if id, ok := Parse_local_name("7.jpg"); !ok || id != 7 {
		t.Errorf("downloaded poster = %d, %v; want 7, true", id, ok)
	}
	if _, ok := Parse_local_name("poster-1234.tmp"); ok {
		t.Errorf("temporary file should not parse")
	}
}

func Test_is_flat(t *testing.T) {
	dir := t.TempDir()

	flat_path := filepath.Join(dir, "flat.jpg")
	write_image(t, flat_path, func(int, int) color.Color { return color.RGBA{12, 12, 12, 255} })
	if flat, err := is_flat(flat_path); err != nil || !flat {
		t.Errorf("uniform frame: flat=%v err=%v, want true", flat, err)
	}

	varied_path := filepath.Join(dir, "varied.jpg")
	write_image(t, varied_path, func(x, y int) color.Color {
		if (x/8+y/8)%2 == 0 {
			return color.RGBA{0, 0, 0, 255}
		}
		return color.RGBA{240, 240, 240, 255}
	})
	if flat, err := is_flat(varied_path); err != nil || flat {
		t.Errorf("varied frame: flat=%v err=%v, want false", flat, err)
	}
}

func write_image(t *testing.T, path string, pixel func(x, y int) color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, pixel(x, y))
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
}
