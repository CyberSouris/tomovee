// Package opensubtitles implements the OpenSubtitles file hash and (later) the
// OpenSubtitles REST API client used for identifying media files.
package opensubtitles

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// chunk_size is the number of bytes hashed from the head and tail.
	chunk_size = 64 * 1024
	// Min_file_size is the smallest file the OpenSubtitles hash is defined for.
	Min_file_size = 128 * 1024
)

// Err_file_too_small is returned by Compute_hash for files below
// Min_file_size. Callers should fall back to filename-based matching.
var Err_file_too_small = errors.New("opensubtitles: file smaller than 128 KiB")

// Compute_hash returns the 16-character lower-case hex OpenSubtitles hash of
// the file at path.
//
// The algorithm is: start from the file size, add every little-endian 64-bit
// word of the first 64 KiB and of the last 64 KiB (chunks may overlap for files
// under 128 KiB), wrapping at 64 bits. Only 128 KiB is ever read, so hashing a
// large file is fast.
func Compute_hash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opensubtitles: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("opensubtitles: stat %s: %w", path, err)
	}
	size := info.Size()
	if size < Min_file_size {
		return "", Err_file_too_small
	}

	head := make([]byte, chunk_size)
	if _, err := io.ReadFull(f, head); err != nil {
		return "", fmt.Errorf("opensubtitles: read head of %s: %w", path, err)
	}
	tail := make([]byte, chunk_size)
	if _, err := f.ReadAt(tail, size-chunk_size); err != nil {
		return "", fmt.Errorf("opensubtitles: read tail of %s: %w", path, err)
	}

	hash := uint64(size)
	hash = add_chunk(hash, head)
	hash = add_chunk(hash, tail)
	return fmt.Sprintf("%016x", hash), nil
}

// Compute_hash_bytes returns the hash of in-memory data using the same
// algorithm, for callers that already hold the relevant bytes. For inputs
// under 128 KiB the head and tail chunks overlap and their words are counted
// twice, matching the published specification.
func Compute_hash_bytes(data []byte) string {
	size := len(data)
	head_end := min(chunk_size, size)
	tail_start := max(0, size-chunk_size)
	hash := uint64(size)
	hash = add_chunk(hash, data[:head_end])
	hash = add_chunk(hash, data[tail_start:])
	return fmt.Sprintf("%016x", hash)
}

// add_chunk adds each little-endian 64-bit word in buf to hash, wrapping at 64
// bits. A trailing partial word is ignored, matching the reference
// implementations.
func add_chunk(hash uint64, buf []byte) uint64 {
	for i := 0; i+8 <= len(buf); i += 8 {
		hash += binary.LittleEndian.Uint64(buf[i:])
	}
	return hash
}
