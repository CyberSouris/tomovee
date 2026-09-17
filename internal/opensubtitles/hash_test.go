package opensubtitles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// pattern_bytes returns deterministic content where byte i is i mod 256.
func pattern_bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 256)
	}
	return b
}

func write_temp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// Reference values independently computed with the canonical algorithm
// (size + sum of little-endian uint64 of first and last 64 KiB, mod 2^64).
func Test_compute_hash_reference_values(t *testing.T) {
	cases := []struct {
		size int
		want string
	}{
		{131072, "a0601fdf9f610000"},
		{200000, "a0601fdf9f620d40"},
		{1000000, "a0601fdf9f6e4240"},
	}
	for _, c := range cases {
		path := write_temp(t, pattern_bytes(c.size))
		got, err := Compute_hash(path)
		if err != nil {
			t.Fatalf("size %d: %v", c.size, err)
		}
		if got != c.want {
			t.Errorf("size %d: hash = %s, want %s", c.size, got, c.want)
		}
	}
}

func Test_compute_hash_leading_zero_padding(t *testing.T) {
	// 128 KiB of 0xFF yields a hash with leading zeros.
	data := make([]byte, 131072)
	for i := range data {
		data[i] = 0xff
	}
	path := write_temp(t, data)
	got, err := Compute_hash(path)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if want := "000000000001c000"; got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}

func Test_compute_hash_too_small(t *testing.T) {
	path := write_temp(t, make([]byte, 1024))
	if _, err := Compute_hash(path); !errors.Is(err, Err_file_too_small) {
		t.Errorf("err = %v, want Err_file_too_small", err)
	}
}

func Test_compute_hash_missing_file(t *testing.T) {
	if _, err := Compute_hash(filepath.Join(t.TempDir(), "nope.mkv")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func Test_compute_hash_bytes_matches_file(t *testing.T) {
	data := pattern_bytes(300000)
	path := write_temp(t, data)
	from_file, err := Compute_hash(path)
	if err != nil {
		t.Fatalf("hash file: %v", err)
	}
	if from_bytes := Compute_hash_bytes(data); from_bytes != from_file {
		t.Errorf("bytes hash = %s, file hash = %s", from_bytes, from_file)
	}
}

func Test_compute_hash_bytes_overlap_counts_twice(t *testing.T) {
	// Exactly one chunk: head and tail are the same 64 KiB and must be counted
	// twice, so the hash differs from a single pass.
	data := pattern_bytes(chunk_size)

	once := add_chunk(uint64(chunk_size), data)
	twice := add_chunk(once, data)

	got := Compute_hash_bytes(data)
	if want := fmt.Sprintf("%016x", twice); got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
	if got == fmt.Sprintf("%016x", once) {
		t.Error("overlapping chunks should be counted twice, not once")
	}
}
