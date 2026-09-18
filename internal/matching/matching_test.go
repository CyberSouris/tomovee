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

func new_test_service(t *testing.T) (*matching.Matching, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)

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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_metadata{}, Logger: logger})
	return matching.New(store, m, index, logger), store
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

	result, err := service.Run(ctx, nil)
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

func Test_run_leaves_versionless_entries_unmatched(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()
	if _, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "No Files Yet", Status: "needs_lookup",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	result, err := service.Run(ctx, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Total != 1 || result.Matched != 0 || result.Unmatched != 1 {
		t.Fatalf("result = %+v, want single unmatched entry", result)
	}
}
