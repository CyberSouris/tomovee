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

	applied, candidates, err := service.Reclassify(ctx, entry_id, true)
	if err != nil || applied || candidates != nil {
		t.Errorf("applied=%v candidates=%v err=%v, want no-op", applied, candidates, err)
	}
}

func Test_reclassify_series_to_movie_drops_episodes(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)

	applied, _, err := service.Reclassify(ctx, entry_id, false)
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

	applied, candidates, err := service.Reclassify(ctx, entry_id, false)
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
