package database

import (
	"context"
	"database/sql"
	"errors"
)

// Config_get returns the persisted value for key, if set.
func (s *Store) Config_get(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}

// Config_set persists a key/value runtime setting.
func (s *Store) Config_set(ctx context.Context, key, value string) error {
	return s.exec_tx(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
}

// Config_all returns every persisted setting.
func (s *Store) Config_all(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.db.QueryContext(ctx, "SELECT key, value FROM config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}
