package metacache

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

type fake_source struct {
	search_movie_calls int
	movie_calls        int
}

func (f *fake_source) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	f.search_movie_calls++
	return []tmdb.Movie_search_result{{Id: 1, Title: "Result"}}, nil
}

func (f *fake_source) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	return nil, nil
}

func (f *fake_source) Movie_details(_ context.Context, id int) (*tmdb.Movie_details, error) {
	f.movie_calls++
	return &tmdb.Movie_details{Id: id, Title: "Movie"}, nil
}

func (f *fake_source) Tv_details(context.Context, int) (*tmdb.Tv_details, error) {
	return &tmdb.Tv_details{}, nil
}

func (f *fake_source) Find_by_imdb(context.Context, string) (*tmdb.Find_result, error) {
	return &tmdb.Find_result{}, nil
}

func new_test_cache(t *testing.T, ttl time.Duration) (*Cache, *fake_source) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	source := &fake_source{}
	cache := New(source, database.New_store(d), ttl, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return cache, source
}

func Test_caches_repeated_lookups(t *testing.T) {
	cache, source := new_test_cache(t, time.Hour)
	ctx := context.Background()

	first, err := cache.Movie_details(ctx, 603)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := cache.Movie_details(ctx, 603)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Title != second.Title {
		t.Errorf("titles differ: %q vs %q", first.Title, second.Title)
	}
	if source.movie_calls != 1 {
		t.Errorf("movie calls = %d, want 1", source.movie_calls)
	}

	if _, err := cache.Movie_details(ctx, 604); err != nil {
		t.Fatalf("other id: %v", err)
	}
	if source.movie_calls != 2 {
		t.Errorf("movie calls = %d, want 2", source.movie_calls)
	}

	if _, err := cache.Search_movie(ctx, "matrix", 1999); err != nil {
		t.Fatalf("search: %v", err)
	}
	if _, err := cache.Search_movie(ctx, "matrix", 1999); err != nil {
		t.Fatalf("search again: %v", err)
	}
	if source.search_movie_calls != 1 {
		t.Errorf("search calls = %d, want 1", source.search_movie_calls)
	}
}

func Test_expired_entries_are_refetched(t *testing.T) {
	cache, source := new_test_cache(t, time.Nanosecond)
	ctx := context.Background()

	if _, err := cache.Movie_details(ctx, 603); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := cache.Movie_details(ctx, 603); err != nil {
		t.Fatalf("second: %v", err)
	}
	if source.movie_calls != 2 {
		t.Errorf("movie calls = %d, want 2", source.movie_calls)
	}
}

func Test_zero_ttl_never_expires(t *testing.T) {
	cache, source := new_test_cache(t, 0)
	ctx := context.Background()

	if _, err := cache.Movie_details(ctx, 603); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := cache.Movie_details(ctx, 603); err != nil {
		t.Fatalf("second: %v", err)
	}
	if source.movie_calls != 1 {
		t.Errorf("movie calls = %d, want 1", source.movie_calls)
	}
}
