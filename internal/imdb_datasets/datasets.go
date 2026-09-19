// Package imdb_datasets consumes the offline IMDb dataset exports
// (https://datasets.imdbws.com/) for local, network-free enrichment and
// verification of titles, years, and IMDb ids. It is the third source in the
// matching pipeline, used to fill gaps when online services are down or keys
// are missing.
package imdb_datasets

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/scanner"
)

// Title is one record of the IMDb title.basics.tsv dataset.
type Title struct {
	Id              string // tconst, e.g. "tt0133093"
	Title_type      string // movie, tvSeries, tvMovie, ...
	Primary_title   string
	Original_title  string
	Adult           bool
	Start_year      int
	End_year        int
	Runtime_minutes int
	Genres          string // comma-separated, may be empty
}

// Akas is one record of the IMDb title.akas.tsv dataset: an alternative title
// (localized, transliterated, or original) for a title.
type Akas struct {
	Id          string // tconst
	Ordering    int
	Title       string
	Region      string
	Language    string
	Types       string
	Attributes  string
	Is_original bool
}

// Episode is one record of the IMDb title.episode.tsv dataset, linking an
// episode title to its parent series.
type Episode struct {
	Id        string // tconst of the episode title
	Parent_id string // tconst of the series
	Season    int
	Episode   int
}

// Rating is one record of the IMDb title.ratings.tsv dataset.
type Rating struct {
	Id             string // tconst
	Average_rating float64
	Num_votes      int
}

// Parse_titles reads headline title.basics.tsv data (column order
// independent, tab-separated, "\N" for null) from r. Malformed rows are
// skipped rather than aborting the load.
func Parse_titles(r io.Reader) ([]Title, error) {
	reader, column, err := make_tsv_reader(r, "title.basics",
		[]string{"tconst", "titleType", "primaryTitle", "originalTitle", "isAdult", "startYear", "endYear", "runtimeMinutes", "genres"})
	if err != nil {
		return nil, err
	}
	get := column_getter(column)

	var titles []Title
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		titles = append(titles, Title{
			Id:              get(rec, "tconst"),
			Title_type:      get(rec, "titleType"),
			Primary_title:   get(rec, "primaryTitle"),
			Original_title:  get(rec, "originalTitle"),
			Adult:           parse_bool(get(rec, "isAdult")),
			Start_year:      parse_int(get(rec, "startYear")),
			End_year:        parse_int(get(rec, "endYear")),
			Runtime_minutes: parse_int(get(rec, "runtimeMinutes")),
			Genres:          clean_null(get(rec, "genres")),
		})
	}
	return titles, nil
}

// Parse_akas reads title.akas.tsv data from r.
func Parse_akas(r io.Reader) ([]Akas, error) {
	reader, column, err := make_tsv_reader(r, "title.akas",
		[]string{"titleId", "ordering", "title", "region", "language", "types", "attributes", "isOriginalTitle"})
	if err != nil {
		return nil, err
	}
	get := column_getter(column)

	var akas []Akas
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		akas = append(akas, Akas{
			Id:          get(rec, "titleId"),
			Ordering:    parse_int(get(rec, "ordering")),
			Title:       clean_null(get(rec, "title")),
			Region:      clean_null(get(rec, "region")),
			Language:    clean_null(get(rec, "language")),
			Types:       clean_null(get(rec, "types")),
			Attributes:  clean_null(get(rec, "attributes")),
			Is_original: parse_bool(get(rec, "isOriginalTitle")),
		})
	}
	return akas, nil
}

// Parse_episodes reads title.episode.tsv data from r.
func Parse_episodes(r io.Reader) ([]Episode, error) {
	reader, column, err := make_tsv_reader(r, "title.episode",
		[]string{"tconst", "parentTconst", "seasonNumber", "episodeNumber"})
	if err != nil {
		return nil, err
	}
	get := column_getter(column)

	var episodes []Episode
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		episodes = append(episodes, Episode{
			Id:        get(rec, "tconst"),
			Parent_id: get(rec, "parentTconst"),
			Season:    parse_int(get(rec, "seasonNumber")),
			Episode:   parse_int(get(rec, "episodeNumber")),
		})
	}
	return episodes, nil
}

// Parse_ratings reads title.ratings.tsv data from r.
func Parse_ratings(r io.Reader) ([]Rating, error) {
	reader, column, err := make_tsv_reader(r, "title.ratings",
		[]string{"tconst", "averageRating", "numVotes"})
	if err != nil {
		return nil, err
	}
	get := column_getter(column)

	var ratings []Rating
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		ratings = append(ratings, Rating{
			Id:             get(rec, "tconst"),
			Average_rating: parse_float(get(rec, "averageRating")),
			Num_votes:      parse_int(get(rec, "numVotes")),
		})
	}
	return ratings, nil
}

// make_tsv_reader validates a TSV dataset header and returns the reader plus
// its column index.
func make_tsv_reader(r io.Reader, name string, required []string) (*csv.Reader, map[string]int, error) {
	reader := csv.NewReader(r)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return reader, nil, nil
		}
		return nil, nil, fmt.Errorf("imdb_datasets: read header: %w", err)
	}
	column := make(map[string]int, len(header))
	for i, name := range header {
		column[name] = i
	}
	for _, name := range required {
		if _, ok := column[name]; !ok {
			return nil, nil, fmt.Errorf("imdb_datasets: missing column %q in %s", name, name)
		}
	}
	return reader, column, nil
}

func column_getter(column map[string]int) func([]string, string) string {
	return func(rec []string, name string) string {
		if i, ok := column[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
}

// Open makes the IMDb datasets at path queryable. path is either a directory
// holding the export files (title.basics.tsv(.gz) plus the optional
// title.akas/episode/ratings files) or a single title.basics file; when a
// single file is given, sibling dataset files in the same directory are still
// picked up if present.
//
// The exports are parsed once into an indexed SQLite database written next to
// them, then queried lazily; opening an already built index does not hold the
// datasets in memory. Rebuilding happens automatically when an export is
// newer than the index.
func Open(path string) (*Index, error) {
	return Open_with_progress(path, nil)
}

// Open_with_progress is Open, additionally reporting index build events
// (importing datasets, rows processed) through on_progress. on_progress may be
// nil to keep the plain behaviour. Because building a fresh index can take a
// long time, callers that must serve immediately should run this in the
// background and report it to the user.
func Open_with_progress(path string, on_progress func(Build_progress)) (*Index, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("imdb_datasets: stat %s: %w", path, err)
	}
	dir := path
	if !info.IsDir() {
		dir = filepath.Dir(path)
	}
	db_path := filepath.Join(dir, index_db_name)
	if !index_db_fresh(path, db_path) {
		if on_progress != nil {
			total := int64(0)
			if files, err := dataset_files(path); err == nil {
				for f, _ := range files {
					if stat, err := os.Stat(f); err == nil {
						total += stat.Size()
					}
				}
			}
			on_progress(Build_progress{Step: Build_stale, Total: total})
		}
		if err := build_index_db(path, db_path, on_progress); err != nil {
			return nil, err
		}
		if on_progress != nil {
			on_progress(Build_progress{Step: Build_ready})
		}
	} else if on_progress != nil {
		on_progress(Build_progress{Step: Build_reuse})
	}
	db, err := open_file_db(db_path)
	if err != nil {
		return nil, fmt.Errorf("imdb_datasets: open index: %w", err)
	}
	return &Index{db: db}, nil
}

// byte_counter records the number of bytes read from the wrapped reader, used
// to report byte-based build progress against the source file size.
type byte_counter struct {
	r io.Reader
	n int64
}

func (c *byte_counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func read_file(path string, counter *byte_counter) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	counter.r = file
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(counter)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("decompress %s: %w", path, err)
		}
		return gzip_reader{gz, file}, nil
	}
	return file, nil
}

// gzip_reader closes the underlying file when the gzip stream reaches EOF or
// is closed.
type gzip_reader struct {
	gz   *gzip.Reader
	file *os.File
}

func (r gzip_reader) Read(p []byte) (int, error) { return r.gz.Read(p) }
func (r gzip_reader) Close() error {
	_ = r.gz.Close()
	return r.file.Close()
}

// media_type_of maps an IMDb titleType to the catalog media type. Movies are
// "movie"/"tvMovie"/"tvSpecial"; series are "tvSeries"/"tvMiniSeries".
// Unknown types (short, video, videoGame, ...) are not catalogued.
func media_type_of(title_type string) (scanner.Media_type, bool) {
	switch title_type {
	case "movie", "tvMovie", "tvSpecial":
		return scanner.Movie, true
	case "tvSeries", "tvMiniSeries":
		return scanner.Series, true
	}
	return 0, false
}

func parse_int(value string) int {
	value = clean_null(value)
	if value == "" {
		return 0
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func parse_float(value string) float64 {
	value = clean_null(value)
	if value == "" {
		return 0
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return n
}

func parse_bool(value string) bool {
	return clean_null(value) == "1"
}

func clean_null(value string) string {
	if value == `\N` {
		return ""
	}
	return value
}
