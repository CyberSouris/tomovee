package matching_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/scanner"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

type fake_movie_metadata struct{}

func (fake_movie_metadata) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	return []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}}, nil
}

func (fake_movie_metadata) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	return nil, nil
}

func (fake_movie_metadata) Movie_details(context.Context, int) (*tmdb.Movie_details, error) {
	return &tmdb.Movie_details{
		Id: 603, Imdb_id: "tt0133093", Title: "The Matrix", Release_date: "1999-03-31",
	}, nil
}

func (fake_movie_metadata) Tv_details(context.Context, int) (*tmdb.Tv_details, error) {
	return &tmdb.Tv_details{}, nil
}

func (fake_movie_metadata) Find_by_imdb(context.Context, string) (*tmdb.Find_result, error) {
	return &tmdb.Find_result{}, nil
}

// fake_offline_hit always answers with one confident movie hit.
type fake_offline_hit struct{}

func (fake_offline_hit) Search(context.Context, string, int, scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return []matcher.Offline_candidate{
		{Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie},
	}, nil
}

// fake_offline_miss answers with a mismatched title so matching stays ambiguous.
type fake_offline_miss struct{}

func (fake_offline_miss) Search(context.Context, string, int, scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return []matcher.Offline_candidate{
		{Imdb_id: "tt9999999", Title: "Completely Different", Year: 1999, Media_type: scanner.Movie},
	}, nil
}

func new_movie_service(t *testing.T, offline matcher.Offline_source) (*matching.Matching, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_movie_metadata{}, Offline: offline, Logger: logger})
	return matching.New(store, m, nil, logger), store
}

func new_offline_service(t *testing.T, offline matcher.Offline_source) (*matching.Matching, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Offline: offline, Logger: logger})
	return matching.New(store, m, nil, logger), store
}

func add_movie(t *testing.T, store *database.Store, title string, guard func(database.Version) database.Version) int64 {
	t.Helper()
	ctx := context.Background()
	version := database.Version{
		File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1234, Hash: "hash-matrix",
	}
	if guard != nil {
		version = guard(version)
	}
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: title, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	version.Catalog_entry_id = entry_id
	if _, err := store.Save_version(ctx, version); err != nil {
		t.Fatalf("save version: %v", err)
	}
	return entry_id
}

func Test_new_uses_default_logger_when_nil(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	m := matcher.New(matcher.Options{})
	service := matching.New(store, m, nil, nil)
	if service == nil {
		t.Fatal("New returned nil")
	}
}

func Test_run_reports_progress(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	add_series(t, store)

	var events []matching.Progress
	result, err := service.Run(ctx, nil, func(p matching.Progress) { events = append(events, p) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Total != 1 || result.Matched != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(events) == 0 {
		t.Fatal("no progress events reported")
	}
	last := events[len(events)-1]
	if last.Done != 1 || last.Total != 1 {
		t.Errorf("last progress = %+v, want Done 1/1", last)
	}
	done := -1
	for _, p := range events {
		if p.Done <= done {
			t.Errorf("progress Done = %d after %d (not increasing)", p.Done, done)
		}
		done = p.Done
	}
}

func Test_run_cancelled_errors(t *testing.T) {
	service, store := new_test_service(t)
	add_series(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Run(ctx, nil, nil); err == nil {
		t.Error("Run on a cancelled context succeeded")
	}
}

func Test_enrich_episodes_ignores_movies(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, ep1_id, _ := add_series(t, store)

	if err := service.Enrich_episodes(ctx, entry_id, &matcher.Result{Media_type: scanner.Movie}); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if meta, err := store.Get_series_metadata(ctx, entry_id); err != nil || meta != nil {
		t.Errorf("series metadata stored for a movie = %+v, %v", meta, err)
	}
	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 2 || episodes[0].Id == ep1_id && episodes[0].Status == "matched" {
		t.Errorf("episode 1 unexpectedly enriched: %+v", episodes[0])
	}
}

func Test_enrich_episodes_fills_titles(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)

	result := &matcher.Result{
		Media_type: scanner.Series, Imdb_id: "tt0903747", Title: "Breaking Bad",
		First_air_date: "2008-01-20", Last_air_date: "2013-09-29",
		Number_of_seasons: 5, Number_of_episodes: 62,
	}
	if err := service.Enrich_episodes(ctx, entry_id, result); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	meta, err := store.Get_series_metadata(ctx, entry_id)
	if err != nil || meta == nil {
		t.Fatalf("series metadata: %v, %v", meta, err)
	}
	if meta.Num_episodes != 62 {
		t.Errorf("metadata = %+v", meta)
	}
	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	by_number := map[int]database.Episode{}
	for _, episode := range episodes {
		by_number[episode.Episode_number] = episode
	}
	if ep1 := by_number[1]; ep1.Status != "matched" || ep1.Title != "Pilot" || ep1.Airdate != "2008" {
		t.Errorf("episode 1 = %+v, want matched pilot airdate 2008", ep1)
	}
}

func Test_enrich_series_without_datasets_only_persists_metadata(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_metadata{}, Logger: logger})
	service := matching.New(store, m, nil, logger)

	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)
	if err := service.Enrich_episodes(ctx, entry_id, &matcher.Result{
		Media_type: scanner.Series, Imdb_id: "tt0903747",
	}); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	meta, err := store.Get_series_metadata(ctx, entry_id)
	if err != nil || meta == nil {
		t.Fatalf("series metadata = %v, %v; want it persisted without datasets", meta, err)
	}
	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	for _, episode := range episodes {
		if episode.Status == "matched" {
			t.Errorf("episode %d enriched without datasets: %+v", episode.Id, episode)
		}
	}
}

func Test_enrich_series_without_imdb_id(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, ep1_id, _ := add_series(t, store)

	if err := service.Enrich_episodes(ctx, entry_id, &matcher.Result{
		Media_type: scanner.Series, Imdb_id: "",
	}); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	ep, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if ep[0].Id == ep1_id && ep[0].Status == "matched" {
		t.Errorf("episode enriched without an IMDb id: %+v", ep[0])
	}
}

func Test_rematch_one_missing_entry(t *testing.T) {
	service, _ := new_movie_service(t, fake_offline_hit{})
	applied, candidates, err := service.Rematch_one(context.Background(), 9999)
	if err != nil || applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v err=%v, want no-op", applied, candidates, err)
	}
}

func Test_rematch_one_versionless_entry(t *testing.T) {
	service, store := new_movie_service(t, fake_offline_hit{})
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "No Files Yet", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	applied, candidates, err := service.Rematch_one(ctx, entry_id)
	if err != nil || applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v err=%v, want no-op", applied, candidates, err)
	}
}

func Test_rematch_one_confident_match(t *testing.T) {
	service, store := new_movie_service(t, fake_offline_hit{})
	ctx := context.Background()
	entry_id := add_movie(t, store, "The Matrix", nil)

	applied, candidates, err := service.Rematch_one(ctx, entry_id)
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if !applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v, want applied with no candidates", applied, candidates)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Imdb_id != "tt0133093" {
		t.Errorf("entry = %+v, want matched with tt0133093", entry)
	}
}

func Test_rematch_one_ambiguous_returns_candidates(t *testing.T) {
	service, store := new_offline_service(t, fake_offline_miss{})
	ctx := context.Background()
	entry_id := add_movie(t, store, "The Matrix", nil)

	applied, candidates, err := service.Rematch_one(ctx, entry_id)
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if applied {
		t.Error("ambiguous match was applied")
	}
	if len(candidates) == 0 {
		t.Error("ambiguous match returned no candidates")
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "needs_lookup" {
		t.Errorf("entry status = %q, want unchanged needs_lookup", entry.Status)
	}
}

func Test_reclassify_noop_when_already_series(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)

	_, applied, candidates, err := service.Reclassify(ctx, entry_id, true)
	if err != nil || applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v err=%v, want no-op", applied, candidates, err)
	}
}

func Test_reclassify_series_to_movie_drops_episodes(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)

	_, applied, _, err := service.Reclassify(ctx, entry_id, false)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if applied {
		t.Errorf("ambiguous re-type was applied")
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Media_type != "movie" {
		t.Errorf("media type = %q, want movie", entry.Media_type)
	}
	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 0 {
		t.Errorf("episodes = %d, want 0 after flipping to movie", len(episodes))
	}
}

func Test_reclassify_series_to_movie_confident(t *testing.T) {
	service, store := new_movie_service(t, fake_offline_hit{})
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1234,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	_, applied, candidates, err := service.Reclassify(ctx, entry_id, false)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if !applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v, want applied", applied, candidates)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Media_type != "movie" || entry.Status != "matched" || entry.Imdb_id != "tt0133093" {
		t.Errorf("entry = %+v, want matched movie tt0133093", entry)
	}
}

// fake_series_hit always answers with one confident series hit.
type fake_series_hit struct{}

func (fake_series_hit) Search(context.Context, string, int, scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return []matcher.Offline_candidate{
		{Imdb_id: "tt0386676", Title: "The Office", Year: 2005, Media_type: scanner.Series},
	}, nil
}

// recording_offline remembers the title the IMDb datasets were queried with and
// only answers with its hit when that title is the one the caller was expected
// to use.
type recording_offline struct {
	queries []string
	expect  string
	hit     matcher.Offline_candidate
}

func new_recording_offline(expect string, hit matcher.Offline_candidate) *recording_offline {
	return &recording_offline{expect: expect, hit: hit}
}

func (r *recording_offline) Search(_ context.Context, query string, _ int, _ scanner.Media_type) ([]matcher.Offline_candidate, error) {
	r.queries = append(r.queries, query)
	if query != r.expect {
		return nil, nil
	}
	return []matcher.Offline_candidate{r.hit}, nil
}

func office_offline() *recording_offline {
	return new_recording_offline("The Office", matcher.Offline_candidate{
		Imdb_id: "tt0386676", Title: "The Office", Year: 2005, Media_type: scanner.Series,
	})
}

func Test_series_is_looked_up_by_its_show_name(t *testing.T) {
	source := office_offline()
	service, store := new_offline_service(t, source)
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 1, Episode_number: 3, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	// An episode file name that names nothing: neither the show nor its season.
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib,
		File_path: "The Office/Season 1/Part 3.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	applied, _, err := service.Rematch_one(ctx, entry_id)
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if !applied {
		t.Fatalf("rematch did not match, queries = %v", source.queries)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v, %v", entry, err)
	}
	if entry.Imdb_id != "tt0386676" {
		t.Errorf("entry = %+v, want tt0386676", entry)
	}
}

func Test_run_looks_series_up_by_its_show_name(t *testing.T) {
	source := office_offline()
	service, store := new_offline_service(t, source)
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "Some Wrong Name", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 2, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib,
		File_path: "The Office/Season 2/Some.Show.S02E01.1080p.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	result, err := service.Run(ctx, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Matched != 1 {
		t.Fatalf("result = %+v, want one matched, queries = %v", result, source.queries)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v, %v", entry, err)
	}
	if entry.Imdb_id != "tt0386676" {
		t.Errorf("entry = %+v, want tt0386676", entry)
	}
}

func Test_series_without_show_folder_uses_its_title(t *testing.T) {
	source := office_offline()
	service, store := new_offline_service(t, source)
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	// Episodes sitting straight in the library root: no folder names the show,
	// so the title recorded on the entry has to.
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib,
		File_path: "Part 1.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	applied, _, err := service.Rematch_one(ctx, entry_id)
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if !applied {
		t.Fatalf("rematch did not match, queries = %v", source.queries)
	}
}

func Test_movies_are_still_looked_up_by_file_name(t *testing.T) {
	source := new_recording_offline("The Matrix", matcher.Offline_candidate{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	})
	service, store := new_offline_service(t, source)
	ctx := context.Background()
	lib := library_id(t, store, "Movies", t.TempDir())

	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix Collection", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, Library_id: lib,
		File_path: "The Matrix/The.Matrix.1999.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	applied, _, err := service.Rematch_one(ctx, entry_id)
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if !applied {
		t.Fatalf("rematch did not match, queries = %v", source.queries)
	}
}

func library_id(t *testing.T, store *database.Store, name, path string) int64 {
	t.Helper()
	if err := store.Ensure_library(context.Background(), name, path, true); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	libraries, err := store.List_libraries(context.Background())
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	for _, library := range libraries {
		if library.Name == name {
			return library.Id
		}
	}
	t.Fatalf("library %q not found", name)
	return 0
}

func Test_split_version_to_movie_from_series_entry(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1234,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	new_id, err := service.Split_version_to_movie(ctx, version_id)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if new_id == 0 {
		t.Fatal("split returned no new entry")
	}
	entry, err := store.Get_catalog_entry(ctx, new_id)
	if err != nil || entry == nil {
		t.Fatalf("get split entry: %v", err)
	}
	if entry.Media_type != "movie" || entry.Title != "The Matrix" || entry.Release_year != 1999 || entry.Status != "needs_lookup" {
		t.Errorf("split entry = %+v", entry)
	}
	versions, err := store.List_versions_for_entry(ctx, new_id)
	if err != nil {
		t.Fatalf("list new versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Id != version_id {
		t.Errorf("new entry versions = %+v, want the split file", versions)
	}
	if entry, err := store.Get_catalog_entry(ctx, entry_id); err != nil || entry != nil {
		t.Errorf("drained series still exists: %+v, %v", entry, err)
	}
}

func Test_split_version_to_movie_from_episode_drains_owner(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, ep1_id, _ := add_series(t, store)
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: ep1_id, File_path: "/media/Breaking.Bad.S01E01.720p.mkv", Size_bytes: 4000,
	}); err != nil {
		t.Fatalf("save second version: %v", err)
	}
	versions, err := store.List_versions_for_episode(ctx, ep1_id)
	if err != nil || len(versions) != 2 {
		t.Fatalf("episode versions = %v, %v", versions, err)
	}
	target := versions[0]

	new_id, err := service.Split_version_to_movie(ctx, target.Id)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	// The episode keeps its second version, and the entry keeps the other
	// episode, so nothing is drained yet.
	if entry, err := store.Get_catalog_entry(ctx, entry_id); err != nil || entry == nil {
		t.Fatalf("series drained too early: %v, %v", entry, err)
	}
	// Split the episode's remaining file: the episode disappears.
	remaining, err := store.List_versions_for_episode(ctx, ep1_id)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining versions = %v, %v", remaining, err)
	}
	if _, err := service.Split_version_to_movie(ctx, remaining[0].Id); err != nil {
		t.Fatalf("split remaining: %v", err)
	}
	if episode, err := store.Get_episode(ctx, ep1_id); err != nil || episode != nil {
		t.Errorf("emptied episode not deleted: %+v, %v", episode, err)
	}
	if new_id == 0 {
		t.Error("split returned no new entry")
	}
}

func Test_assign_version_to_episode_from_series(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Office/Interviews.mkv", Size_bytes: 1234,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	episode_id, err := service.Assign_version_to_episode(ctx, version_id, 1, 3)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	episode, err := store.Get_episode(ctx, episode_id)
	if err != nil || episode == nil {
		t.Fatalf("get episode: %v, %v", episode, err)
	}
	if episode.Catalog_entry_id != entry_id || episode.Season_number != 1 || episode.Episode_number != 3 {
		t.Errorf("episode = %+v", episode)
	}
	versions, err := store.List_versions_for_episode(ctx, episode_id)
	if err != nil || len(versions) != 1 || versions[0].Id != version_id {
		t.Errorf("episode versions = %+v, %v", versions, err)
	}
	entry := store_entry(t, store, entry_id)
	if entry != nil && entry.Media_type != "series" {
		t.Errorf("series flipped = %+v", entry)
	}
	if entry, err := store.Get_catalog_entry(ctx, entry_id); err != nil || entry == nil {
		t.Errorf("series lost its entry: %v, %v", entry, err)
	}
}

func Test_assign_version_to_episode_rejects_movies(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	store_entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: store_entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1234,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	if _, err := service.Assign_version_to_episode(ctx, version_id, 1, 1); err == nil {
		t.Error("assigning a movie file to an episode succeeded")
	}
}

func Test_assign_version_to_episode_moves_within_series(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, ep1_id, _ := add_series(t, store)
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: ep1_id, File_path: "/media/Breaking.Bad.S01E01.720p.mkv", Size_bytes: 4000,
	}); err != nil {
		t.Fatalf("save second version: %v", err)
	}
	versions, err := store.List_versions_for_episode(ctx, ep1_id)
	if err != nil || len(versions) != 2 {
		t.Fatalf("episode versions = %v, %v", versions, err)
	}
	target := versions[0]
	other_episode, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 2, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert other episode: %v", err)
	}
	// ep1 keeps its other file, so it survives; the version lands on the new one.
	episode_id, err := service.Assign_version_to_episode(ctx, target.Id, 2, 1)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if episode_id != other_episode {
		t.Errorf("episode id = %d, want %d", episode_id, other_episode)
	}
	if episode, err := store.Get_episode(ctx, ep1_id); err != nil || episode == nil {
		t.Errorf("still-populated episode deleted: %+v, %v", episode, err)
	}
}

func store_entry(t *testing.T, store *database.Store, id int64) *database.Catalog_entry {
	t.Helper()
	entry, err := store.Get_catalog_entry(context.Background(), id)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	return entry
}

func Test_reclassify_movie_to_series_groups_show_folder(t *testing.T) {
	service, store := new_movie_service(t, fake_series_hit{})
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	anchor_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office S01E01", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert anchor: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: anchor_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save anchor version: %v", err)
	}
	sibling_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office S01E02", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert sibling: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: sibling_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E02.mkv", Size_bytes: 1100,
	}); err != nil {
		t.Fatalf("save sibling version: %v", err)
	}
	extra_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "Interviews", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert extra: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: extra_id, Library_id: lib, File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 900,
	}); err != nil {
		t.Fatalf("save extra version: %v", err)
	}

	target, applied, candidates, err := service.Reclassify(ctx, anchor_id, true)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if target != anchor_id {
		t.Fatalf("target = %d, want the flipped anchor %d", target, anchor_id)
	}
	if !applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v, want the folder matched as a series", applied, candidates)
	}

	for _, gone := range []int64{sibling_id, extra_id} {
		entry, err := store.Get_catalog_entry(ctx, gone)
		if err != nil {
			t.Fatalf("get drained entry: %v", err)
		}
		if entry != nil {
			t.Errorf("drained entry %d still exists: %+v", gone, entry)
		}
	}
	anchor, err := store.Get_catalog_entry(ctx, anchor_id)
	if err != nil || anchor == nil {
		t.Fatalf("get anchor: %v", err)
	}
	if anchor.Media_type != "series" || anchor.Title != "The Office" {
		t.Errorf("anchor = %+v, want series named after the folder", anchor)
	}
	episodes, err := store.List_episodes(ctx, anchor_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 3 {
		t.Fatalf("episodes = %d, want the two folded episode rows plus the numbered interview file", len(episodes))
	}
	var episode_versions int
	for _, episode := range episodes {
		versions, err := store.List_versions_for_episode(ctx, episode.Id)
		if err != nil {
			t.Fatalf("episode versions: %v", err)
		}
		episode_versions += len(versions)
	}
	if episode_versions != 3 {
		t.Errorf("versions on episodes = %d, want 3", episode_versions)
	}
	extra, err := store.List_versions_for_entry(ctx, anchor_id)
	if err != nil {
		t.Fatalf("list extra versions: %v", err)
	}
	if len(extra) != 0 {
		t.Errorf("extra versions on the show = %d, want none left loose", len(extra))
	}
}

func Test_reclassify_movie_to_series_merges_into_existing_series(t *testing.T) {
	service, store := new_movie_service(t, fake_series_hit{})
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	series_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save series version: %v", err)
	}

	movie_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office Interviews", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: lib, File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 900,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}

	target, applied, candidates, err := service.Reclassify(ctx, movie_id, true)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if target != series_id {
		t.Fatalf("target = %d, want the existing series %d", target, series_id)
	}
	if !applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v, want the merged series matched", applied, candidates)
	}
	if entry, err := store.Get_catalog_entry(ctx, movie_id); err != nil || entry != nil {
		t.Errorf("re-typed movie entry not drained: entry=%+v err=%v", entry, err)
	}
	series, err := store.Get_catalog_entry(ctx, series_id)
	if err != nil || series == nil {
		t.Fatalf("get series: %v", err)
	}
	if series.Media_type != "series" || series.Imdb_id != "tt0386676" {
		t.Errorf("series = %+v, want matched tt0386676", series)
	}
	episodes, err := store.List_episodes(ctx, series_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Errorf("episodes = %d, want the untouched episode row plus the numbered interview file", len(episodes))
	}
	extras, err := store.List_versions_for_entry(ctx, series_id)
	if err != nil {
		t.Fatalf("list extra versions: %v", err)
	}
	if len(extras) != 0 {
		t.Errorf("extras = %d, want the interview file numbered as an episode", len(extras))
	}
}

func Test_reclassify_movie_to_series_numbers_unnumbered_files(t *testing.T) {
	service, store := new_movie_service(t, fake_series_hit{})
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	files := []struct {
		path  string
		mtime string
	}{
		{"The.Office/Part 10.mkv", "2024-01-01T00:00:03Z"},
		{"The.Office/Part 2.mkv", "2024-01-01T00:00:04Z"},
		{"The.Office/Part 1.mkv", "2024-01-01T00:00:01Z"},
		{"The.Office/Season 1/The.Office.S01E01.mkv", ""},
		{"The.Office/Specials/Bloopers.mkv", "2024-01-01T00:00:05Z"},
		{"The.Office/Season 3/Chapter One.mkv", "2024-01-01T00:00:06Z"},
	}
	for i, file := range files {
		if _, err := store.Save_version(ctx, database.Version{
			Catalog_entry_id: entry_id, Library_id: lib, File_path: file.path,
			Size_bytes: int64(1000 + i), Mtime: file.mtime,
		}); err != nil {
			t.Fatalf("save version: %v", err)
		}
	}

	if _, _, _, err := service.Reclassify(ctx, entry_id, true); err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	numbers := map[string][2]int{}
	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	for _, episode := range episodes {
		versions, err := store.List_versions_for_episode(ctx, episode.Id)
		if err != nil {
			t.Fatalf("episode versions: %v", err)
		}
		if len(versions) != 1 {
			t.Fatalf("episode %d has %d versions, want one", episode.Id, len(versions))
		}
		numbers[versions[0].File_path] = [2]int{episode.Season_number, episode.Episode_number}
	}
	want := map[string][2]int{
		// The numbered file keeps the number its name gave it.
		"The.Office/Season 1/The.Office.S01E01.mkv": {1, 1},
		// Files with no number are numbered in the order they were added in,
		// continuing after the season's highest number.
		"The.Office/Part 1.mkv":  {1, 2},
		"The.Office/Part 10.mkv": {1, 3},
		// Two files added at the same time fall back to their names, read the
		// way a human reads them: part 2 before part 10.
		"The.Office/Part 2.mkv":               {1, 4},
		"The.Office/Season 3/Chapter One.mkv": {3, 1},
		// Extras land in the specials season instead of shifting the show's.
		"The.Office/Specials/Bloopers.mkv": {0, 1},
	}
	if len(numbers) != len(want) {
		t.Fatalf("episodes = %v, want %v", numbers, want)
	}
	for path, expected := range want {
		got, ok := numbers[path]
		if !ok {
			t.Errorf("%s is not on an episode", path)
			continue
		}
		if got != expected {
			t.Errorf("%s = S%02dE%02d, want S%02dE%02d", path, got[0], got[1], expected[0], expected[1])
		}
	}
	loose, err := store.List_versions_for_entry(ctx, entry_id)
	if err != nil {
		t.Fatalf("list loose versions: %v", err)
	}
	if len(loose) != 0 {
		t.Errorf("loose versions = %d, want none", len(loose))
	}
}

func Test_reclassify_movie_to_series_keeps_assigned_episodes(t *testing.T) {
	service, store := new_movie_service(t, fake_series_hit{})
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	series_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "matched", Imdb_id: "tt0386676",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	// An episode a user placed by hand, on a file whose name says S01E01.
	assigned_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 2, Episode_number: 5, Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: assigned_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save series version: %v", err)
	}

	movie_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office S01E02", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E02.mkv", Size_bytes: 1100,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}

	if _, _, _, err := service.Reclassify(ctx, movie_id, true); err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	versions, err := store.List_versions_for_episode(ctx, assigned_id)
	if err != nil {
		t.Fatalf("assigned episode versions: %v", err)
	}
	if len(versions) != 1 || versions[0].File_path != "The.Office/Season 1/The.Office.S01E01.mkv" {
		t.Errorf("assigned episode = %+v, want the show's own file left in place", versions)
	}
}

func Test_reclassify_movie_to_series_flat_in_library_root_stays_single(t *testing.T) {
	service, store := new_movie_service(t, fake_series_hit{})
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, Library_id: lib, File_path: "The.Matrix.1999.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	target, _, _, err := service.Reclassify(ctx, entry_id, true)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if target != entry_id {
		t.Errorf("target = %d, want the single entry %d", target, entry_id)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Media_type != "series" || entry.Title != "The Matrix" {
		t.Errorf("entry = %+v, want a plain flipped series with its filename title", entry)
	}
}
