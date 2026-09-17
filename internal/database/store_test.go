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

	version_id, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, File_path: "/media/m.mkv", Size_bytes: 100,
		Mtime: "2026-01-01T00:00:00Z", Resolution_label: "1080p",
		Audio:     []Audio_track{{Language: "eng", Codec: "aac", Channels: 2}},
		Subtitles: []Subtitle_track{{Language: "eng", Format: "subrip"}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, File_path: "/media/m.mkv", Size_bytes: 100,
		Mtime: "2026-01-01T00:00:00Z", Resolution_label: "4K",
		Audio: []Audio_track{{Language: "fre", Codec: "dts", Channels: 6}},
	}); err != nil {
		t.Fatalf("resave: %v", err)
	}

	ref, found, err := store.Find_version_by_path(ctx, "/media/m.mkv")
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

func Test_watch_folders(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Set_watch_folder(ctx, "/media/movies", true); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.Set_watch_folder(ctx, "/media/shows", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	enabled, _ := store.Enabled_watch_folders(ctx)
	if len(enabled) != 1 || enabled[0].Path != "/media/movies" {
		t.Fatalf("enabled = %+v", enabled)
	}
	if err := store.Touch_watch_folder(ctx, "/media/movies", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	all, _ := store.List_watch_folders(ctx)
	if len(all) != 2 {
		t.Fatalf("all = %+v", all)
	}
	if all[0].Last_scan == "" {
		t.Errorf("last_scan not recorded: %+v", all[0])
	}
}
