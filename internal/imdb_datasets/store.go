package imdb_datasets

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// index_db_name is the suffixless-side database file written next to the
// datasets. It holds an indexed copy of the parsed exports, so opening the
// datasets later never requires loading them into RAM.
const index_db_name = "tomovee_imdb.db"

var memory_seq uint64

// open_file_db opens (creating if necessary) a SQLite database at db_path.
func open_file_db(db_path string) (*sql.DB, error) {
	dsn := "file:" + url.PathEscape(db_path) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// open_memory_db opens a private in-memory SQLite database.
func open_memory_db() (*sql.DB, error) {
	dsn := fmt.Sprintf("file:tomovee_imdb_mem_%d_%d?mode=memory&cache=shared&_pragma=busy_timeout(10000)",
		os.Getpid(), atomic.AddUint64(&memory_seq, 1))
	return sql.Open("sqlite", dsn)
}

// create_schema creates the tables and indexes the index queries rely on. The
// normalized title keys mirror Normalize_title so exact-match lookups need no
// further processing at query time.
func create_schema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS title (
			id TEXT PRIMARY KEY,
			title_type TEXT NOT NULL,
			primary_title TEXT NOT NULL,
			original_title TEXT NOT NULL DEFAULT '',
			pri_key TEXT NOT NULL,
			orig_key TEXT,
			start_year INTEGER NOT NULL DEFAULT 0,
			end_year INTEGER NOT NULL DEFAULT 0,
			runtime_minutes INTEGER NOT NULL DEFAULT 0,
			genres TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS title_pri_key ON title(pri_key)`,
		`CREATE INDEX IF NOT EXISTS title_orig_key ON title(orig_key) WHERE orig_key IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS title_aka (
			key TEXT NOT NULL,
			title_id TEXT NOT NULL REFERENCES title(id)
		)`,
		`CREATE INDEX IF NOT EXISTS title_aka_key ON title_aka(key)`,
		`CREATE TABLE IF NOT EXISTS title_episode (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL,
			season INTEGER NOT NULL DEFAULT 0,
			episode INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS title_episode_parent ON title_episode(parent_id)`,
		`CREATE TABLE IF NOT EXISTS title_rating (
			id TEXT PRIMARY KEY,
			average_rating REAL NOT NULL DEFAULT 0,
			num_votes INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("imdb_datasets: create schema: %w", err)
		}
	}
	return nil
}

// sqlite_batch runs batched, transactional inserts against one prepared
// statement. Callers must Flush before relying on committed data.
type sqlite_batch struct {
	db          *sql.DB
	sql         string
	flush_every int
	stmt        *sql.Stmt
	tx          *sql.Tx
	count       int
}

func new_sqlite_batch(db *sql.DB, sql string) *sqlite_batch {
	return &sqlite_batch{db: db, sql: sql, flush_every: 2000}
}

func (b *sqlite_batch) Exec(args ...any) error {
	if b.stmt == nil {
		tx, err := b.db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(b.sql)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		b.tx = tx
		b.stmt = stmt
	}
	if _, err := b.stmt.Exec(args...); err != nil {
		return err
	}
	b.count++
	if b.count%b.flush_every == 0 {
		return b.Flush()
	}
	return nil
}

func (b *sqlite_batch) Flush() error {
	if b.stmt == nil {
		return nil
	}
	if err := b.stmt.Close(); err != nil {
		b.stmt = nil
		return err
	}
	b.stmt = nil
	err := b.tx.Commit()
	b.tx = nil
	return err
}

// build_index_db parses the dataset exports in source (a directory or a single
// title.basics file) and writes an indexed SQLite database at db_path. It
// builds to a temporary file and renames it into place, so a partially built
// index is never observed.
func build_index_db(source string, db_path string) error {
	tmp := db_path + ".tmp"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	dsn := "file:" + url.PathEscape(tmp) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(MEMORY)&_pragma=synchronous(OFF)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("imdb_datasets: open build database: %w", err)
	}
	db.SetMaxOpenConns(1)
	build_err := func() error {
		if err := create_schema(db); err != nil {
			return err
		}
		files, err := dataset_files(source)
		if err != nil {
			return err
		}
		for path, name := range files {
			data, err := read_file(path)
			if err != nil {
				return fmt.Errorf("imdb_datasets: %w", err)
			}
			err = import_dataset(db, name, data)
			_ = data.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}()
	if err := db.Close(); err != nil && build_err == nil {
		build_err = err
	}
	if build_err != nil {
		_ = os.Remove(tmp)
		return build_err
	}
	// Drop any stale WAL sidecars from a previous index before replacing it so
	// SQLite never tries to replay them against the newly built file.
	_ = os.Remove(db_path + "-wal")
	_ = os.Remove(db_path + "-shm")
	if err := os.Rename(tmp, db_path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("imdb_datasets: rename index: %w", err)
	}
	return nil
}

// dataset_files resolves the exports to import for a source (directory or
// title.basics file) into a map of path → dataset stem. title.basics is
// required; the others are imported when present.
func dataset_files(source string) (map[string]string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("imdb_datasets: stat %s: %w", source, err)
	}
	dir := source
	basics := filepath.Join(dir, "title.basics")
	if !info.IsDir() {
		dir = filepath.Dir(source)
		basics = source
	}
	files := map[string]string{}
	for _, name := range []string{"title.basics", "title.akas", "title.episode", "title.ratings"} {
		path := basics
		if name != "title.basics" {
			path = filepath.Join(dir, name)
		}
		resolved, err := dataset_file(path)
		if err != nil {
			if name == "title.basics" || os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("imdb_datasets: stat %s: %w", path, err)
		}
		files[resolved] = name
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("imdb_datasets: %s does not look like IMDb export data", source)
	}
	return files, nil
}

// dataset_file resolves a dataset stem to an existing export file, allowing
// the optional .tsv and .tsv.gz suffixes.
func dataset_file(stem string) (string, error) {
	for _, candidate := range []string{stem, stem + ".tsv", stem + ".tsv.gz"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

// import_dataset streams one export into its table.
func import_dataset(db *sql.DB, name string, data io.Reader) error {
	switch name {
	case "title.basics":
		return import_titles_stream(db, data)
	case "title.akas":
		return import_akas_stream(db, data)
	case "title.episode":
		return import_episodes_stream(db, data)
	case "title.ratings":
		return import_ratings_stream(db, data)
	}
	return fmt.Errorf("imdb_datasets: unknown dataset %s", name)
}

func import_titles_stream(db *sql.DB, data io.Reader) error {
	batch := new_sqlite_batch(db, `
		INSERT INTO title (id, title_type, primary_title, original_title, pri_key, orig_key,
		                   start_year, end_year, runtime_minutes, genres)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	defer batch.Flush()
	return each_row(data, "title.basics",
		[]string{"tconst", "titleType", "primaryTitle", "originalTitle", "isAdult", "startYear", "endYear", "runtimeMinutes", "genres"},
		func(get func([]string, string) string, rec []string) error {
			primary := get(rec, "primaryTitle")
			original := get(rec, "originalTitle")
			var orig_key any
			if original != "" && original != primary {
				orig_key = Normalize_title(original)
			}
			return batch.Exec(
				get(rec, "tconst"), get(rec, "titleType"), primary, original,
				Normalize_title(primary), orig_key,
				parse_int(get(rec, "startYear")), parse_int(get(rec, "endYear")),
				parse_int(get(rec, "runtimeMinutes")), clean_null(get(rec, "genres")))
		})
}

func import_akas_stream(db *sql.DB, data io.Reader) error {
	batch := new_sqlite_batch(db, "INSERT INTO title_aka (key, title_id) VALUES (?, ?)")
	defer batch.Flush()
	return each_row(data, "title.akas",
		[]string{"titleId", "ordering", "title", "region", "language", "types", "attributes", "isOriginalTitle"},
		func(get func([]string, string) string, rec []string) error {
			id := get(rec, "titleId")
			if id == "" || parse_bool(get(rec, "isOriginalTitle")) {
				return nil
			}
			key := Normalize_title(clean_null(get(rec, "title")))
			if key == "" {
				return nil
			}
			return batch.Exec(key, id)
		})
}

func import_episodes_stream(db *sql.DB, data io.Reader) error {
	batch := new_sqlite_batch(db, `
		INSERT INTO title_episode (id, parent_id, season, episode)
		VALUES (?, ?, ?, ?)`)
	defer batch.Flush()
	return each_row(data, "title.episode",
		[]string{"tconst", "parentTconst", "seasonNumber", "episodeNumber"},
		func(get func([]string, string) string, rec []string) error {
			id := get(rec, "tconst")
			parent := get(rec, "parentTconst")
			if id == "" || parent == "" {
				return nil
			}
			return batch.Exec(id, parent,
				parse_int(get(rec, "seasonNumber")), parse_int(get(rec, "episodeNumber")))
		})
}

func import_ratings_stream(db *sql.DB, data io.Reader) error {
	batch := new_sqlite_batch(db, `
		INSERT INTO title_rating (id, average_rating, num_votes)
		VALUES (?, ?, ?)`)
	defer batch.Flush()
	return each_row(data, "title.ratings",
		[]string{"tconst", "averageRating", "numVotes"},
		func(get func([]string, string) string, rec []string) error {
			return batch.Exec(get(rec, "tconst"),
				parse_float(get(rec, "averageRating")), parse_int(get(rec, "numVotes")))
		})
}

// each_row streams a TSV dataset, invoking fn once per data row in dataset
// order, without collecting the file in memory.
func each_row(r io.Reader, name string, required []string, fn func(func([]string, string) string, []string) error) error {
	reader, column, err := make_tsv_reader(r, name, required)
	if err != nil {
		return err
	}
	if column == nil {
		return nil
	}
	get := column_getter(column)
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		if err := fn(get, rec); err != nil {
			return fmt.Errorf("imdb_datasets: import %s: %w", name, err)
		}
	}
}

// index_db_fresh reports whether db_path is a complete index of the datasets
// in source (i.e. it exists and no export is newer than it).
func index_db_fresh(source string, db_path string) bool {
	db_info, err := os.Stat(db_path)
	if err != nil {
		return false
	}
	files, err := dataset_files(source)
	if err != nil {
		return false
	}
	for path := range files {
		info, err := os.Stat(path)
		if err != nil || info.ModTime().After(db_info.ModTime()) {
			return false
		}
	}
	return true
}
