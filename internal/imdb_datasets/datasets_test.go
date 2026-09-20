package imdb_datasets

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func Test_parse_akas(t *testing.T) {
	tsv := `titleId	ordering	title	region	language	types	attributes	isOriginalTitle
tt0133093	1	The Matrix	US	en	original		1
tt0133093	2	Matrix	NA	\N	title	in Latin alphabet	0
tt0133093	3	マトリックス	JP	ja	\N	\N	0
tt0903747	1	Breaking Bad	US	en	original		0
`
	akas, err := Parse_akas(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(akas) != 4 {
		t.Fatalf("akas = %d, want 4", len(akas))
	}
	if akas[1].Title != "Matrix" || !akas[0].Is_original {
		t.Errorf("akas[0] = %+v, akas[1] = %+v", akas[0], akas[1])
	}
}

func Test_parse_episodes(t *testing.T) {
	tsv := `tconst	parentTconst	seasonNumber	episodeNumber
tt1586952	tt0903747	1	1
tt1586954	tt0903747	1	2
tt3322312	tt0903747	1	99
`
	episodes, err := Parse_episodes(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(episodes) != 3 || episodes[0].Parent_id != "tt0903747" || episodes[0].Season != 1 {
		t.Fatalf("episodes = %+v", episodes)
	}
}

func Test_parse_ratings(t *testing.T) {
	tsv := `tconst	averageRating	numVotes
tt0133093	8.7	2500000
tt0903747	9.5	2300000
`
	ratings, err := Parse_ratings(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ratings) != 2 {
		t.Fatalf("ratings = %d, want 2", len(ratings))
	}
	if ratings[0].Average_rating != 8.7 || ratings[0].Num_votes != 2500000 {
		t.Errorf("ratings[0] = %+v", ratings[0])
	}
}

func Test_index_episodes_ratings(t *testing.T) {
	index := must_index(t)
	episodes, err := Parse_episodes(strings.NewReader(
		"tconst\tparentTconst\tseasonNumber\tepisodeNumber\n" +
			"tt1586952\ttt0903747\t1\t1\n" +
			"tt0000007\ttt0903747\t0\t1\n"))
	if err != nil {
		t.Fatalf("parse episodes: %v", err)
	}
	index.Index_episodes(episodes)

	episode, ok := index.Episode_lookup("tt0903747", 1, 1)
	if !ok || episode.Id != "tt1586952" {
		t.Errorf("episode lookup = %+v, ok=%v", episode, ok)
	}
	if _, ok := index.Episode_lookup("tt0903747", 2, 1); ok {
		t.Error("unexpected episode hit")
	}
	all := index.Episodes("tt0903747")
	if len(all) != 2 {
		t.Fatalf("episodes = %+v, want 2", all)
	}
	if !all[1].Is_special {
		t.Errorf("season 0 row should be marked special: %+v", all[1])
	}

	ratings, err := Parse_ratings(strings.NewReader("tconst\taverageRating\tnumVotes\ntt0133093\t8.7\t2500000\n"))
	if err != nil {
		t.Fatalf("parse ratings: %v", err)
	}
	index.Index_ratings(ratings)
	got := index.Search("The Matrix", 0, scanner.Movie)
	if len(got) != 1 || got[0].Rating != 8.7 || got[0].Votes != 2500000 {
		t.Errorf("search with rating = %+v", got)
	}
}

func Test_search_by_akas(t *testing.T) {
	index := must_index(t)
	akas, err := Parse_akas(strings.NewReader(
		"titleId\tordering\ttitle\tregion\tlanguage\ttypes\tattributes\tisOriginalTitle\n" +
			"tt0133093\t1\tMatrix\tNA\t\\N\ttitle\t\t0\n" +
			"tt0133093\t2\tThe Matrix\tUS\ten\toriginal\t\t1\n"))
	if err != nil {
		t.Fatalf("parse akas: %v", err)
	}
	index.Index_akas(akas)

	got := index.Search("Matrix", 0, scanner.Movie)
	if len(got) != 1 || got[0].Id != "tt0133093" {
		t.Fatalf("aka search = %+v, want single matrix entry", got)
	}
}

func Test_open_directory_loads_optional_datasets(t *testing.T) {
	dir := t.TempDir()
	write_gz := func(name string, data string) {
		t.Helper()
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		gz := gzip.NewWriter(file)
		if _, err := gz.Write([]byte(data)); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("gz close: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	write_gz("title.basics.tsv.gz", test_tsv)
	write_gz("title.akas.tsv.gz",
		"titleId\tordering\ttitle\tregion\tlanguage\ttypes\tattributes\tisOriginalTitle\n"+
			"tt0133093\t1\tMatrix\tNA\t\\N\ttitle\t\tfalse\n")
	write_gz("title.episode.tsv.gz",
		"tconst\tparentTconst\tseasonNumber\tepisodeNumber\n"+
			"tt1586952\ttt0903747\t1\t1\n")

	index, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	if index.Count() != 7 {
		t.Errorf("count = %d, want 7", index.Count())
	}
	if got := index.Search("Matrix", 0, scanner.Movie); len(got) != 1 || got[0].Id != "tt0133093" {
		t.Errorf("aka search = %+v", got)
	}
	if got := index.Search("The Matrix", 0, scanner.Movie); len(got) != 1 {
		t.Errorf("primary search = %+v", got)
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

	index, err := Open(path, nil)
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

func Test_open_reuses_and_rebuilds_index(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "title.basics.tsv"), []byte(test_tsv), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	index, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if index.Count() != 7 {
		t.Fatalf("count = %d, want 7", index.Count())
	}
	if err := index.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	index, err = Open(dir, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := index.Search("The Matrix", 0, scanner.Movie); len(got) != 1 || got[0].Id != "tt0133093" {
		t.Errorf("search after reopen = %+v", got)
	}
	_ = index.Close()

	// A dataset newer than the index triggers a rebuild.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "title.basics.tsv"), now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	index, err = Open(dir, nil)
	if err != nil {
		t.Fatalf("reopen after touch: %v", err)
	}
	defer index.Close()
	if index.Count() != 7 {
		t.Errorf("count after rebuild = %d, want 7", index.Count())
	}
}

func Test_open_single_file_builds_sidecar_index(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "title.basics.tsv")
	if err := os.WriteFile(path, []byte(test_tsv), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	index, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer index.Close()
	if index.Count() != 7 {
		t.Errorf("count = %d, want 7", index.Count())
	}
	if _, err := os.Stat(filepath.Join(dir, Index_db_name)); err != nil {
		t.Errorf("sidecar index not created: %v", err)
	}
}

func Test_open_with_progress_reports_build_stages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "title.basics.tsv"), []byte(test_tsv), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var events []Build_progress
	index, err := Open_with_progress(dir, nil, func(p Build_progress) {
		events = append(events, p)
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	var got_stale, got_import, got_ready, got_basics_done bool
	for _, p := range events {
		switch p.Step {
		case Build_stale:
			got_stale = true
		case Build_import:
			if p.Dataset == "title.basics" {
				got_import = true
				if p.Done && p.Rows != 9 {
					t.Errorf("basics done rows = %d, want 9", p.Rows)
				}
			}
			if p.Dataset == "title.basics" && p.Done {
				got_basics_done = true
			}
		case Build_ready:
			got_ready = true
		}
	}
	if !got_stale || !got_import || !got_basics_done || !got_ready {
		t.Errorf("events = %+v, want stale/import/ready with basics done", events)
	}

	_ = index.Close()

	var reuse *Build_progress
	index, err = Open_with_progress(dir, nil, func(p Build_progress) {
		if p.Step == Build_reuse {
			reuse = &p
		}
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer index.Close()
	if reuse == nil {
		t.Error("expected a reuse event when reopening a fresh index")
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

func Test_matcher_source_offline_lookup(t *testing.T) {
	index := must_index(t)
	if _, ok := index.Lookup("tt0133093"); !ok {
		t.Fatal("fixture missing tt0133093")
	}
	ratings, err := Parse_ratings(strings.NewReader("tconst\taverageRating\tnumVotes\ntt0133093\t8.7\t2500000\n"))
	if err != nil {
		t.Fatalf("parse ratings: %v", err)
	}
	index.Index_ratings(ratings)

	source := New_matcher_source(index)
	result, ok := source.Lookup(nil, "tt0133093")
	if !ok {
		t.Fatal("lookup reported not found for known id")
	}
	if !result.Matched || result.Source != "imdb-datasets" || result.Media_type != scanner.Movie {
		t.Fatalf("result = %+v", result)
	}
	if result.Title != "The Matrix" || result.Year != 1999 {
		t.Errorf("title/year = %q/%d", result.Title, result.Year)
	}
	if result.Rating != 8.7 || result.Vote_count != 2500000 {
		t.Errorf("rating/votes = %v/%d", result.Rating, result.Vote_count)
	}

	if _, ok := source.Lookup(nil, "tt9999999"); ok {
		t.Error("lookup reported found for unknown id")
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
