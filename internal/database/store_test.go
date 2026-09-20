package database

import (
	"context"
	"reflect"
	"testing"
)

func new_test_store(t *testing.T) *Store {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New_store(d)
}

func Test_upsert_catalog_entry_groups_by_imdb_id(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	first, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Imdb_id: "tt0133093", Tmdb_id: 603, Status: "matched",
		Genres: []string{"Action", "Science Fiction"},
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Imdb_id: "tt0133093", Tmdb_id: 603, Status: "matched", Overview: "updated",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first != second {
		t.Fatalf("ids differ: %d vs %d", first, second)
	}

	entry, err := store.Get_catalog_entry(ctx, first)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.Overview != "updated" {
		t.Errorf("overview = %q, want updated", entry.Overview)
	}
	if !reflect.DeepEqual(entry.Genres, []string{"Action", "Science Fiction"}) {
		t.Errorf("genres = %v", entry.Genres)
	}
}

func Test_upsert_preserves_poster_when_empty(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	id, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Mystery", Status: "needs_lookup",
		Poster_path: "local://frame.jpg",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Mystery", Status: "needs_lookup",
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.Poster_path != "local://frame.jpg" {
		t.Errorf("poster_path = %q, want preserved", entry.Poster_path)
	}
}

func Test_upsert_catalog_entry_groups_by_title_year_without_ids(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	first, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Some Indie", Status: "needs_lookup",
	})
	second, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Some Indie", Status: "needs_lookup",
	})
	if first != second {
		t.Fatalf("unmatched entries did not group: %d vs %d", first, second)
	}
	series, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Some Indie", Status: "needs_lookup",
	})
	if series == first {
		t.Error("movie and series should not share an entry")
	}
}

func Test_upsert_episode(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	entry, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Imdb_id: "tt0903747", Status: "matched",
	})

	first, err := store.Upsert_episode(ctx, Episode{
		Catalog_entry_id: entry, Season_number: 1, Episode_number: 1, Status: "matched",
	})
	if err != nil {
		t.Fatalf("first episode: %v", err)
	}
	second, err := store.Upsert_episode(ctx, Episode{
		Catalog_entry_id: entry, Season_number: 1, Episode_number: 1,
		Title: "Pilot", Status: "matched",
	})
	if err != nil {
		t.Fatalf("second episode: %v", err)
	}
	if first != second {
		t.Fatalf("episode ids differ: %d vs %d", first, second)
	}
	episodes, err := store.List_episodes(ctx, entry)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Fatalf("episodes = %+v", episodes)
	}
}

func Test_save_version_replaces_tracks(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	entry, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{Media_type: "movie", Title: "M", Status: "matched"})
	if err := store.Upsert_library(ctx, "Media", "/media", true); err != nil {
		t.Fatalf("library: %v", err)
	}
	libraries, _ := store.List_libraries(ctx)
	if len(libraries) != 1 {
		t.Fatalf("libraries = %+v", libraries)
	}

	version_id, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, Library_id: libraries[0].Id, File_path: "m.mkv", Size_bytes: 100,
		Mtime: "2026-01-01T00:00:00Z", Resolution_label: "1080p",
		Audio:     []Audio_track{{Language: "eng", Codec: "aac", Channels: 2}},
		Subtitles: []Subtitle_track{{Language: "eng", Format: "subrip"}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, Library_id: libraries[0].Id, File_path: "m.mkv", Size_bytes: 100,
		Mtime: "2026-01-01T00:00:00Z", Resolution_label: "4K",
		Audio: []Audio_track{{Language: "fre", Codec: "dts", Channels: 6}},
	}); err != nil {
		t.Fatalf("resave: %v", err)
	}

	ref, found, err := store.Find_version_in_library(ctx, libraries[0].Id, "m.mkv")
	if err != nil || !found {
		t.Fatalf("find: %v found=%v", err, found)
	}
	if ref.Id != version_id {
		t.Errorf("version id changed on resave: %d vs %d", ref.Id, version_id)
	}
	versions, _ := store.List_versions_for_entry(ctx, entry)
	if len(versions) != 1 || versions[0].Resolution_label != "4K" {
		t.Fatalf("versions = %+v", versions)
	}
	audio, subtitles, err := store.Version_tracks(ctx, version_id)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if len(audio) != 1 || audio[0].Language != "fre" {
		t.Errorf("audio = %+v", audio)
	}
	if len(subtitles) != 0 {
		t.Errorf("subtitles = %+v, want replaced (empty)", subtitles)
	}
}

func Test_list_catalog_entries_filters(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	matrix, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999, Status: "matched",
		Rating: 8.2, Genres: []string{"Action"},
	})
	store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Release_year: 2008, Status: "matched",
		Genres: []string{"Drama"},
	})
	store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Mystery Film", Release_year: 2020, Status: "needs_lookup",
	})
	store.Save_version(ctx, Version{Catalog_entry_id: matrix, File_path: "/m/matrix.mkv", Resolution_label: "1080p",
		Audio: []Audio_track{{Language: "eng"}}})

	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Media_type: "movie"}); len(got) != 2 {
		t.Errorf("movie filter = %d, want 2", len(got))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Status: "needs_lookup"}); len(got) != 1 {
		t.Errorf("status filter = %d, want 1", len(got))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Search: "matrix"}); len(got) != 1 {
		t.Errorf("search filter = %d, want 1", len(got))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Genre: "Drama"}); len(got) != 1 {
		t.Errorf("genre filter = %d, want 1", len(got))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Resolution: "1080p"}); len(got) != 1 {
		t.Errorf("resolution filter = %d, want 1", len(got))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Language: "eng"}); len(got) != 1 {
		t.Errorf("language filter = %d, want 1", len(got))
	}
	year_sorted, _ := store.List_catalog_entries(ctx, Catalog_filter{Sort: "year", Desc: true})
	if len(year_sorted) != 3 || year_sorted[0].Release_year != 2020 {
		t.Errorf("year sort = %+v", year_sorted)
	}
}

func Test_genre_counts(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Status: "matched",
		Genres: []string{"Action"},
	})
	store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Alien", Status: "matched",
		Genres: []string{"Action", "Science Fiction"},
	})
	store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Unmatched Drama", Status: "needs_lookup",
		Genres: []string{"Drama"},
	})

	matched, err := store.Genre_counts(ctx, "matched")
	if err != nil {
		t.Fatalf("genre counts: %v", err)
	}
	if len(matched) != 2 || matched[0].Name != "Action" || matched[0].Count != 2 ||
		matched[1].Name != "Science Fiction" || matched[1].Count != 1 {
		t.Errorf("matched genre counts = %+v", matched)
	}

	unmatched, err := store.Genre_counts(ctx, "needs_lookup")
	if err != nil {
		t.Fatalf("genre counts: %v", err)
	}
	if len(unmatched) != 1 || unmatched[0].Name != "Drama" || unmatched[0].Count != 1 {
		t.Errorf("needs_lookup genre counts = %+v", unmatched)
	}
}

func Test_cache_roundtrip(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if _, found, _ := store.Cache_get(ctx, "hash", "abc"); found {
		t.Error("unexpected cache hit")
	}
	if err := store.Cache_put(ctx, "hash", "abc", `{"imdb":"tt0133093"}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	payload, found, err := store.Cache_get(ctx, "hash", "abc")
	if err != nil || !found || payload != `{"imdb":"tt0133093"}` {
		t.Fatalf("get = %q found=%v err=%v", payload, found, err)
	}
}

func Test_config_store(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if _, found, _ := store.Config_get(ctx, "key"); found {
		t.Error("unexpected config hit")
	}
	if err := store.Config_set(ctx, "key", "value"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.Config_set(ctx, "key", "value2"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	value, found, _ := store.Config_get(ctx, "key")
	if !found || value != "value2" {
		t.Fatalf("get = %q found=%v", value, found)
	}
	all, _ := store.Config_all(ctx)
	if all["key"] != "value2" {
		t.Errorf("all = %v", all)
	}
}

func Test_libraries(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Upsert_library(ctx, "Movies", "/media/movies", true); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert_library(ctx, "Shows", "/media/shows", false); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	enabled, _ := store.Enabled_libraries(ctx)
	if len(enabled) != 1 || enabled[0].Name != "Movies" {
		t.Fatalf("enabled = %+v", enabled)
	}
	if err := store.Touch_library(ctx, "Movies", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	all, _ := store.List_libraries(ctx)
	if len(all) != 2 || all[0].Name != "Movies" || all[1].Name != "Shows" {
		t.Fatalf("all = %+v", all)
	}
	if all[0].Last_scan == "" {
		t.Errorf("last_scan not recorded: %+v", all[0])
	}
	if all[1].Enabled {
		t.Errorf("Shows should stay disabled: %+v", all[1])
	}
}

func Test_ensure_library_renames_for_path(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Ensure_library(ctx, "movies", "/media/movies", true); err != nil {
		t.Fatalf("ensure first: %v", err)
	}
	enabled_before := func() Library {
		list, _ := store.List_libraries(ctx)
		return list[0]
	}
	before := enabled_before()
	if !before.Enabled {
		t.Fatalf("library not enabled: %+v", before)
	}
	if err := store.Ensure_library(ctx, "Movies", "/media/movies", false); err != nil {
		t.Fatalf("ensure rename: %v", err)
	}
	list, _ := store.List_libraries(ctx)
	if len(list) != 1 || list[0].Name != "Movies" {
		t.Fatalf("libraries after rename = %+v", list)
	}
	if !list[0].Enabled {
		t.Errorf("rename must preserve enabled state: %+v", list[0])
	}
}

func Test_normalize_library_versions(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Ensure_library(ctx, "Media", "/media", true); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	libraries, _ := store.List_libraries(ctx)
	lib := libraries[0]

	entry, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{Media_type: "movie", Title: "M", Status: "matched"})
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, File_path: "/media/sub/movie.mkv", Size_bytes: 100,
	}); err != nil {
		t.Fatalf("legacy save: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, File_path: "/elsewhere/other.mkv", Size_bytes: 200,
	}); err != nil {
		t.Fatalf("outside save: %v", err)
	}

	rewritten, err := store.Normalize_library_versions(ctx, lib)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if rewritten != 1 {
		t.Fatalf("rewritten = %d, want 1", rewritten)
	}

	if ref, found, err := store.Find_version_in_library(ctx, lib.Id, "sub/movie.mkv"); err != nil || !found {
		t.Fatalf("normalized path not found: %v found=%v", err, found)
	} else if ref.Id != 0 && ref.File_path != "sub/movie.mkv" {
		t.Fatalf("ref = %+v", ref)
	}
	if ref, found, _ := store.Find_version_in_library(ctx, 0, "/media/sub/movie.mkv"); found {
		t.Fatalf("legacy absolute row still present: %+v", ref)
	}
	// The row outside the library keeps its absolute path.
	outside, found, err := store.Find_version_in_library(ctx, 0, "/elsewhere/other.mkv")
	if err != nil || !found {
		t.Fatalf("outside row lost: %v found=%v", err, found)
	}
	if outside.File_path != "/elsewhere/other.mkv" {
		t.Errorf("outside path = %q", outside.File_path)
	}
}

func Test_candidates_roundtrip_and_batch(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	id, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Ambiguous Title", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	candidates := []Candidate{
		{Tmdb_id: 10, Imdb_id: "tt0000001", Title: "First", Year: 2000, Media_type: "movie", Score: 0.7, Overview: "o1", Poster_path: "/p1.jpg"},
		{Imdb_id: "tt0000002", Title: "Second", Year: 2001, Media_type: "movie", Score: 0.5, Overview: "o2"},
	}
	if err := store.Replace_candidates(ctx, id, candidates); err != nil {
		t.Fatalf("replace: %v", err)
	}

	listed, err := store.List_candidates(ctx, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].Title != "First" {
		t.Fatalf("listed = %+v", listed)
	}

	batch, err := store.List_candidates_for_entries(ctx, []int64{id})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch[id]) != 2 || batch[id][0].Title != "First" || batch[id][1].Title != "Second" {
		t.Fatalf("batch = %+v", batch)
	}

	if err := store.Replace_candidates(ctx, id, []Candidate{{Tmdb_id: 30, Title: "Third", Media_type: "movie"}}); err != nil {
		t.Fatalf("replace again: %v", err)
	}
	after, _ := store.List_candidates(ctx, id)
	if len(after) != 1 || after[0].Tmdb_id != 30 {
		t.Fatalf("after replace = %+v", after)
	}

	if err := store.Clear_candidates(ctx, id); err != nil {
		t.Fatalf("clear: %v", err)
	}
	gone, _ := store.List_candidates(ctx, id)
	if len(gone) != 0 {
		t.Fatalf("after clear = %+v", gone)
	}
}

func Test_update_catalog_entry_to_matched_clears_candidates(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	id, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Ambiguous", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Replace_candidates(ctx, id, []Candidate{{Tmdb_id: 10, Title: "First", Media_type: "movie"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if err := store.Update_catalog_entry(ctx, id, Catalog_entry{
		Media_type: "movie", Title: "Ambiguous", Tmdb_id: 10, Status: "matched",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	left, err := store.List_candidates(ctx, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("candidates survived a matched update: %+v", left)
	}
}
