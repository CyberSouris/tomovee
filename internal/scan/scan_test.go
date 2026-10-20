package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
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
		Media_type: "movie", Title: "The Matrix",
		Release_year: 1999, Imdb_id: "tt0133093", Tmdb_id: 603,
		Genres: []string{"Action"},
	})
	if err != nil {
		t.Fatalf("upsert matched entry: %v", err)
	}
	// A title is matched once it owns a matched file; before the scan its file
	// is a placeholder in another library, so the new file joins the same entry.
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: matched_id, File_path: "existing.mkv", Size_bytes: 1,
		Status: "matched",
	}); err != nil {
		t.Fatalf("save matched file: %v", err)
	}

	s := new_test_scanner(t, store, dir)
	if _, err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	entry, err := store.Get_catalog_entry(ctx, matched_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if !entry.Matched || entry.Imdb_id != "tt0133093" {
		t.Fatalf("matched entry was degraded: %+v", entry)
	}
	versions, _ := store.List_versions_for_entry(ctx, matched_id)
	if len(versions) != 2 {
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

// One file that cannot be read is recorded against that file and the scan goes
// on: a single unreadable video must not cost the whole library its scan.
func Test_run_records_per_file_failures_and_keeps_going(t *testing.T) {
	dir := t.TempDir()
	broken := write_file(t, dir, "Broken.2020.mkv", 1000)
	write_file(t, dir, "Good.2021.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)
	s.probe = func(ctx context.Context, path string) (*metadata.File_info, error) {
		if path == broken {
			return nil, errors.New("ffprobe could not read the file")
		}
		return fake_probe(ctx, path)
	}

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 1 {
		t.Errorf("new = %d, want the readable file only", result.New)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "ffprobe could not read the file") {
		t.Fatalf("errors = %v, want the probe failure against the broken file", result.Errors)
	}
	if !strings.Contains(result.Errors[0], "Broken.2020.mkv") {
		t.Errorf("error = %q, want the failing file named", result.Errors[0])
	}
	entries, err := store.List_catalog_entries(context.Background(), database.Catalog_filter{Status: "needs_lookup"})
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "Good" {
		t.Errorf("entries = %+v, want only the readable file", entries)
	}
}

// A file that vanishes from disk loses its row, and a title left with no files
// is pruned rather than kept as a stale "missing" entry.
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
	entries, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{})
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want the orphaned entry pruned", entries)
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

// Test_force_libraries_rebuilds_catalog verifies the WebUI force rescan: a plain
// rescan skips unchanged files and keeps existing decisions, while a forced one
// drops the derived catalog and rebuilds every entry from the files.
func Test_force_libraries_rebuilds_catalog(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)

	store := new_test_store(t)
	ctx := context.Background()
	s := new_test_scanner(t, store, dir)

	if _, err := s.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	entries, _ := store.List_catalog_entries(ctx, database.Catalog_filter{})
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	stale_id := entries[0].Id
	if err := store.Set_entry_files_status(ctx, stale_id, "matched"); err != nil {
		t.Fatalf("mark matched: %v", err)
	}

	// A plain rescan skips the unchanged file and keeps the match.
	if result, err := s.Run(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	} else if result.Skipped != 1 || result.New != 0 {
		t.Fatalf("plain rescan = %+v, want 1 skipped and 0 new", result)
	}
	if entry, _ := store.Get_catalog_entry(ctx, stale_id); entry == nil || !entry.Matched {
		t.Fatalf("plain rescan dropped the match: %+v", entry)
	}

	// The force rescan drops the derived catalog and rebuilds it from the files.
	result, err := s.Force_libraries(ctx, nil, nil)
	if err != nil {
		t.Fatalf("force run: %v", err)
	}
	if result.New != 1 || result.Skipped != 0 {
		t.Fatalf("force run = %+v, want 1 new and 0 skipped", result)
	}
	if entry, _ := store.Get_catalog_entry(ctx, stale_id); entry != nil {
		t.Fatalf("force run kept the stale entry: %+v", entry)
	}
	entries, _ = store.List_catalog_entries(ctx, database.Catalog_filter{})
	if len(entries) != 1 {
		t.Fatalf("entries after force = %+v, want 1 rebuilt", entries)
	}
	if entries[0].Id == stale_id || entries[0].Matched {
		t.Fatalf("entry not rebuilt from scratch: %+v", entries[0])
	}
	if versions, _ := store.List_versions_for_entry(ctx, entries[0].Id); len(versions) != 1 {
		t.Fatalf("versions after force = %+v, want 1", versions)
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
		Media_type: "movie", Title: "Old Movie",
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
	if entries[0].Matched {
		t.Fatalf("entry matched = true, want an unmatched series")
	}
	episodes, _ := store.List_episodes(context.Background(), entries[0].Id)
	if len(episodes) != 2 || episodes[0].Episode_number != 1 || episodes[1].Episode_number != 2 {
		t.Fatalf("episodes = %+v", episodes)
	}
	versions, _ := store.List_versions_for_episode(context.Background(), episodes[0].Id)
	if len(versions) != 1 {
		t.Fatalf("versions = %+v", versions)
	}
	if versions[0].Status != "needs_lookup" {
		t.Fatalf("file status = %q, want needs_lookup", versions[0].Status)
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

// A show whose season folders are prefixed with the show name, and whose
// episodes each live in their own per-episode folder, must still end up as one
// series named after the show. It also covers the scene habit of joining the
// show name to the season token with an underscore
// ("Show.Name_S03E16_Episode.avi"), which must not turn the file into a movie.
func Test_run_series_with_prefixed_season_and_episode_folders(t *testing.T) {
	dir := t.TempDir()
	const show = "The Rangers Of Pinewood"
	season := "Series/" + show + "/" + show + " Season "
	episode := "Series/" + show + "/" + show + " Season 1/"
	mkdir_all(t, dir, episode+show+" 1x02 - Night Watch")
	mkdir_all(t, dir, episode+show+" 1x13 - Double Watch")
	mkdir_all(t, dir, season+"3")
	mkdir_all(t, dir, season+"6")
	mkdir_all(t, dir, season+"7")

	write_file(t, dir, episode+show+" 1x02 - Night Watch/"+show+" 1x02 - Night Watch.avi", 1000)
	write_file(t, dir, episode+show+" 1x02 - Night Watch/Episode Summary & Details.txt", 1001)
	write_file(t, dir, episode+show+" 1x13 - Double Watch/"+show+" 1x13 - Double Watch.avi", 1002)
	write_file(t, dir, season+"3/The.Rangers.Of.Pinewood_S03E16_Border Run.avi", 1100)
	write_file(t, dir, season+"6/}{serie}{ Rangers Of Pinewood 6x03 Too Many Chores (dvdrip by someone).avi", 1200)
	write_file(t, dir, season+"7/Rangers Of Pinewood 7x16 A Wedding (dvdrip by someone).avi", 1300)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 5 || result.Found != 5 {
		t.Fatalf("result = %+v, want the 5 videos found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != show {
		t.Fatalf("series entries = %+v, want one %q", series, show)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 5 {
		t.Fatalf("episodes = %+v, want all 5 episode files grouped", episodes)
	}
	if movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"}); len(movies) != 0 {
		t.Fatalf("movie entries = %+v, want none", movies)
	}
}

// Files numbered with the tracker "#SS_EE" spelling must be recognized as
// episodes rather than movies, so the show groups and is looked up as a series.
func Test_run_series_with_hash_numbered_episodes(t *testing.T) {
	dir := t.TempDir()
	const show = "Harbour Lights"
	mkdir_all(t, dir, "Series/"+show+"/S01")
	mkdir_all(t, dir, "Series/"+show+"/S02")
	write_file(t, dir, "Series/"+show+"/S01/#01_02_The First Case.mp4", 1000)
	write_file(t, dir, "Series/"+show+"/S01/#01_03_Old Tricks.mp4", 1100)
	write_file(t, dir, "Series/"+show+"/S02/#02_07_Not a Civil Servant.mp4", 1200)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 3 || result.Found != 3 {
		t.Fatalf("result = %+v, want the 3 files found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != show {
		t.Fatalf("series entries = %+v, want one %q", series, show)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 3 {
		t.Fatalf("episodes = %+v, want all 3 numbered episodes", episodes)
	}
	if movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"}); len(movies) != 0 {
		t.Fatalf("movie entries = %+v, want none", movies)
	}
}

// Files numbered by an absolute, zero-padded episode count inside a named
// collection folder must become episodes of that collection rather than
// standalone movies, so the collection is looked up as a series.
func Test_run_series_with_absolute_numbered_episodes(t *testing.T) {
	dir := t.TempDir()
	const collection = "Meadow Friends At The Cottage"
	base := "Series/Meadow Friends/" + collection
	mkdir_all(t, dir, base)
	write_file(t, dir, base+"/084 The Pond.mp4", 1000)
	write_file(t, dir, base+"/085 The Fence.mp4", 1100)
	write_file(t, dir, base+"/086 The Old Tree.mp4", 1200)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 3 || result.Found != 3 {
		t.Fatalf("result = %+v, want the 3 files found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != collection {
		t.Fatalf("series entries = %+v, want one %q", series, collection)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 3 {
		t.Fatalf("episodes = %+v, want all 3 numbered episodes", episodes)
	}
	if movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"}); len(movies) != 0 {
		t.Fatalf("movie entries = %+v, want none", movies)
	}
}

// A video with no episode marker of its own, sitting in a folder named after
// its episode ("47 Závan minulosti/želvy ninja 47.avi"), must become that
// episode of the enclosing show rather than its own movie entry.
func Test_run_series_with_numbered_episode_folders(t *testing.T) {
	dir := t.TempDir()
	const show = "Meadow Friends"
	base := "Series/" + show
	mkdir_all(t, dir, base+"/47 The Pond")
	mkdir_all(t, dir, base+"/48 The Fence")
	write_file(t, dir, base+"/47 The Pond/meadow friends 47.avi", 1000)
	write_file(t, dir, base+"/48 The Fence/meadow friends 48.avi", 1100)
	write_file(t, dir, base+"/48 The Fence/Thumbs.db", 10)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 2 || result.Found != 2 {
		t.Fatalf("result = %+v, want the 2 videos found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != show {
		t.Fatalf("series entries = %+v, want one %q", series, show)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 2 {
		t.Fatalf("episodes = %+v, want both folders' files grouped", episodes)
	}
	if movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"}); len(movies) != 0 {
		t.Fatalf("movie entries = %+v, want none", movies)
	}
}

// The British "Series N" season spelling and the "S01.E16" episode spelling
// must both be understood so the show groups under one entry.
func Test_run_series_with_series_season_folder(t *testing.T) {
	dir := t.TempDir()
	const show = "Meadow Friends"
	base := "Series/" + show
	mkdir_all(t, dir, base+"/"+show+".Series1")
	write_file(t, dir, base+"/"+show+".Series1/"+show+".S01.E16.mp4", 1000)
	write_file(t, dir, base+"/"+show+".Series1/"+show+".S01.E25.mp4", 1100)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.New != 2 || result.Found != 2 {
		t.Fatalf("result = %+v, want the 2 files found and new", result)
	}

	series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"})
	if len(series) != 1 || series[0].Title != show {
		t.Fatalf("series entries = %+v, want one %q", series, show)
	}
	episodes, _ := store.List_episodes(context.Background(), series[0].Id)
	if len(episodes) != 2 {
		t.Fatalf("episodes = %+v, want both episodes", episodes)
	}
	if movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"}); len(movies) != 0 {
		t.Fatalf("movie entries = %+v, want none", movies)
	}
}

// A movie in a released-year folder must stay a movie even though the folder
// starts with a number that could be read as an episode.
func Test_run_movie_in_dated_numbered_folder_stays_movie(t *testing.T) {
	dir := t.TempDir()
	mkdir_all(t, dir, "Movies/101 Dalmatians (1996)")
	write_file(t, dir, "Movies/101 Dalmatians (1996)/101.Dalmatians.1996.1080p.mkv", 1000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if series, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "series"}); len(series) != 0 {
		t.Fatalf("series entries = %+v, want none", series)
	}
	movies, _ := store.List_catalog_entries(context.Background(), database.Catalog_filter{Media_type: "movie"})
	if len(movies) != 1 {
		t.Fatalf("movie entries = %+v, want one", movies)
	}
}

// The frame poster only ever fills a blank: online artwork wins, an extraction
// failure just leaves the entry blank, and a frame that is already on disk is
// marked without extracting it a second time.
func Test_ensure_frame_poster_prefers_existing_art_and_never_fails_the_scan(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	dir := t.TempDir()
	video := write_file(t, dir, "Movie.2020.mkv", 1000)
	poster_dir := t.TempDir()

	s := new_test_scanner(t, store, dir)
	s.opts.Poster_dir = poster_dir
	var extracted int
	s.extract = func(_ context.Context, _ string, _ float64, out string) error {
		extracted++
		return os.WriteFile(out, []byte("fake-jpeg"), 0o644)
	}

	new_entry := func(title, poster string) int64 {
		t.Helper()
		id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
			Media_type: "movie", Title: title, Poster_path: poster,
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return id
	}
	poster_of := func(id int64) string {
		t.Helper()
		entry, err := store.Get_catalog_entry(ctx, id)
		if err != nil || entry == nil {
			t.Fatalf("get entry %d: %+v, %v", id, entry, err)
		}
		return entry.Poster_path
	}

	online := new_entry("Has Art", "https://images.example/has.jpg")
	s.ensure_frame_poster(ctx, online, video, 120)
	if extracted != 0 {
		t.Errorf("extracted %d frames, want none for an entry that already has artwork", extracted)
	}
	if got := poster_of(online); got != "https://images.example/has.jpg" {
		t.Errorf("poster = %q, want the online artwork untouched", got)
	}

	// ffmpeg missing, then ffmpeg failing: both leave the entry blank.
	blank := new_entry("No Art", "")
	s.extract = func(context.Context, string, float64, string) error {
		return fmt.Errorf("extract frame: %w", thumbnail.Err_no_ffmpeg)
	}
	s.ensure_frame_poster(ctx, blank, video, 120)
	if got := poster_of(blank); got != "" {
		t.Errorf("poster = %q, want blank without ffmpeg", got)
	}
	s.extract = func(context.Context, string, float64, string) error {
		return errors.New("ffmpeg gave up")
	}
	s.ensure_frame_poster(ctx, blank, video, 120)
	if got := poster_of(blank); got != "" {
		t.Errorf("poster = %q, want blank after a failed extraction", got)
	}

	// A frame that is already on disk is claimed, not re-extracted.
	seeded := new_entry("Seeded", "")
	seeded_file := thumbnail.Local_path(poster_dir, seeded)
	if err := os.MkdirAll(filepath.Dir(seeded_file), 0o750); err != nil {
		t.Fatalf("mkdir poster dir: %v", err)
	}
	if err := os.WriteFile(seeded_file, []byte("fake-jpeg"), 0o644); err != nil {
		t.Fatalf("seed frame poster: %v", err)
	}
	s.extract = func(context.Context, string, float64, string) error {
		extracted++
		return nil
	}
	s.ensure_frame_poster(ctx, seeded, video, 120)
	if extracted != 0 {
		t.Errorf("extracted %d frames, want the existing frame reused", extracted)
	}
	if got := poster_of(seeded); got != thumbnail.Local_marker {
		t.Errorf("poster = %q, want %q", got, thumbnail.Local_marker)
	}

	// And a successful extraction still claims its frame.
	s.extract = func(_ context.Context, _ string, _ float64, out string) error {
		extracted++
		return os.WriteFile(out, []byte("fake-jpeg"), 0o644)
	}
	fresh := new_entry("Fresh", "")
	s.ensure_frame_poster(ctx, fresh, video, 120)
	if extracted != 1 {
		t.Errorf("extracted %d frames, want 1", extracted)
	}
	if got := poster_of(fresh); got != thumbnail.Local_marker {
		t.Errorf("poster = %q, want %q", got, thumbnail.Local_marker)
	}
}

// Test_run_reports_done_progress_with_totals pins the tail of run: after the
// last file the caller gets one final "done" snapshot carrying the scan
// totals, and the same totals come back in the returned result.
func Test_run_reports_done_progress_with_totals(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "The.Matrix.1999.1080p.mkv", 1000)
	write_file(t, dir, "The.Matrix.1999.2160p.mkv", 2000)

	store := new_test_store(t)
	s := new_test_scanner(t, store, dir)

	var events []Progress
	result, err := s.Run_libraries(context.Background(), nil, func(p Progress) {
		events = append(events, p)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result == nil || result.Found != 2 || result.New != 2 {
		t.Fatalf("result = %+v, want 2 found and 2 new", result)
	}
	if len(events) == 0 {
		t.Fatal("expected progress events")
	}
	done := events[len(events)-1]
	if done.Phase != "done" {
		t.Fatalf("last phase = %q, want done", done.Phase)
	}
	if done.Files_found != result.Found || done.New_files != result.New || done.Skipped != result.Skipped || done.Errors != len(result.Errors) {
		t.Errorf("done event = %+v, want the totals of result %+v", done, result)
	}
}

// Test_run_appends_mark_missing_failure pins the failure tail of run: a store
// failure inside mark_missing is appended to the result errors instead of
// aborting the scan, and the "done" event still fires with the error counted.
func Test_run_appends_mark_missing_failure(t *testing.T) {
	dir := t.TempDir()
	write_file(t, dir, "Old.Movie.2001.mkv", 1000)

	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	if err := store.Ensure_library(context.Background(), "Media", dir, false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	s := New(store, Options{})
	s.probe = fake_probe
	s.hash = no_hash

	// The progress callback runs synchronously inside the scan, so closing the
	// database there deterministically breaks mark_missing's store query.
	closed := false
	var done Progress
	result, err := s.Run_libraries(context.Background(), nil, func(p Progress) {
		if p.Phase == "done" {
			done = p
			return
		}
		if !closed {
			closed = true
			_ = d.Close()
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	saw_failure := false
	for _, message := range result.Errors {
		if strings.HasPrefix(message, "mark missing: ") {
			saw_failure = true
		}
	}
	if !saw_failure {
		t.Fatalf("errors = %v, want a mark missing failure appended", result.Errors)
	}
	if done.Phase != "done" {
		t.Fatalf("done event = %+v, want the final progress event", done)
	}
	if done.Errors != len(result.Errors) {
		t.Errorf("done errors = %d, want %d (the totals must include the failure)", done.Errors, len(result.Errors))
	}
}
