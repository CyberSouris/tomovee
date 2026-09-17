// Package metacache wraps a matcher.Metadata_source with a persistent lookup
// cache so repeated TMDB searches and detail fetches avoid the network.
package metacache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

// Default_ttl is how long cached API responses are considered fresh.
const Default_ttl = 7 * 24 * time.Hour

// Cache decorates a Metadata_source with a lookup_cache-backed cache.
type Cache struct {
	source matcher.Metadata_source
	store  *database.Store
	ttl    time.Duration
	logger *slog.Logger
}

// New builds a Cache. A non-positive ttl disables expiry.
func New(source matcher.Metadata_source, store *database.Store, ttl time.Duration, logger *slog.Logger) *Cache {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cache{source: source, store: store, ttl: ttl, logger: logger}
}

// Search_movie caches movie search results.
func (c *Cache) Search_movie(ctx context.Context, query string, year int) ([]tmdb.Movie_search_result, error) {
	return cached(c, ctx, "tmdb.search_movie", fmt.Sprintf("%s|%d", query, year),
		func() ([]tmdb.Movie_search_result, error) { return c.source.Search_movie(ctx, query, year) })
}

// Search_tv caches TV search results.
func (c *Cache) Search_tv(ctx context.Context, query string, year int) ([]tmdb.Tv_search_result, error) {
	return cached(c, ctx, "tmdb.search_tv", fmt.Sprintf("%s|%d", query, year),
		func() ([]tmdb.Tv_search_result, error) { return c.source.Search_tv(ctx, query, year) })
}

// Movie_details caches full movie records.
func (c *Cache) Movie_details(ctx context.Context, id int) (*tmdb.Movie_details, error) {
	return cached(c, ctx, "tmdb.movie", fmt.Sprintf("%d", id),
		func() (*tmdb.Movie_details, error) { return c.source.Movie_details(ctx, id) })
}

// Tv_details caches full TV records.
func (c *Cache) Tv_details(ctx context.Context, id int) (*tmdb.Tv_details, error) {
	return cached(c, ctx, "tmdb.tv", fmt.Sprintf("%d", id),
		func() (*tmdb.Tv_details, error) { return c.source.Tv_details(ctx, id) })
}

// Find_by_imdb caches external-id resolutions.
func (c *Cache) Find_by_imdb(ctx context.Context, imdb_id string) (*tmdb.Find_result, error) {
	return cached(c, ctx, "tmdb.find_imdb", imdb_id,
		func() (*tmdb.Find_result, error) { return c.source.Find_by_imdb(ctx, imdb_id) })
}

func cached[T any](c *Cache, ctx context.Context, kind, key string, fetch func() (T, error)) (T, error) {
	var zero T
	if payload, ok, err := c.store.Cache_get_fresh(ctx, kind, key, c.ttl); err != nil {
		c.logger.Warn("metadata cache read failed", "kind", kind, "error", err)
	} else if ok {
		var value T
		if err := json.Unmarshal([]byte(payload), &value); err == nil {
			return value, nil
		}
		c.logger.Warn("metadata cache payload invalid", "kind", kind)
	}

	value, err := fetch()
	if err != nil {
		return zero, err
	}
	if payload, err := json.Marshal(value); err == nil {
		if err := c.store.Cache_put(ctx, kind, key, string(payload)); err != nil {
			c.logger.Warn("metadata cache write failed", "kind", kind, "error", err)
		}
	}
	return value, nil
}
