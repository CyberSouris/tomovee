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

// Parse_titles reads headline title.basics.tsv data (column order
// independent, tab-separated, "\N" for null) from r. Malformed rows are
// skipped rather than aborting the load.
func Parse_titles(r io.Reader) ([]Title, error) {
	reader := csv.NewReader(r)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, fmt.Errorf("imdb_datasets: read header: %w", err)
	}
	column := make(map[string]int, len(header))
	for i, name := range header {
		column[name] = i
	}
	required := []string{"tconst", "titleType", "primaryTitle", "originalTitle", "isAdult", "startYear", "endYear", "runtimeMinutes", "genres"}
	for _, name := range required {
		if _, ok := column[name]; !ok {
			return nil, fmt.Errorf("imdb_datasets: missing column %q in title.basics", name)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := column[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}

	var titles []Title
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: parse row: %w", err)
		}
		if len(rec) < len(required) {
			continue
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

// Open loads a title.basics file from disk, transparently decompressing gzip
// (chosen by the ".gz" suffix), and returns a searchable Index.
func Open(path string) (*Index, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("imdb_datasets: open %s: %w", path, err)
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: decompress %s: %w", path, err)
		}
		defer gz.Close()
		reader = gz
	}
	titles, err := Parse_titles(reader)
	if err != nil {
		return nil, err
	}
	return New_index(titles), nil
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

func parse_bool(value string) bool {
	return clean_null(value) == "1"
}

func clean_null(value string) string {
	if value == `\N` {
		return ""
	}
	return value
}
