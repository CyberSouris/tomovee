package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Cache_get returns the cached payload for (kind, key), if present. Payloads
// are opaque strings (typically JSON) owned by the caller.
func (s *Store) Cache_get(ctx context.Context, kind, key string) (string, bool, error) {
	var payload string
	err := s.db.db.QueryRowContext(ctx,
		"SELECT payload FROM lookup_cache WHERE kind = ? AND key = ?", kind, key).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return payload, true, nil
}

// Cache_get_fresh behaves like Cache_get but treats entries older than max_age
// as absent. A non-positive max_age accepts any age.
func (s *Store) Cache_get_fresh(ctx context.Context, kind, key string, max_age time.Duration) (string, bool, error) {
	var payload, created_at string
	err := s.db.db.QueryRowContext(ctx,
		"SELECT payload, created_at FROM lookup_cache WHERE kind = ? AND key = ?", kind, key).
		Scan(&payload, &created_at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if max_age > 0 {
		created, err := time.Parse("2006-01-02 15:04:05", created_at)
		if err == nil && time.Since(created) > max_age {
			return "", false, nil
		}
	}
	return payload, true, nil
}

// Cache_put stores or replaces a cached payload for (kind, key).
func (s *Store) Cache_put(ctx context.Context, kind, key, payload string) error {
	return s.exec_tx(ctx, `
		INSERT INTO lookup_cache (kind, key, payload) VALUES (?, ?, ?)
		ON CONFLICT(kind, key) DO UPDATE SET
			payload = excluded.payload,
			created_at = datetime('now')`,
		kind, key, payload)
}
