package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/metadata"
	"github.com/cybersouris/tomovee/internal/thumbnail"
)

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

func new_test_scanner(t *testing.T, store *database.Store, dir string) *Scanner {
	t.Helper()
	if err := store.Ensure_library(context.Background(), "Media", dir, false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	s := New(store, Options{})
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

func mkdir_all(t *testing.T, dir, sub string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
}

func Test_run_groups_versions(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)
	write_file(t, dir, "The.Matrix.1999.2160p.mkv", 2000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Found != 2 || result.New != 2 {
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
	s := new_test_scanner(t, store, dir)

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

func Test_run_stores_hash_offline(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Mystery.Film.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)
	s.hash = func(string) (string, error) { return "abc-hash-123", nil }

	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	versions, _ := store.List_versions_for_entry(context.Background(), entries[0].Id)
	if len(versions) != 1 || versions[0].Hash != "abc-hash-123" {
		t.Fatalf("versions = %+v, want stored hash", versions)
	}
}

func Test_run_keeps_matched_entries_untouched(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)

	store := new_test_store(t)
	ctx := context.Background()
	matched_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Original_title: "The Matrix",
		Release_year: 1999, Imdb_id: "tt0133093", Tmdb_id: 603,
		Overview: "A hacker learns the truth.", Rating: 8.2,
		Status: "matched", Genres: []string{"Action"},
	})
	if err != nil {
		t.Fatalf("upsert matched entry: %v", err)
	}

	s := new_test_scanner(t, store, dir)
	if _, err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	entry, err := store.Get_catalog_entry(ctx, matched_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Imdb_id != "tt0133093" || entry.Overview != "A hacker learns the truth." {
		t.Fatalf("matched entry was degraded: %+v", entry)
	}
	versions, _ := store.List_versions_for_entry(ctx, matched_id)
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want the new file attached", len(versions))
	}
}

func Test_run_unmatched_entries(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Mystery.Film.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 1 {
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
	s := new_test_scanner(t, store, dir)
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

func Test_run_libraries_scans_only_named_library(t *testing.T) {
	dir_a := t.TempDir()
	dir_b := t.TempDir()
	write_file(t, dir_a, "Film.A.mkv", 1000)
	write_file(t, dir_b, "Film.B.mkv", 1000)

	store := new_test_store(t)
	ctx := context.Background()
	s := new_test_scanner(t, store, dir_a)
	if err := store.Ensure_library(ctx, "B", dir_b, false); err != nil {
		t.Fatalf("register B: %v", err)
	}

	result, err := s.Run_libraries(ctx, []string{"B"}, nil)
	if err != nil {
		t.Fatalf("run B: %v", err)
	}
	if result.Found != 1 || result.New != 1 {
		t.Fatalf("result = %+v, want only B scanned", result)
	}
	entries, _ := store.List_catalog_entries(ctx, database.Catalog_filter{})
	if len(entries) != 1 || entries[0].Title != "Film B" {
		t.Fatalf("entries = %+v", entries)
	}

	libraries, _ := store.List_libraries(ctx)
	var lib_a database.Library
	for _, library := range libraries {
		if library.Name == "A" {
			lib_a = library
		}
	}
	if ref, found, err := store.Find_version_in_library(ctx, lib_a.Id, "Film.A.mkv"); err != nil || found {
		t.Fatalf("library A must not have been scanned: found=%v err=%v ref=%+v", found, err, ref)
	}

	if _, err := s.Run_libraries(ctx, []string{"Nope"}, nil); err == nil {
		t.Fatal("expected error for unknown library")
	}
}

func Test_run_unknown_library_only_errors(t *testing.T) {
	store := new_test_store(t)
	s := New(store, Options{})
	if _, err := s.Run_libraries(context.Background(), []string{"Nope"}, nil); err == nil {
		t.Fatal("expected error for unknown library")
	}
}

func Test_run_normalizes_legacy_absolute_version(t *testing.T) {
	dir := t.TempDir()
	path := write_file(t, dir, "Old.Movie.mkv", 1000)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	store := new_test_store(t)
	ctx := context.Background()
	entry, _ := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "Old Movie", Status: "needs_lookup",
	})
	// A pre-library row: absolute path, no library.
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry, File_path: path, Size_bytes: info.Size(),
		Mtime: format_mtime(info.ModTime()),
	}); err != nil {
		t.Fatalf("legacy save: %v", err)
	}

	scanner := new_test_scanner(t, store, dir)
	result, err := scanner.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the legacy file skipped, not re-inserted", result)
	}
	libraries, _ := store.List_libraries(ctx)
	versions, _ := store.List_versions_for_entry(ctx, entry)
	if len(versions) != 1 {
		t.Fatalf("versions = %+v", versions)
	}
	if versions[0].Library_id != libraries[0].Id || versions[0].File_path != "Old.Movie.mkv" {
		t.Fatalf("version not normalized: %+v (library %+v)", versions[0], libraries[0])
	}
}

func Test_run_extracts_frame_poster(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Mystery.Film.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)
	poster_dir := t.TempDir()
	s.opts.Poster_dir = poster_dir
	s.extract = func(_ context.Context, _ string, _ float64, out string) error {
		return os.WriteFile(out, []byte("fake-jpeg"), 0o644)
	}

	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	if entries[0].Poster_path != thumbnail.Local_marker {
		t.Fatalf("poster_path = %q, want %q", entries[0].Poster_path, thumbnail.Local_marker)
	}
	local := thumbnail.Local_path(poster_dir, entries[0].Id)
	if data, err := os.ReadFile(local); err != nil || string(data) != "fake-jpeg" {
		t.Fatalf("frame poster missing: %v (%q)", err, data)
	}
}

func Test_run_series_episodes(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Breaking.Bad.S01E01.mkv", 1000)
	write_file(t, dir, "Breaking.Bad.S01E02.mkv", 1100)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 2 {
		t.Fatalf("result = %+v", result)
	}
	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(entries) != 1 || entries[0].Title != "Breaking Bad" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Status != "needs_lookup" {
		t.Fatalf("entry status = %q, want needs_lookup", entries[0].Status)
	}
	episodes, _ := store.List_episodes(context.Background(), entries[0].Id)
	if len(episodes) != 2 || episodes[0].Episode_number != 1 || episodes[1].Episode_number != 2 {
		t.Fatalf("episodes = %+v", episodes)
	}
	if episodes[0].Status != "needs_lookup" {
		t.Fatalf("episode status = %q, want needs_lookup", episodes[0].Status)
	}
	versions, _ := store.List_versions_for_episode(context.Background(), episodes[0].Id)
	if len(versions) != 1 {
		t.Fatalf("versions = %+v", versions)
	}
}

func Test_run_series_grouped_by_folder(t *testing.T) {
	dir := t.TempDir()
	mkdir_all(t, dir, "Breaking.Bad/Season 1")
	mkdir_all(t, dir, "Lego.Movie.2014")
	write_file(t, dir, "Breaking.Bad/Season 1/Breaking.Bad.S01E01.mkv", 1000)
	write_file(t, dir, "Breaking.Bad/Season 1/Breaking.Bad.S01E02.mkv", 1100)
	write_file(t, dir, "Breaking.Bad/Season 1/Breaking.Bad.First.Look.mkv", 1200)
	write_file(t, dir, "Lego.Movie.2014/Lego.Movie.2014.mkv", 1300)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 4 || result.Found != 4 {
		t.Fatalf("result = %+v, want 4 found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != "Breaking Bad" {
		t.Fatalf("series entries = %+v, want one Breaking Bad", series)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 2 {
		t.Fatalf("episodes = %+v, want the 2 numbered episodes", episodes)
	}
	direct, _ := store.List_versions_for_entry(context.Background(), series[0].Id)
	if len(direct) != 1 {
		t.Fatalf("series-level versions = %d, want the folded extra attached to the show", len(direct))
	}

	movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"})
	if len(movies) != 1 || movies[0].Title != "Lego Movie" {
		t.Fatalf("movie entries = %+v, want one Lego Movie untouched", movies)
	}
}

func Test_run_series_folder_groups_across_even_scan(t *testing.T) {
	dir := t.TempDir()
	mkdir_all(t, dir, "The.Office/Season 1")
	mkdir_all(t, dir, "The.Office/Season 2")
	write_file(t, dir, "The.Office/Season 1/The.Office.S01E01.mkv", 1000)
	write_file(t, dir, "The.Office/Season 2/The.Office.S02E01.mkv", 1100)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != "The Office" {
		t.Fatalf("series = %+v, want one The Office across season folders", series)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 2 {
		t.Fatalf("episodes = %+v, want episodes from both seasons grouped", episodes)
	}
}
