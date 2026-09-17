package imdb_datasets

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/scanner"
)

const test_tsv = `tconst	titleType	primaryTitle	originalTitle	isAdult	startYear	endYear	runtimeMinutes	genres
tt0133093	movie	The Matrix	The Matrix	0	1999	\N	136	Action,Sci-Fi
tt0234215	movie	The Matrix Reloaded	The Matrix Reloaded	0	2003	\N	138	Action,Sci-Fi
tt0903747	tvSeries	Breaking Bad	Breaking Bad	0	2008	2013	49	Crime,Drama
tt0111161	movie	The Shawshank Redemption	The Shawshank Redemption	0	1994	\N	142	Drama
tt0000001	short	Some Short	Some Short	0	1900	\N	1	Short
tt0000002	videoGame	Some Game	Some Game	0	2001	\N	0	Action
tt0000003	movie	Amélie	Le Fabuleux Destin d'Amélie Poulain	0	2001	\N	122	Comedy,Romance
tt0000005	movie	Remake	Remake	0	2000	\N	90	Drama
tt0000006	movie	Remake	Remake	0	2000	\N	95	Drama
`

func Test_parse_titles(t *testing.T) {
	titles, err := Parse_titles(strings.NewReader(test_tsv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(titles) != 9 {
		t.Fatalf("titles = %d, want 9", len(titles))
	}
	first := titles[0]
	if first.Id != "tt0133093" || first.Title_type != "movie" {
		t.Errorf("first = %+v", first)
	}
	if first.Primary_title != "The Matrix" || first.Start_year != 1999 || first.Runtime_minutes != 136 {
		t.Errorf("first = %+v", first)
	}
	if first.End_year != 0 {
		t.Errorf("null endYear = %d, want 0", first.End_year)
	}
	if first.Genres != "Action,Sci-Fi" {
		t.Errorf("genres = %q", first.Genres)
	}
}

func Test_parse_titles_column_order_independent(t *testing.T) {
	reordered := "startYear\ttconst\ttitleType\tprimaryTitle\toriginalTitle\tisAdult\tendYear\truntimeMinutes\tgenres\n" +
		"1999\ttt0133093\tmovie\tThe Matrix\tThe Matrix\t0\t\\N\t136\tAction\n"
	titles, err := Parse_titles(strings.NewReader(reordered))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(titles) != 1 || titles[0].Start_year != 1999 || titles[0].Id != "tt0133093" {
		t.Fatalf("titles = %+v", titles)
	}
}

func Test_parse_titles_missing_column(t *testing.T) {
	if _, err := Parse_titles(strings.NewReader("tconst\ttitleType\n")); err == nil {
		t.Fatal("expected error for malformed header")
	}
}

func Test_open_gzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "title.basics.tsv.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gz := gzip.NewWriter(file)
	if _, err := gz.Write([]byte(test_tsv)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	index, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if index.Count() != 7 {
		t.Errorf("count = %d, want 7 catalogued titles (short + videoGame excluded)", index.Count())
	}
	if got := index.Search("The Matrix", 1999, scanner.Movie); len(got) != 1 || got[0].Id != "tt0133093" {
		t.Errorf("search = %+v", got)
	}
}

func Test_search_normalizes_query(t *testing.T) {
	index := must_index(t)
	for _, query := range []string{"The Matrix", "the.matrix", "The  Matrix!", "THE-MATRIX"} {
		got := index.Search(query, 0, scanner.Movie)
		if len(got) == 0 {
			t.Errorf("Search(%q) returned nothing", query)
		}
	}
}

func Test_search_by_original_title(t *testing.T) {
	index := must_index(t)
	got := index.Search("Le Fabuleux Destin d'Amélie Poulain", 0, scanner.Movie)
	if len(got) != 1 || got[0].Id != "tt0000003" {
		t.Fatalf("search = %+v", got)
	}
	if got[0].Title != "Amélie" {
		t.Errorf("primary title = %q, want Amélie", got[0].Title)
	}
}

func Test_search_filters_by_media_type(t *testing.T) {
	index := must_index(t)
	if got := index.Search("Breaking Bad", 0, scanner.Movie); len(got) != 0 {
		t.Errorf("movie search found a series: %+v", got)
	}
	got := index.Search("Breaking Bad", 0, scanner.Series)
	if len(got) != 1 || got[0].Id != "tt0903747" || got[0].Media_type != scanner.Series {
		t.Fatalf("series search = %+v", got)
	}
	if got[0].End_year != 2013 {
		t.Errorf("end year = %d, want 2013", got[0].End_year)
	}
}

func Test_search_unknown_or_stripped_types(t *testing.T) {
	index := must_index(t)
	if got := index.Search("Some Short", 0, scanner.Movie); len(got) != 0 {
		t.Errorf("short should not be catalogued: %+v", got)
	}
	if got := index.Search("Some Game", 0, scanner.Movie); len(got) != 0 {
		t.Errorf("videoGame should not be catalogued: %+v", got)
	}
}

func Test_search_ambiguous_returns_all(t *testing.T) {
	index := must_index(t)
	got := index.Search("Remake", 0, scanner.Movie)
	if len(got) != 2 {
		t.Fatalf("ambiguous search = %+v, want 2", got)
	}
}

func Test_search_empty(t *testing.T) {
	index := must_index(t)
	if got := index.Search("   ", 0, scanner.Movie); got != nil {
		t.Errorf("blank query = %+v", got)
	}
	if got := index.Search("Nonexistent Film", 0, scanner.Movie); len(got) != 0 {
		t.Errorf("unknown query = %+v", got)
	}
}

func Test_lookup(t *testing.T) {
	index := must_index(t)
	title, ok := index.Lookup("tt0133093")
	if !ok || title.Primary_title != "The Matrix" {
		t.Fatalf("lookup = %+v, ok=%v", title, ok)
	}
	if _, ok := index.Lookup("tt9999999"); ok {
		t.Error("unexpected lookup hit")
	}
}

func Test_normalize_title(t *testing.T) {
	cases := map[string]string{
		"The Matrix":               "the matrix",
		"The.Matrix":               "the matrix",
		"À Bientôt":                "à bientôt",
		"Blade Runner 2049 (2017)": "blade runner 2049 2017",
	}
	for in, want := range cases {
		if got := Normalize_title(in); got != want {
			t.Errorf("Normalize_title(%q) = %q, want %q", in, got, want)
		}
	}
}

func Test_matcher_source_adapter(t *testing.T) {
	source := New_matcher_source(must_index(t))
	got, err := source.Search(nil, "The Matrix", 1999, scanner.Movie)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Imdb_id != "tt0133093" || got[0].Title != "The Matrix" {
		t.Fatalf("candidates = %+v", got)
	}
}

func must_index(t *testing.T) *Index {
	t.Helper()
	titles, err := Parse_titles(strings.NewReader(test_tsv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return New_index(titles)
}
