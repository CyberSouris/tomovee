package metacache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

type recording_source struct {
	tv_search_calls int
	tv_calls        int
	imdb_calls      int
	detail_err      error
}

func (f *recording_source) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	return nil, nil
}

func (f *recording_source) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	f.tv_search_calls++
	return []tmdb.Tv_search_result{{Id: 1396, Name: "Breaking Bad"}}, nil
}

func (f *recording_source) Movie_details(context.Context, int) (*tmdb.Movie_details, error) {
	return nil, f.detail_err
}

func (f *recording_source) Tv_details(_ context.Context, id int) (*tmdb.Tv_details, error) {
	f.tv_calls++
	return &tmdb.Tv_details{Id: id, Name: "Show"}, nil
}

func (f *recording_source) Find_by_imdb(context.Context, string) (*tmdb.Find_result, error) {
	f.imdb_calls++
	return &tmdb.Find_result{Tv_results: []tmdb.Tv_search_result{{Id: 7}}}, nil
}

func new_recording_cache(t *testing.T) (*Cache, *recording_source, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	source := &recording_source{}
	cache := New(source, store, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return cache, source, store
}

func Test_search_tv_cached(t *testing.T) {
	cache, source, _ := new_recording_cache(t)
	ctx := context.Background()

	first, err := cache.Search_tv(ctx, "breaking", 2008)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := cache.Search_tv(ctx, "breaking", 2008)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(first) != 1 || first[0].Name != "Breaking Bad" {
		t.Fatalf("first result = %+v", first)
	}
	if len(second) != 1 {
		t.Fatalf("second result = %+v", second)
	}
	if source.tv_search_calls != 1 {
		t.Errorf("tv_search_calls = %d, want 1", source.tv_search_calls)
	}
}

func Test_tv_details_cached(t *testing.T) {
	cache, source, _ := new_recording_cache(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		details, err := cache.Tv_details(ctx, 1396)
		if err != nil {
			t.Fatalf("tv_details: %v", err)
		}
		if details.Id != 1396 {
			t.Errorf("details id = %d, want 1396", details.Id)
		}
	}
	if source.tv_calls != 1 {
		t.Errorf("tv_calls = %d, want 1", source.tv_calls)
	}
}

func Test_find_by_imdb_cached(t *testing.T) {
	cache, source, _ := new_recording_cache(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		result, err := cache.Find_by_imdb(ctx, "tt0903747")
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if len(result.Tv_results) != 1 {
			t.Errorf("results = %d, want 1", len(result.Tv_results))
		}
	}
	if source.imdb_calls != 1 {
		t.Errorf("imdb_calls = %d, want 1", source.imdb_calls)
	}
}

func Test_corrupt_payload_refetches(t *testing.T) {
	cache, source, store := new_recording_cache(t)
	ctx := context.Background()

	// A corrupt payload must fall through to the source instead of being
	// returned verbatim.
	if err := store.Cache_put(ctx, "tmdb.tv", "1396", "not json {"); err != nil {
		t.Fatalf("cache_put: %v", err)
	}
	details, err := cache.Tv_details(ctx, 1396)
	if err != nil {
		t.Fatalf("tv_details: %v", err)
	}
	if details.Name != "Show" {
		t.Errorf("details = %+v, want the source value", details)
	}
	if source.tv_calls != 1 {
		t.Errorf("tv_calls = %d, want 1 (refetch after corrupt payload)", source.tv_calls)
	}
}

func Test_fetch_error_propagates(t *testing.T) {
	cache, source, _ := new_recording_cache(t)
	ctx := context.Background()
	source.detail_err = errors.New("tmdb down")

	if _, err := cache.Movie_details(ctx, 603); err == nil {
		t.Fatal("expected the source error")
	}
	if _, err := cache.Movie_details(ctx, 603); err == nil {
		t.Fatal("expected the source error again")
	}
}
