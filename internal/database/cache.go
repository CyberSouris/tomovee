package database

import (
	"context"
	"database/sql"
	"errors"
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

// Cache_put stores or replaces a cached payload for (kind, key).
func (s *Store) Cache_put(ctx context.Context, kind, key, payload string) error {
	return s.exec_tx(ctx, `
		INSERT INTO lookup_cache (kind, key, payload) VALUES (?, ?, ?)
		ON CONFLICT(kind, key) DO UPDATE SET
			payload = excluded.payload,
			created_at = datetime('now')`,
		kind, key, payload)
}
