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

// Catalog_entry is the persisted catalog record for a movie or series.
type Catalog_entry struct {
	Id              int64
	Media_type      string // "movie" | "series"
	Title           string
	Original_title  string
	Release_year    int
	Overview        string
	Runtime_minutes int
	Rating          float64
	Vote_count      int
	Imdb_id         string
	Tmdb_id         int
	Poster_path     string
	Status          string // "matched" | "needs_lookup" | "missing"
	Genres          []string
}

// Episode is a single series episode belonging to a catalog entry.
type Episode struct {
	Id               int64
	Catalog_entry_id int64
	Season_number    int
	Episode_number   int
	Title            string
	Overview         string
	Airdate          string
	Is_special       bool
	Status           string
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

// Version is one media file backing a catalog entry or episode.
type Version struct {
	Id               int64
	Catalog_entry_id int64
	Episode_id       int64
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
	Resolution_label  string
	Video_codec       string
	Hdr               bool
	Frame_rate        float64
	Bit_depth         int
	Audio             []Audio_track
	Subtitles         []Subtitle_track
}

// Version_ref is a lightweight version row used for re-scan detection.
type Version_ref struct {
	Id               int64
	Catalog_entry_id int64
	Episode_id       int64
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
