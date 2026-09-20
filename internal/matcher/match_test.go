package matcher

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/opensubtitles"
	"github.com/cybersouris/tomovee/internal/scanner"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

// fake_subtitles is a scripted Subtitles_source for tests.
type fake_subtitles struct {
	features []opensubtitles.Feature
	err      error
}

func (f *fake_subtitles) Search_by_hash(_ context.Context, _ string) ([]opensubtitles.Feature, error) {
	return f.features, f.err
}

// fake_offline is a scripted Offline_source for tests. calls counts how often
// the offline layer was consulted, so precedence can be asserted.
type fake_offline struct {
	candidates []Offline_candidate
	err        error
	calls      int
}

func (f *fake_offline) Search(_ context.Context, _ string, _ int, _ scanner.Media_type) ([]Offline_candidate, error) {
	f.calls++
	return f.candidates, f.err
}

// fake_metadata is a scripted Metadata_source for tests.
type fake_metadata struct {
	movies  []tmdb.Movie_search_result
	tv      []tmdb.Tv_search_result
	details map[int]any // *tmdb.Movie_details or *tmdb.Tv_details
	find    *tmdb.Find_result

	search_err error
	detail_err error
	find_err   error
}

func (f *fake_metadata) Search_movie(_ context.Context, _ string, _ int) ([]tmdb.Movie_search_result, error) {
	return f.movies, f.search_err
}

func (f *fake_metadata) Search_tv(_ context.Context, _ string, _ int) ([]tmdb.Tv_search_result, error) {
	return f.tv, f.search_err
}

func (f *fake_metadata) Movie_details(_ context.Context, id int) (*tmdb.Movie_details, error) {
	if f.detail_err != nil {
		return nil, f.detail_err
	}
	if d, ok := f.details[id].(*tmdb.Movie_details); ok {
		return d, nil
	}
	return nil, nil
}

func (f *fake_metadata) Tv_details(_ context.Context, id int) (*tmdb.Tv_details, error) {
	if f.detail_err != nil {
		return nil, f.detail_err
	}
	if d, ok := f.details[id].(*tmdb.Tv_details); ok {
		return d, nil
	}
	return nil, nil
}

func (f *fake_metadata) Find_by_imdb(_ context.Context, _ string) (*tmdb.Find_result, error) {
	return f.find, f.find_err
}

func matrix_movie_details() *tmdb.Movie_details {
	return &tmdb.Movie_details{
		Id:             603,
		Imdb_id:        "tt0133093",
		Title:          "The Matrix",
		Original_title: "The Matrix",
		Release_date:   "1999-03-31",
		Overview:       "A hacker learns the truth.",
		Poster_path:    "/matrix.jpg",
		Vote_average:   8.2,
		Vote_count:     20000,
		Runtime:        136,
		Genres:         []tmdb.Genre{{Id: 28, Name: "Action"}, {Id: 878, Name: "Science Fiction"}},
	}
}

func breaking_bad_tv_details() *tmdb.Tv_details {
	return &tmdb.Tv_details{
		Id:                 1396,
		Imdb_id:            "tt0903747",
		Name:               "Breaking Bad",
		Original_name:      "Breaking Bad",
		First_air_date:     "2008-01-20",
		Overview:           "A chemistry teacher cooks.",
		Poster_path:        "/bb.jpg",
		Vote_average:       9.0,
		Vote_count:         5000,
		Number_of_seasons:  5,
		Number_of_episodes: 62,
		Genres:             []tmdb.Genre{{Id: 18, Name: "Drama"}},
	}
}

func Test_match_by_hash_movie(t *testing.T) {
	subs := &fake_subtitles{features: []opensubtitles.Feature{{
		Title: "The Matrix", Year: 1999, Imdb_id: "tt0133093",
		Tmdb_id: 603, Feature_type: "movie", Download_count: 500,
	}}}
	meta := &fake_metadata{details: map[int]any{603: matrix_movie_details()}}
	m := New(Options{Subtitles: subs, Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv",
		Hash: "8e245d9679d31e12", Kind: scanner.Movie,
	})
	if !result.Matched {
		t.Fatalf("expected a hash match, got %+v", result)
	}
	if result.Source != "opensubtitles" || result.Tmdb_id != 603 || result.Imdb_id != "tt0133093" {
		t.Errorf("identity = %+v", result)
	}
	if result.Title != "The Matrix" || result.Year != 1999 {
		t.Errorf("title/year = %q/%d", result.Title, result.Year)
	}
	if result.Overview == "" || result.Runtime_minutes != 136 || len(result.Genres) != 2 {
		t.Errorf("enrichment missing: %+v", result)
	}
	if result.Confidence != confidence_hash {
		t.Errorf("confidence = %v, want %v", result.Confidence, confidence_hash)
	}
}

func Test_match_by_hash_series_via_imdb(t *testing.T) {
	subs := &fake_subtitles{features: []opensubtitles.Feature{{
		Title: "Breaking Bad", Year: 2008, Imdb_id: "tt0903747",
		Feature_type: "episode", Season: 1, Episode: 1,
	}}}
	find := &tmdb.Find_result{Tv_results: []tmdb.Tv_search_result{{Id: 1396, Name: "Breaking Bad"}}}
	meta := &fake_metadata{details: map[int]any{1396: breaking_bad_tv_details()}, find: find}
	m := New(Options{Subtitles: subs, Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/s/Breaking.Bad.S01E01.mkv", File_name: "Breaking.Bad.S01E01.mkv",
		Hash: "abc123", Kind: scanner.Series,
	})
	if !result.Matched {
		t.Fatalf("expected hash match, got %+v", result)
	}
	if result.Source != "opensubtitles" || result.Tmdb_id != 1396 || result.Imdb_id != "tt0903747" {
		t.Errorf("identity = %+v", result)
	}
	if result.Media_type != scanner.Series || result.Number_of_seasons != 5 || result.Number_of_episodes != 62 {
		t.Errorf("series enrichment = %+v", result)
	}
}

func Test_match_by_hash_error_falls_back_to_search(t *testing.T) {
	subs := &fake_subtitles{err: context.DeadlineExceeded}
	meta := &fake_metadata{
		movies:  []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}},
		details: map[int]any{603: matrix_movie_details()},
	}
	m := New(Options{Subtitles: subs, Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv",
		Hash: "x", Kind: scanner.Movie,
	})
	if !result.Matched || result.Source != "tmdb" {
		t.Fatalf("expected tmdb fallback match, got %+v", result)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning about the failed hash lookup")
	}
}

func Test_match_by_search_movie_high_confidence(t *testing.T) {
	meta := &fake_metadata{
		movies:  []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}},
		details: map[int]any{603: matrix_movie_details()},
	}
	m := New(Options{Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.BluRay.mkv", File_name: "The.Matrix.1999.BluRay.mkv",
		Kind: scanner.Movie,
	})
	if !result.Matched || result.Tmdb_id != 603 || result.Imdb_id != "tt0133093" {
		t.Fatalf("expected matched movie, got %+v", result)
	}
	if result.Overview == "" || result.Runtime_minutes != 136 {
		t.Errorf("full metadata missing: %+v", result)
	}
	if len(result.Candidates) != 1 {
		t.Errorf("candidates = %d, want 1", len(result.Candidates))
	}
}

func Test_match_by_search_low_confidence(t *testing.T) {
	meta := &fake_metadata{
		movies: []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}},
	}
	m := New(Options{Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/m/A.Totally.Different.Film.mkv", File_name: "A.Totally.Different.Film.mkv",
		Kind: scanner.Movie,
	})
	if result.Matched {
		t.Fatalf("expected no confident match, got %+v", result)
	}
	if result.Title != "A Totally Different Film" {
		t.Errorf("parsed title = %q", result.Title)
	}
	if len(result.Candidates) == 0 {
		t.Error("expected candidates for manual review")
	}
}

func Test_match_by_search_series(t *testing.T) {
	meta := &fake_metadata{
		tv:      []tmdb.Tv_search_result{{Id: 1396, Name: "Breaking Bad", First_air_date: "2008-01-20"}},
		details: map[int]any{1396: breaking_bad_tv_details()},
	}
	m := New(Options{Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/s/Breaking.Bad.S01E01.mkv", File_name: "Breaking.Bad.S01E01.mkv",
		Kind: scanner.Series,
	})
	if !result.Matched || result.Media_type != scanner.Series || result.Tmdb_id != 1396 {
		t.Fatalf("expected series match, got %+v", result)
	}
	if result.Number_of_seasons != 5 || result.Number_of_episodes != 62 {
		t.Errorf("series details = %+v", result)
	}
}

func Test_match_derives_title_from_file_name(t *testing.T) {
	m := New(Options{})
	result := m.Match(context.Background(), Input{
		Path: "/m/No.Year.Movie.mkv", File_name: "No.Year.Movie.mkv", Kind: scanner.Movie,
	})
	if result.Title != "No Year Movie" {
		t.Errorf("title = %q", result.Title)
	}
	if result.Matched {
		t.Error("no sources → must not match")
	}
	found := false
	for _, w := range result.Warnings {
		if w == "no metadata source configured" {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a no-source warning", result.Warnings)
	}
}

func Test_match_with_no_derivable_title(t *testing.T) {
	meta := &fake_metadata{movies: []tmdb.Movie_search_result{{Id: 1, Title: "Whatever"}}}
	m := New(Options{Metadata: meta})

	result := m.Match(context.Background(), Input{
		Path: "/m/1080p.mkv", File_name: "1080p.mkv", Kind: scanner.Movie,
	})
	if result.Matched {
		t.Fatalf("unexpected match: %+v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "could not derive a title from the file name" {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func Test_match_debug_logs_reasoning(t *testing.T) {
	var buf bytes.Buffer
	debug_logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := New(Options{
		Metadata: &fake_metadata{
			movies: []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}},
			details: map[int]any{
				603: matrix_movie_details(),
			},
		},
		Logger: debug_logger,
	})

	result := m.Match(context.Background(), Input{
		Path: "/m/Matrix.1999.mkv", File_name: "Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if !result.Matched {
		t.Fatalf("expected a match: %+v", result)
	}
	output := buf.String()
	if !strings.Contains(output, "match: start") {
		t.Errorf("debug output missing start log:\n%s", output)
	}
	if !strings.Contains(output, "match: tmdb movie candidate") {
		t.Errorf("debug output missing candidate log:\n%s", output)
	}
	if !strings.Contains(output, "match: search accepted") {
		t.Errorf("debug output missing accepted log:\n%s", output)
	}
	if !strings.Contains(output, "score=") {
		t.Errorf("debug output missing score attribute:\n%s", output)
	}
}

func Test_pick_feature(t *testing.T) {
	movie := opensubtitles.Feature{Feature_type: "movie", Download_count: 10}
	episode := opensubtitles.Feature{Feature_type: "episode", Download_count: 100}
	tvshow := opensubtitles.Feature{Feature_type: "tvshow", Download_count: 50}

	cases := []struct {
		name     string
		features []opensubtitles.Feature
		kind     scanner.Media_type
		want     string
	}{
		{"prefers episode for series", []opensubtitles.Feature{movie, episode}, scanner.Series, "episode"},
		{"prefers download champion", []opensubtitles.Feature{movie, tvshow}, scanner.Series, "tvshow"},
		{"movie when only other feature", []opensubtitles.Feature{movie}, scanner.Series, "movie"},
		{"episode feature for movie kind", []opensubtitles.Feature{episode}, scanner.Movie, "episode"},
	}
	for _, c := range cases {
		got, ok := pick_feature(c.features, c.kind)
		if !ok || got.Feature_type != c.want {
			t.Errorf("pick_feature(%s) = %+v, ok=%v; want %s", c.name, got, ok, c.want)
		}
	}

	if _, ok := pick_feature(nil, scanner.Movie); ok {
		t.Error("expected no match for empty features")
	}
}

func Test_media_type_for_feature(t *testing.T) {
	if got := media_type_for_feature(opensubtitles.Feature{Feature_type: "movie"}); got != scanner.Movie {
		t.Errorf("movie → %v", got)
	}
	for _, ft := range []string{"episode", "tvshow"} {
		if got := media_type_for_feature(opensubtitles.Feature{Feature_type: ft}); got != scanner.Series {
			t.Errorf("%s → %v", ft, got)
		}
	}
	if got := media_type_for_feature(opensubtitles.Feature{Feature_type: "weird"}); got != scanner.Movie {
		t.Errorf("unknown → %v", got)
	}
}

func Test_genre_names(t *testing.T) {
	genres := []tmdb.Genre{{Id: 28, Name: "Action"}, {Id: 0, Name: ""}, {Id: 18, Name: "Drama"}}
	got := genre_names(genres)
	if len(got) != 2 || got[0] != "Action" || got[1] != "Drama" {
		t.Errorf("genre_names = %v", got)
	}
}

func Test_top_candidates(t *testing.T) {
	all := make([]Candidate, 10)
	if got := top_candidates(all, max_candidates); len(got) != max_candidates {
		t.Errorf("capped = %d, want %d", len(got), max_candidates)
	}
	short := make([]Candidate, 3)
	if got := top_candidates(short, max_candidates); len(got) != 3 {
		t.Errorf("short = %d, want 3", len(got))
	}
}

func Test_set_offline_activates_layer(t *testing.T) {
	m := New(Options{})
	if m.Has_sources() {
		t.Fatal("matcher without sources reports configured")
	}
	offline := &fake_offline{candidates: []Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}}
	m.Set_offline(offline)
	if !m.Has_sources() {
		t.Fatal("matcher does not report sources after Set_offline")
	}
	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if !result.Matched || result.Source != "imdb-datasets" || result.Imdb_id != "tt0133093" {
		t.Fatalf("expected offline match after Set_offline, got %+v", result)
	}
	m.Set_offline(nil)
	if m.Has_sources() {
		t.Fatal("matcher still reports sources after Set_offline(nil)")
	}
	result = m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if len(result.Candidates) != 0 {
		t.Errorf("offline consulted after Set_offline(nil): %+v", result.Candidates)
	}
}

func Test_set_sources_swaps_online_layers(t *testing.T) {
	sub := &fake_subtitles{features: []opensubtitles.Feature{{
		Title: "The Matrix", Year: 1999, Imdb_id: "tt0133093",
		Tmdb_id: 603, Feature_type: "movie", Download_count: 500,
	}}}
	meta := &fake_metadata{details: map[int]any{603: matrix_movie_details()}}
	m := New(Options{})

	m.Set_sources(sub, meta)
	if !m.Has_sources() {
		t.Fatal("matcher without sources after Set_sources")
	}
	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
		Hash: "deadbeef",
	})
	if result.Source != "opensubtitles" {
		t.Fatalf("expected hash match via swapped subtitles source, got %+v", result)
	}

	m.Set_sources(nil, nil)
	if m.Has_sources() {
		t.Fatal("matcher still reports sources after Set_sources(nil, nil)")
	}
	result = m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
		Hash: "deadbeef",
	})
	if len(result.Candidates) != 0 {
		t.Errorf("online sources consulted after Set_sources(nil, nil): %+v", result.Candidates)
	}
}

func Test_match_offline_single_candidate(t *testing.T) {
	offline := &fake_offline{candidates: []Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}}
	m := New(Options{Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if !result.Matched || result.Source != "imdb-datasets" || result.Imdb_id != "tt0133093" {
		t.Fatalf("expected offline match, got %+v", result)
	}
	if result.Title != "The Matrix" || result.Year != 1999 {
		t.Errorf("title/year = %q/%d", result.Title, result.Year)
	}
}

func Test_match_offline_ambiguous_leaves_for_review(t *testing.T) {
	offline := &fake_offline{candidates: []Offline_candidate{
		{Imdb_id: "tt0000005", Title: "Remake", Year: 2000, Media_type: scanner.Movie},
		{Imdb_id: "tt0000006", Title: "Remake", Year: 2000, Media_type: scanner.Movie},
	}}
	m := New(Options{Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/Remake.2000.mkv", File_name: "Remake.2000.mkv", Kind: scanner.Movie,
	})
	if result.Matched {
		t.Fatalf("ambiguous offline result must not auto-match: %+v", result)
	}
	if len(result.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(result.Candidates))
	}
}

func Test_match_offline_low_confidence_leaves_for_review(t *testing.T) {
	offline := &fake_offline{candidates: []Offline_candidate{{
		Imdb_id: "tt0133093", Title: "Completely Different", Year: 1999, Media_type: scanner.Movie,
	}}}
	m := New(Options{Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if result.Matched {
		t.Fatalf("weak offline result must not auto-match: %+v", result)
	}
	if len(result.Candidates) == 0 {
		t.Error("expected offline candidates for manual review")
	}
}

func Test_match_offline_appends_after_weak_tmdb_search(t *testing.T) {
	meta := &fake_metadata{
		movies: []tmdb.Movie_search_result{{Id: 999, Title: "Unrelated Picture", Release_date: "1980-01-01"}},
	}
	offline := &fake_offline{candidates: []Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}}
	m := New(Options{Metadata: meta, Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if !result.Matched || result.Source != "imdb-datasets" {
		t.Fatalf("expected offline fallback, got %+v", result)
	}
	if len(result.Candidates) != 2 {
		t.Errorf("candidates = %d, want tmdb + offline", len(result.Candidates))
	}
}

func Test_match_offline_not_consulted_when_tmdb_matches(t *testing.T) {
	meta := &fake_metadata{
		movies:  []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}},
		details: map[int]any{603: matrix_movie_details()},
	}
	offline := &fake_offline{candidates: []Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}}
	m := New(Options{Metadata: meta, Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if !result.Matched || result.Source != "tmdb" {
		t.Fatalf("expected tmdb match, got %+v", result)
	}
	if offline.calls != 0 {
		t.Errorf("offline consulted %d times after a tmdb match, want 0", offline.calls)
	}
}

func Test_match_offline_error_records_warning(t *testing.T) {
	offline := &fake_offline{err: context.DeadlineExceeded}
	m := New(Options{Offline: offline})

	result := m.Match(context.Background(), Input{
		Path: "/m/The.Matrix.1999.mkv", File_name: "The.Matrix.1999.mkv", Kind: scanner.Movie,
	})
	if result.Matched {
		t.Fatalf("unexpected match: %+v", result)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning about the offline lookup")
	}
}
