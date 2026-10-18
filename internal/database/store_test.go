package database

import (
	"context"
	"reflect"
	"sort"
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

// An episode's status is what keeps it on the unmatched list, so setting it has
// to be readable back through the same query the list uses.
func Test_set_episode_status(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	entry, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Imdb_id: "tt0903747", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	id, err := store.Upsert_episode(ctx, Episode{
		Catalog_entry_id: entry, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if err := store.Set_episode_status(ctx, id, "matched"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	episode, err := store.Get_episode(ctx, id)
	if err != nil || episode == nil {
		t.Fatalf("get episode: %+v, %v", episode, err)
	}
	if episode.Status != "matched" {
		t.Errorf("status = %q, want matched", episode.Status)
	}
	// An unknown episode is not an error: there is simply nothing to update.
	if err := store.Set_episode_status(ctx, 9999, "matched"); err != nil {
		t.Errorf("unknown episode = %v, want no error", err)
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
	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Release_year: 2008, Status: "matched",
		Genres: []string{"Drama"},
	}); err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Mystery Film", Release_year: 2020, Status: "needs_lookup",
	}); err != nil {
		t.Fatalf("upsert mystery film: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{Catalog_entry_id: matrix, File_path: "/m/matrix.mkv", Resolution_label: "1080p",
		Audio: []Audio_track{{Language: "eng"}}}); err != nil {
		t.Fatalf("save version: %v", err)
	}

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

func Test_list_catalog_entries_filtered_by_library(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Upsert_library(ctx, "Movies", "/media/movies", true); err != nil {
		t.Fatalf("upsert movies: %v", err)
	}
	if err := store.Upsert_library(ctx, "Shows", "/media/shows", true); err != nil {
		t.Fatalf("upsert shows: %v", err)
	}
	all, _ := store.List_libraries(ctx)
	var lib_id = func(name string) int64 {
		for _, l := range all {
			if l.Name == name {
				return l.Id
			}
		}
		return 0
	}

	movie, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Status: "needs_lookup",
	})
	series, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Status: "needs_lookup",
	})
	if _, err := store.Save_version(ctx, Version{Catalog_entry_id: movie, Library_id: lib_id("Movies"), File_path: "matrix.mkv"}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{Catalog_entry_id: series, Library_id: lib_id("Shows"), File_path: "bb.s01e01.mkv"}); err != nil {
		t.Fatalf("save series version: %v", err)
	}

	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Libraries: []string{"Movies"}}); len(got) != 1 || got[0].Title != "The Matrix" {
		t.Errorf("single library filter = %+v, want only The Matrix", got)
	}
	both, _ := store.List_catalog_entries(ctx, Catalog_filter{Libraries: []string{"Movies", "Shows"}})
	if len(both) != 2 {
		t.Errorf("multi library filter = %d, want 2", len(both))
	}
	if got, _ := store.List_catalog_entries(ctx, Catalog_filter{Libraries: []string{"Nope"}}); len(got) != 0 {
		t.Errorf("unknown library filter = %d, want 0", len(got))
	}
}

// seed_clause_catalog fills a fresh store with the fixture the catalog WHERE
// clause tests query against: two libraries and three entries, each with a
// single version. The Matrix is a matched 1999 Action/Sci-Fi movie with a
// poster in Movies at 1080p with an eng track; Blade Runner is a matched 1982
// Drama/Sci-Fi movie without a poster in Movies at 4K with a fre track;
// Breaking Bad is an unmatched 2008 Crime/Drama series with a poster in Shows
// at 1080p with an eng track.
func seed_clause_catalog(t *testing.T) *Store {
	t.Helper()
	store := new_test_store(t)
	ctx := context.Background()

	if err := store.Upsert_library(ctx, "Movies", "/media/movies", true); err != nil {
		t.Fatalf("upsert movies: %v", err)
	}
	if err := store.Upsert_library(ctx, "Shows", "/media/shows", true); err != nil {
		t.Fatalf("upsert shows: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil || len(libraries) != 2 {
		t.Fatalf("libraries = %+v, %v", libraries, err)
	}
	lib_id := func(name string) int64 {
		for _, library := range libraries {
			if library.Name == name {
				return library.Id
			}
		}
		return 0
	}

	matrix, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Status: "matched", Poster_path: "/p/matrix.jpg",
		Genres: []string{"Action", "Sci-Fi"},
	})
	if err != nil {
		t.Fatalf("upsert matrix: %v", err)
	}
	runner, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Blade Runner", Release_year: 1982,
		Status: "matched", Genres: []string{"Drama", "Sci-Fi"},
	})
	if err != nil {
		t.Fatalf("upsert runner: %v", err)
	}
	bad, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Release_year: 2008,
		Status: "needs_lookup", Poster_path: "/p/bb.jpg",
		Genres: []string{"Crime", "Drama"},
	})
	if err != nil {
		t.Fatalf("upsert breaking bad: %v", err)
	}

	versions := []struct {
		entry      int64
		library    string
		file       string
		resolution string
		language   string
	}{
		{matrix, "Movies", "matrix.mkv", "1080p", "eng"},
		{runner, "Movies", "blade-runner.mkv", "4K", "fre"},
		{bad, "Shows", "bb.s01e01.mkv", "1080p", "eng"},
	}
	for _, version := range versions {
		if _, err := store.Save_version(ctx, Version{
			Catalog_entry_id: version.entry, Library_id: lib_id(version.library),
			File_path: version.file, Size_bytes: 1, Mtime: "2026-01-01T00:00:00Z",
			Resolution_label: version.resolution,
			Audio:            []Audio_track{{Language: version.language}},
		}); err != nil {
			t.Fatalf("save version %s: %v", version.file, err)
		}
	}
	return store
}

// list_titles lists the catalog matching filter and returns the sorted titles
// so the filter tests can compare whole result sets regardless of row order.
func list_titles(t *testing.T, store *Store, ctx context.Context, filter Catalog_filter) []string {
	t.Helper()
	entries, err := store.List_catalog_entries(ctx, filter)
	if err != nil {
		t.Fatalf("list catalog entries: %v", err)
	}
	titles := make([]string, len(entries))
	for i, entry := range entries {
		titles[i] = entry.Title
	}
	sort.Strings(titles)
	return titles
}

// The plural status filter binds one IN-list placeholder per value, so it has
// to accept any list length and match every status it names.
func Test_list_catalog_entries_statuses_filter(t *testing.T) {
	store := seed_clause_catalog(t)
	ctx := context.Background()

	if got := list_titles(t, store, ctx, Catalog_filter{}); len(got) != 3 {
		t.Fatalf("unfiltered = %v, want 3 entries", got)
	}
	both := list_titles(t, store, ctx,
		Catalog_filter{Statuses: []string{"matched", "needs_lookup"}})
	if want := []string{"Blade Runner", "Breaking Bad", "The Matrix"}; !reflect.DeepEqual(both, want) {
		t.Errorf("statuses [matched needs_lookup] = %v, want %v", both, want)
	}
	matched := list_titles(t, store, ctx,
		Catalog_filter{Statuses: []string{"matched"}})
	if want := []string{"Blade Runner", "The Matrix"}; !reflect.DeepEqual(matched, want) {
		t.Errorf("statuses [matched] = %v, want %v", matched, want)
	}
	if got := list_titles(t, store, ctx,
		Catalog_filter{Statuses: []string{"needs_lookup"}}); !reflect.DeepEqual(got, []string{"Breaking Bad"}) {
		t.Errorf("statuses [needs_lookup] = %v, want Breaking Bad", got)
	}
	if got := list_titles(t, store, ctx,
		Catalog_filter{Statuses: []string{"nope"}}); len(got) != 0 {
		t.Errorf("unknown status = %v, want nothing", got)
	}
	// An empty status list must not filter at all: the clause builder guards
	// zero-length lists before it ever reaches the placeholder join.
	if got := list_titles(t, store, ctx,
		Catalog_filter{Statuses: nil}); len(got) != 3 {
		t.Errorf("empty status list = %v, want all 3 entries", got)
	}
}

// Each remaining filter dimension adds its own clause, and each has to both
// match on its own and exclude what does not match.
func Test_list_catalog_entries_year_genres_poster_library_filters(t *testing.T) {
	store := seed_clause_catalog(t)
	ctx := context.Background()

	if got := list_titles(t, store, ctx, Catalog_filter{Year: 1999}); !reflect.DeepEqual(got, []string{"The Matrix"}) {
		t.Errorf("year 1999 = %v, want The Matrix", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{Year: 2000}); len(got) != 0 {
		t.Errorf("year 2000 = %v, want nothing", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{Year: 0}); len(got) != 3 {
		t.Errorf("no year = %v, want all 3 entries", got)
	}

	if got := list_titles(t, store, ctx, Catalog_filter{Genres: []string{"Sci-Fi"}}); len(got) != 2 {
		t.Errorf("genres [Sci-Fi] = %v, want the two Sci-Fi movies", got)
	}
	// Genres intersect: Blade Runner is the only entry carrying both.
	if got := list_titles(t, store, ctx,
		Catalog_filter{Genres: []string{"Sci-Fi", "Drama"}}); !reflect.DeepEqual(got, []string{"Blade Runner"}) {
		t.Errorf("genres [Sci-Fi Drama] = %v, want Blade Runner", got)
	}
	// An empty genre name is skipped rather than matching nothing.
	if got := list_titles(t, store, ctx,
		Catalog_filter{Genres: []string{"", "Drama"}}); len(got) != 2 {
		t.Errorf("genres [<empty> Drama] = %v, want the two Drama entries", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{Genres: []string{"Nope"}}); len(got) != 0 {
		t.Errorf("genres [Nope] = %v, want nothing", got)
	}

	if got := list_titles(t, store, ctx, Catalog_filter{Has_poster: true}); len(got) != 2 {
		t.Errorf("has_poster = %v, want the two entries with posters", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{}); len(got) != 3 {
		t.Errorf("no poster filter = %v, want all 3 entries", got)
	}

	if got := list_titles(t, store, ctx, Catalog_filter{Library: "Movies"}); len(got) != 2 {
		t.Errorf("library Movies = %v, want the two movies", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{Library: "Shows"}); !reflect.DeepEqual(got, []string{"Breaking Bad"}) {
		t.Errorf("library Shows = %v, want Breaking Bad", got)
	}
	if got := list_titles(t, store, ctx, Catalog_filter{Library: "Nope"}); len(got) != 0 {
		t.Errorf("unknown library = %v, want nothing", got)
	}
	if got := list_titles(t, store, ctx,
		Catalog_filter{Libraries: []string{"Movies", "Shows"}}); len(got) != 3 {
		t.Errorf("libraries [Movies Shows] = %v, want all 3 entries", got)
	}
	if got := list_titles(t, store, ctx,
		Catalog_filter{Libraries: nil}); len(got) != 3 {
		t.Errorf("empty libraries = %v, want all 3 entries", got)
	}
}

// Stacking the filters builds one WHERE clause per dimension, so the combined
// query has to intersect them rather than let any single clause widen it.
func Test_list_catalog_entries_combined_clause_filters(t *testing.T) {
	store := seed_clause_catalog(t)
	ctx := context.Background()

	both := list_titles(t, store, ctx, Catalog_filter{
		Statuses: []string{"matched", "needs_lookup"}, Resolution: "1080p",
		Language: "eng", Libraries: []string{"Movies", "Shows"},
	})
	if want := []string{"Breaking Bad", "The Matrix"}; !reflect.DeepEqual(both, want) {
		t.Errorf("combined 1080p/eng = %v, want %v", both, want)
	}
	narrowed := list_titles(t, store, ctx, Catalog_filter{
		Statuses: []string{"matched", "needs_lookup"}, Resolution: "1080p",
		Language: "eng", Libraries: []string{"Shows"},
	})
	if !reflect.DeepEqual(narrowed, []string{"Breaking Bad"}) {
		t.Errorf("combined narrowed to Shows = %v, want Breaking Bad", narrowed)
	}
	// Blade Runner is 4K with a fre track, so a 4K/eng combination matches
	// nothing at all.
	if got := list_titles(t, store, ctx,
		Catalog_filter{Resolution: "4K", Language: "eng"}); len(got) != 0 {
		t.Errorf("4K/eng = %v, want nothing", got)
	}
}

func Test_genre_counts(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()

	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Status: "matched",
		Genres: []string{"Action"},
	}); err != nil {
		t.Fatalf("upsert matrix: %v", err)
	}
	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Alien", Status: "matched",
		Genres: []string{"Action", "Science Fiction"},
	}); err != nil {
		t.Fatalf("upsert alien: %v", err)
	}
	if _, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Unmatched Drama", Status: "needs_lookup",
		Genres: []string{"Drama"},
	}); err != nil {
		t.Fatalf("upsert unmatched drama: %v", err)
	}

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

// Subtitle tracks are read back for the UI, so a track whose language or format
// is unknown still has to come back as a row rather than a hole.
func Test_version_tracks_lists_subtitles_with_unknown_fields(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	entry, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "M", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	if err := store.Upsert_library(ctx, "Media", "/media", true); err != nil {
		t.Fatalf("library: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil || len(libraries) != 1 {
		t.Fatalf("libraries = %+v, %v", libraries, err)
	}
	id, err := store.Save_version(ctx, Version{
		Catalog_entry_id: entry, Library_id: libraries[0].Id, File_path: "m.mkv", Size_bytes: 100,
		Mtime: "2026-01-01T00:00:00Z",
		Subtitles: []Subtitle_track{
			{Language: "eng", Format: "subrip", Source: "ffprobe"},
			{},
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	_, subtitles, err := store.Version_tracks(ctx, id)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if len(subtitles) != 2 {
		t.Fatalf("subtitles = %+v, want both tracks", subtitles)
	}
	if subtitles[0].Language != "eng" || subtitles[0].Format != "subrip" {
		t.Errorf("subtitles[0] = %+v", subtitles[0])
	}
	if subtitles[1].Language != "" || subtitles[1].Format != "" {
		t.Errorf("subtitles[1] = %+v, want empty strings for unknown fields", subtitles[1])
	}
	if subtitles[1].Source != "ffprobe" {
		t.Errorf("subtitles[1].Source = %q, want the ffprobe default", subtitles[1].Source)
	}

	// A closed database is an error, not an empty track list.
	if err := store.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := store.Version_tracks(ctx, id); err == nil {
		t.Error("reading tracks from a closed database should fail")
	}
}

func Test_reset_catalogs_scopes_to_named_libraries(t *testing.T) {
	ctx := context.Background()
	store := new_test_store(t)

	for _, name := range []string{"A", "B"} {
		if err := store.Upsert_library(ctx, name, "/media/"+name, false); err != nil {
			t.Fatalf("upsert library %s: %v", name, err)
		}
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	ids := map[string]int64{}
	for _, library := range libraries {
		ids[library.Name] = library.Id
	}

	only_a, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Only A", Status: "needs_lookup"})
	shared, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Shared", Status: "needs_lookup"})
	only_b, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "movie", Title: "Only B", Status: "needs_lookup"})

	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: only_a, Library_id: ids["A"], File_path: "Only.A.mkv", Size_bytes: 1}); err != nil {
		t.Fatalf("save only-a: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: shared, Library_id: ids["A"], File_path: "Shared.A.mkv", Size_bytes: 1}); err != nil {
		t.Fatalf("save shared-a: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: shared, Library_id: ids["B"], File_path: "Shared.B.mkv", Size_bytes: 1}); err != nil {
		t.Fatalf("save shared-b: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Catalog_entry_id: only_b, Library_id: ids["B"], File_path: "Only.B.mkv", Size_bytes: 1}); err != nil {
		t.Fatalf("save only-b: %v", err)
	}

	// A series episode backed only by library A must go with it.
	series, _ := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Only A Series", Status: "needs_lookup"})
	episode, err := store.Upsert_episode(ctx, Episode{
		Catalog_entry_id: series, Season_number: 1, Episode_number: 1, Status: "needs_lookup"})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, Version{
		Episode_id: episode, Library_id: ids["A"], File_path: "Only.A.S01E01.mkv", Size_bytes: 1}); err != nil {
		t.Fatalf("save episode version: %v", err)
	}

	// An empty list is a no-op.
	if err := store.Reset_catalogs(ctx, nil); err != nil {
		t.Fatalf("reset empty: %v", err)
	}
	if entry, _ := store.Get_catalog_entry(ctx, only_a); entry == nil {
		t.Fatal("empty reset dropped an entry")
	}

	if err := store.Reset_catalogs(ctx, []int64{ids["A"]}); err != nil {
		t.Fatalf("reset A: %v", err)
	}

	if entry, _ := store.Get_catalog_entry(ctx, only_a); entry != nil {
		t.Errorf("entry backed only by A survived: %+v", entry)
	}
	if entry, _ := store.Get_catalog_entry(ctx, series); entry != nil {
		t.Errorf("series backed only by an A episode survived: %+v", entry)
	}
	if entry, _ := store.Get_catalog_entry(ctx, only_b); entry == nil {
		t.Error("entry backed only by B was dropped")
	}
	if entry, _ := store.Get_catalog_entry(ctx, shared); entry == nil {
		t.Error("entry shared with B was dropped")
	}
	versions, err := store.List_versions_for_entry(ctx, shared)
	if err != nil {
		t.Fatalf("list shared versions: %v", err)
	}
	if len(versions) != 1 || versions[0].File_path != "Shared.B.mkv" {
		t.Errorf("shared versions = %+v, want only the B file", versions)
	}
}

func Test_reset_entry_match_clears_matched_metadata(t *testing.T) {
	ctx := context.Background()
	store := new_test_store(t)

	id, err := store.Upsert_catalog_entry(ctx, Catalog_entry{
		Media_type: "series", Title: "Old Match", Original_title: "Old Original",
		Release_year: 2008, Overview: "an overview", Runtime_minutes: 49,
		Rating: 9.5, Vote_count: 12000, Imdb_id: "tt0903747", Tmdb_id: 1396,
		Poster_path: "/poster.jpg", Status: "matched", Genres: []string{"Drama"},
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, Episode{
		Catalog_entry_id: id, Season_number: 1, Episode_number: 1,
		Title: "Pilot", Overview: "pilot overview", Airdate: "2008", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if err := store.Upsert_series_metadata(ctx, Series_metadata{
		Catalog_entry_id: id, First_air_date: "2008-01-20", Num_episodes: 62,
	}); err != nil {
		t.Fatalf("upsert series metadata: %v", err)
	}
	if err := store.Replace_candidates(ctx, id, []Candidate{
		{Tmdb_id: 1396, Title: "Breaking Bad", Year: 2008, Media_type: "series", Score: 0.4},
	}); err != nil {
		t.Fatalf("replace candidates: %v", err)
	}

	if err := store.Reset_entry_match(ctx, id, "Breaking Bad", 0); err != nil {
		t.Fatalf("reset entry match: %v", err)
	}

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "needs_lookup" || entry.Title != "Breaking Bad" || entry.Release_year != 0 {
		t.Errorf("entry = %+v, want needs_lookup/raw title raw year", entry)
	}
	if entry.Original_title != "" || entry.Overview != "" || entry.Runtime_minutes != 0 ||
		entry.Rating != 0 || entry.Vote_count != 0 || entry.Imdb_id != "" ||
		entry.Tmdb_id != 0 || entry.Poster_path != "" {
		t.Errorf("matched metadata survived reset: %+v", entry)
	}
	if len(entry.Genres) != 0 {
		t.Errorf("genres survived reset: %+v", entry.Genres)
	}
	if meta, _ := store.Get_series_metadata(ctx, id); meta != nil {
		t.Errorf("series metadata survived reset: %+v", meta)
	}
	if candidates, _ := store.List_candidates(ctx, id); len(candidates) != 0 {
		t.Errorf("candidates survived reset: %+v", candidates)
	}
	episodes, err := store.List_episodes(ctx, id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("episodes = %d, want the row kept", len(episodes))
	}
	if episodes[0].Id != episode_id || episodes[0].Status != "needs_lookup" ||
		episodes[0].Title != "" || episodes[0].Overview != "" || episodes[0].Airdate != "" {
		t.Errorf("episode = %+v, want cleared match state", episodes[0])
	}
	// Versions survive: only the match is dropped, the files stay catalogued.
	if versions, _ := store.List_versions_for_entry(ctx, id); len(versions) != 0 {
		t.Errorf("unexpected version rows: %+v", versions)
	}
}
