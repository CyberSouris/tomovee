package database

import (
	"context"
	"database/sql"
	"fmt"
)

// Store is the persistence gateway used by the scanning pipeline and the web
// server. It wraps a Database and exposes typed repository methods.
type Store struct {
	db *Database
}

// New_store builds a Store over an open Database.
func New_store(db *Database) *Store {
	return &Store{db: db}
}

// Media type codes stored in catalog_entry.media_type.
const (
	Media_type_movie  = 0
	Media_type_series = 1
)

// Media_type_name maps a stored media type code to the lower-case name callers
// and JSON use ("movie" | "series"). Unknown codes read as "movie".
func Media_type_name(code int) string {
	if code == Media_type_series {
		return "series"
	}
	return "movie"
}

// Media_type_code maps a media type name ("movie" | "series") to its stored
// code. An empty name defaults to movie so an unset field reads as a movie.
func Media_type_code(name string) int {
	if name == "series" {
		return Media_type_series
	}
	return Media_type_movie
}

// File status codes stored in media_file.status.
const (
	File_status_needs_lookup = 0
	File_status_matched      = 1
)

// File_status_name maps a stored file status code to the name callers and JSON
// use ("needs_lookup" | "matched"). Unknown codes read as "needs_lookup".
func File_status_name(code int) string {
	if code == File_status_matched {
		return "matched"
	}
	return "needs_lookup"
}

// File_status_code maps a file status name to its stored code. An empty name
// defaults to needs_lookup.
func File_status_code(name string) int {
	if name == "matched" {
		return File_status_matched
	}
	return File_status_needs_lookup
}

// Catalog_entry is the persisted identity of a movie or series. Only fields
// worth caching locally live here: media type, title, release year, the external
// ids, and the poster path. Overview, rating, vote count, and runtime come from
// the source on demand. Whether the title is matched follows from its media_file
// rows, so there is no status column here.
type Catalog_entry struct {
	Id           int64
	Media_type   string // "movie" | "series"
	Title        string
	Release_year int
	Imdb_id      string
	Tmdb_id      int
	Poster_path  string
	Genres       []string
	// Matched reports whether any media_file points at this entry. It is not a
	// stored column; read helpers fill it from the files that own the entry.
	Matched bool
}

// Episode is a single series episode belonging to a catalog entry. Only its
// local numbering and title are stored; overview, airdate, and whether it is a
// special come from the source on demand. Its matched state follows its files.
type Episode struct {
	Id               int64
	Catalog_entry_id int64
	Season_number    int
	Episode_number   int
	Title            string
}

// Series_metadata is the show-level record for a series catalog entry.
type Series_metadata struct {
	Catalog_entry_id int64
	First_air_date   string
	Last_air_date    string
	Num_seasons      int
	Num_episodes     int
}

// Audio_track is one audio stream of a version.
type Audio_track struct {
	Language string
	Codec    string
	Channels int
	Source   string
}

// Subtitle_track is one subtitle stream of a version.
type Subtitle_track struct {
	Language string
	Format   string
	Source   string
}

// Version is one media file backing a catalog entry or episode. The name is
// kept for the existing callers; the storage it represents is the media_file
// table, where a file's own status lives.
type Version struct {
	Id               int64
	Catalog_entry_id int64
	Episode_id       int64
	// Library_id is the library whose root File_path is relative to. Versions
	// scanned before libraries existed keep an absolute File_path and a zero
	// Library_id; they are normalized the next time their library is scanned.
	Library_id       int64
	File_path        string
	Size_bytes       int64
	Mtime            string
	Duration_seconds float64
	Container        string
	// Hash is the OpenSubtitles content hash computed during scanning. It is
	// stored so background matching can hash-lookup without re-reading files.
	Hash              string
	Resolution_width  int
	Resolution_height int
	// Resolution_label is derived from Resolution_height (the column is no
	// longer stored); it exists so callers keep a human label. The interlaced
	// "i" suffix is not recoverable from stored data.
	Resolution_label string
	Video_codec      string
	Hdr              bool
	Frame_rate       float64
	Bit_depth        int
	// Status is the file's own match state ("matched" | "needs_lookup"), the
	// single source of truth now that neither the entry nor the episode stores
	// one.
	Status    string
	Audio     []Audio_track
	Subtitles []Subtitle_track
}

// Version_ref is a lightweight media_file row used for re-scan detection.
type Version_ref struct {
	Id               int64
	Catalog_entry_id int64
	Episode_id       int64
	Library_id       int64
	File_path        string
	Size_bytes       int64
	Mtime            string
	Hash             string
	Status           string
}

// with_tx runs fn inside a transaction, committing on success.
func (s *Store) with_tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// exec_tx is a convenience for single-statement transactional writes.
func (s *Store) exec_tx(ctx context.Context, query string, args ...any) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}
