package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/metadata"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

// fake_metadata is a scripted matcher.Metadata_source.
type fake_metadata struct{}

func (fake_metadata) Search_movie(_ context.Context, _ string, _ int) ([]tmdb.Movie_search_result, error) {
	return []tmdb.Movie_search_result{{
		Id: 603, Title: "The Matrix", Release_date: "1999-03-31",
	}}, nil
}

func (fake_metadata) Search_tv(_ context.Context, _ string, _ int) ([]tmdb.Tv_search_result, error) {
	return []tmdb.Tv_search_result{{
		Id: 1396, Name: "Breaking Bad", First_air_date: "2008-01-20",
	}}, nil
}

func (fake_metadata) Movie_details(_ context.Context, id int) (*tmdb.Movie_details, error) {
	return &tmdb.Movie_details{
		Id: id, Imdb_id: "tt0133093", Title: "The Matrix", Original_title: "The Matrix",
		Release_date: "1999-03-31", Overview: "Sci-fi", Runtime: 136,
		Genres: []tmdb.Genre{{Id: 28, Name: "Action"}},
	}, nil
}

func (fake_metadata) Tv_details(_ context.Context, id int) (*tmdb.Tv_details, error) {
	return &tmdb.Tv_details{
		Id: id, Imdb_id: "tt0903747", Name: "Breaking Bad", Original_name: "Breaking Bad",
		First_air_date: "2008-01-20", Last_air_date: "2013-09-29",
		Number_of_seasons: 5, Number_of_episodes: 62,
		Genres: []tmdb.Genre{{Id: 18, Name: "Drama"}},
	}, nil
}

func (fake_metadata) Find_by_imdb(_ context.Context, _ string) (*tmdb.Find_result, error) {
	return &tmdb.Find_result{}, nil
}

func fake_probe(_ context.Context, path string) (*metadata.File_info, error) {
	return &metadata.File_info{
		Path:             path,
		Container:        "matroska",
		Duration_seconds: 120,
		Video: &metadata.Video_stream_info{
			Codec: "h264", Width: 1920, Height: 1080, Resolution_label: "1080p",
		},
		Audio:     []metadata.Audio_stream_info{{Language: "eng", Codec: "aac", Channels: 2}},
		Subtitles: []metadata.Subtitle_stream_info{{Language: "eng", Format: "subrip"}},
	}, nil
}

func no_hash(string) (string, error) { return "", nil }

func new_test_store(t *testing.T) *database.Store {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return database.New_store(d)
}

func new_test_scanner(t *testing.T, store *database.Store, m *matcher.Matcher, dir string) *Scanner {
	t.Helper()
	s := New(store, m, Options{Directories: []string{dir}})
	s.probe = fake_probe
	s.hash = no_hash
	return s
}

func write_file(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func Test_run_groups_versions(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)
	write_file(t, dir, "The.Matrix.1999.2160p.mkv", 2000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, matcher.New(matcher.Options{Metadata: fake_metadata{}}), dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Found != 2 || result.New != 2 || result.Matched != 2 {
		t.Fatalf("result = %+v", result)
	}

	entries, err := store.List_catalog_entries(context.Background(), database.Catalog_filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "The Matrix" {
		t.Fatalf("entries = %+v, want one The Matrix entry", entries)
	}
	versions, _ := store.List_versions_for_entry(context.Background(), entries[0].Id)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2 grouped", len(versions))
	}
	if versions[0].Size_bytes != 2000 {
		t.Errorf("versions not ordered by size desc: %+v", versions)
	}
}

func Test_run_rescan_skips_unchanged(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)
	write_file(t, dir, "The.Matrix.1999.2160p.mkv", 2000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, matcher.New(matcher.Options{Metadata: fake_metadata{}}), dir)

	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.New != 0 || result.Skipped != 2 {
		t.Fatalf("second run = %+v, want 0 new and 2 skipped", result)
	}
}

func Test_run_unmatched_entries(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Mystery.Film.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, matcher.New(matcher.Options{}), dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Unmatched != 1 || result.Matched != 0 {
		t.Fatalf("result = %+v", result)
	}
	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Status: "needs_lookup"})
	if len(entries) != 1 || entries[0].Title != "Mystery Film" {
		t.Fatalf("entries = %+v", entries)
	}
}

func Test_run_marks_missing(t *testing.T) {
	dir := t.TempDir()
	path := write_file(t, dir, "Old.Movie.2001.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, matcher.New(matcher.Options{Metadata: fake_metadata{}}), dir)
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.Missing != 1 {
		t.Fatalf("missing = %d, want 1", result.Missing)
	}
	missing, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Status: "missing"})
	if len(missing) != 1 {
		t.Fatalf("missing entries = %+v", missing)
	}
}

func Test_run_series_episodes(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Breaking.Bad.S01E01.mkv", 1000)
	write_file(t, dir, "Breaking.Bad.S01E02.mkv", 1100)

	store := new_test_store(t)
	s := new_test_scanner(t, store, matcher.New(matcher.Options{Metadata: fake_metadata{}}), dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Matched != 2 {
		t.Fatalf("result = %+v", result)
	}
	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(entries) != 1 || entries[0].Title != "Breaking Bad" {
		t.Fatalf("entries = %+v", entries)
	}
	episodes, _ := store.List_episodes(context.Background(), entries[0].Id)
	if len(episodes) != 2 || episodes[0].Episode_number != 1 || episodes[1].Episode_number != 2 {
		t.Fatalf("episodes = %+v", episodes)
	}
	versions, _ := store.List_versions_for_episode(context.Background(), episodes[0].Id)
	if len(versions) != 1 {
		t.Fatalf("versions = %+v", versions)
	}
}
