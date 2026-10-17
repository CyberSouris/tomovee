package matching_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

type fake_metadata struct{}

func (fake_metadata) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	return nil, nil
}

func (fake_metadata) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	return []tmdb.Tv_search_result{{
		Id: 1396, Name: "Breaking Bad", First_air_date: "2008-01-20",
	}}, nil
}

func (fake_metadata) Movie_details(context.Context, int) (*tmdb.Movie_details, error) {
	return &tmdb.Movie_details{}, nil
}

func (fake_metadata) Tv_details(context.Context, int) (*tmdb.Tv_details, error) {
	return &tmdb.Tv_details{
		Id: 1396, Imdb_id: "tt0903747", Name: "Breaking Bad",
		Original_name: "Breaking Bad", First_air_date: "2008-01-20",
		Last_air_date: "2013-09-29", Number_of_seasons: 5, Number_of_episodes: 62,
	}, nil
}

func (fake_metadata) Find_by_imdb(context.Context, string) (*tmdb.Find_result, error) {
	return &tmdb.Find_result{}, nil
}

func new_test_index(t *testing.T) *imdb_datasets.Index {
	t.Helper()
	titles, err := imdb_datasets.Parse_titles(strings.NewReader(
		"tconst\ttitleType\tprimaryTitle\toriginalTitle\tisAdult\tstartYear\tendYear\truntimeMinutes\tgenres\n" +
			"tt0903747\ttvSeries\tBreaking Bad\tBreaking Bad\t0\t2008\t2013\t49\tCrime,Drama\n" +
			"tt1586952\tepisode\tPilot\tPilot\t0\t2008\t\\N\t\\N\t\\N\n" +
			"tt1586954\tepisode\tCat's in the Bag...\tCat's in the Bag...\t0\t2008\t\\N\t\\N\t\\N\n"))
	if err != nil {
		t.Fatalf("parse titles: %v", err)
	}
	index := imdb_datasets.New_index(titles)
	episodes, err := imdb_datasets.Parse_episodes(strings.NewReader(
		"tconst\tparentTconst\tseasonNumber\tepisodeNumber\n" +
			"tt1586952\ttt0903747\t1\t1\n" +
			"tt1586954\ttt0903747\t1\t2\n"))
	if err != nil {
		t.Fatalf("parse episodes: %v", err)
	}
	index.Index_episodes(episodes)
	return index
}

func new_test_service(t *testing.T) (*matching.Matching, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_metadata{}, Logger: logger})
	return matching.New(store, m, new_test_index(t), logger), store
}

func add_series(t *testing.T, store *database.Store) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "Breaking Bad", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	ep1_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode 1: %v", err)
	}
	ep2_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 1, Episode_number: 2, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode 2: %v", err)
	}
	for _, version := range []database.Version{
		{Episode_id: ep1_id, File_path: "/media/Breaking.Bad.S01E02.mkv", Size_bytes: 8000, Hash: "hash-smaller"},
		{Episode_id: ep1_id, File_path: "/media/Breaking.Bad.S01E01.mkv", Size_bytes: 9000, Hash: "hash-largest"},
		{Episode_id: ep2_id, File_path: "/media/Breaking.Bad.S01E02.mkv", Size_bytes: 7000, Hash: "hash-ep2"},
	} {
		if _, err := store.Save_version(ctx, version); err != nil {
			t.Fatalf("save version: %v", err)
		}
	}
	return entry_id, ep1_id, ep2_id
}

func Test_run_matches_and_enriches_series(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)

	result, err := service.Run(ctx, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Total != 1 || result.Matched != 1 || result.Unmatched != 0 {
		t.Fatalf("result = %+v, want one matched", result)
	}

	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Title != "Breaking Bad" ||
		entry.Imdb_id != "tt0903747" || entry.Media_type != "series" {
		t.Fatalf("entry = %+v, want matched series", entry)
	}

	meta, err := store.Get_series_metadata(ctx, entry_id)
	if err != nil || meta == nil {
		t.Fatalf("series metadata: %v, %v", meta, err)
	}
	if meta.First_air_date != "2008-01-20" || meta.Num_episodes != 62 {
		t.Errorf("metadata = %+v", meta)
	}

	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes))
	}
	by_number := map[int]database.Episode{}
	for _, episode := range episodes {
		by_number[episode.Episode_number] = episode
	}
	ep1 := by_number[1]
	if ep1.Status != "matched" || ep1.Title != "Pilot" || ep1.Airdate != "2008" {
		t.Errorf("episode 1 = %+v, want matched pilot airdate 2008", ep1)
	}
	ep2 := by_number[2]
	if ep2.Status != "matched" || ep2.Title != "Cat's in the Bag..." {
		t.Errorf("episode 2 = %+v, want matched with offline title", ep2)
	}
}

func Test_set_datasets_after_construction_enriches(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_metadata{}, Logger: logger})

	// Built without a dataset, like the daemon before the background load
	// finishes; the offline index is attached afterwards.
	service := matching.New(store, m, nil, logger)
	service.Set_datasets(new_test_index(t))

	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)
	result, err := service.Run(ctx, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Total != 1 || result.Matched != 1 {
		t.Fatalf("result = %+v, want one matched", result)
	}

	episodes, err := store.List_episodes(ctx, entry_id)
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes))
	}
	by_number := map[int]database.Episode{}
	for _, episode := range episodes {
		by_number[episode.Episode_number] = episode
	}
	if ep1 := by_number[1]; ep1.Status != "matched" || ep1.Title != "Pilot" || ep1.Airdate != "2008" {
		t.Errorf("episode 1 = %+v, want matched pilot airdate 2008", ep1)
	}
	if ep2 := by_number[2]; ep2.Status != "matched" || ep2.Title != "Cat's in the Bag..." {
		t.Errorf("episode 2 = %+v, want matched with offline title", ep2)
	}
}

func Test_run_force_rematch_drops_unmatchable_match(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()

	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix Reloaded", Release_year: 2003,
		Overview: "stale overview", Runtime_minutes: 138, Rating: 7.1,
		Vote_count: 5000, Imdb_id: "tt0234215", Tmdb_id: 604,
		Poster_path: "/reloaded.jpg", Status: "matched", Genres: []string{"Action"},
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: id, File_path: "/media/The.Matrix.1999.mkv",
		Size_bytes: 9000, Hash: "hash-1",
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	// The fake matcher has no movie source, so a forced rematch cannot match
	// this entry and must drop the accepted match instead of keeping it.
	result, err := service.Run_force_rematch(ctx, nil, nil)
	if err != nil {
		t.Fatalf("force rematch: %v", err)
	}
	if result.Total != 1 || result.Matched != 0 || result.Unmatched != 1 {
		t.Fatalf("result = %+v, want single unmatched", result)
	}

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "needs_lookup" {
		t.Errorf("status = %q, want needs_lookup", entry.Status)
	}
	if entry.Title != "The Matrix" || entry.Release_year != 1999 {
		t.Errorf("title/year = %q/%d, want the file-derived The Matrix/1999", entry.Title, entry.Release_year)
	}
	if entry.Imdb_id != "" || entry.Tmdb_id != 0 || entry.Overview != "" ||
		entry.Poster_path != "" || entry.Rating != 0 || entry.Vote_count != 0 {
		t.Errorf("stale match metadata survived: %+v", entry)
	}
	if len(entry.Genres) != 0 {
		t.Errorf("stale genres survived: %+v", entry.Genres)
	}
}

func Test_run_force_rematch_rematches_series(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	entry_id, _, _ := add_series(t, store)
	if _, err := service.Run(ctx, nil, nil); err != nil {
		t.Fatalf("initial run: %v", err)
	}

	// Corrupt the accepted match so the force rematch has something to drop.
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Title = "Wrong Show"
	entry.Imdb_id = "tt0000000"
	if err := store.Update_catalog_entry(ctx, entry_id, *entry); err != nil {
		t.Fatalf("corrupt entry: %v", err)
	}

	result, err := service.Run_force_rematch(ctx, nil, nil)
	if err != nil {
		t.Fatalf("force rematch: %v", err)
	}
	if result.Total != 1 || result.Matched != 1 {
		t.Fatalf("result = %+v, want one matched", result)
	}
	fresh, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || fresh == nil {
		t.Fatalf("get fresh entry: %v", err)
	}
	if fresh.Title != "Breaking Bad" || fresh.Imdb_id != "tt0903747" || fresh.Status != "matched" {
		t.Errorf("entry = %+v, want re-matched Breaking Bad", fresh)
	}
}

func Test_run_leaves_versionless_entries_unmatched(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	if _, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "No Files Yet", Status: "needs_lookup",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	result, err := service.Run(ctx, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Total != 1 || result.Matched != 0 || result.Unmatched != 1 {
		t.Fatalf("result = %+v, want single unmatched entry", result)
	}
}
