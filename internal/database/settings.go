package database

import (
	"context"
	"database/sql"
	"errors"
)

// Watch_folder is a configured directory with its watching state.
type Watch_folder struct {
	Path      string
	Enabled   bool
	Last_scan string
}

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

// List_watch_folders returns all configured watch folders, enabled or not.
func (s *Store) List_watch_folders(ctx context.Context) ([]Watch_folder, error) {
	rows, err := s.db.db.QueryContext(ctx,
		"SELECT path, enabled, COALESCE(last_scan, '') FROM watch_folder ORDER BY path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch_folder
	for rows.Next() {
		var folder Watch_folder
		var enabled int
		if err := rows.Scan(&folder.Path, &enabled, &folder.Last_scan); err != nil {
			return nil, err
		}
		folder.Enabled = enabled == 1
		out = append(out, folder)
	}
	return out, rows.Err()
}

// Enabled_watch_folders returns only the folders with watching turned on.
func (s *Store) Enabled_watch_folders(ctx context.Context) ([]Watch_folder, error) {
	all, err := s.List_watch_folders(ctx)
	if err != nil {
		return nil, err
	}
	enabled := all[:0]
	for _, folder := range all {
		if folder.Enabled {
			enabled = append(enabled, folder)
		}
	}
	return enabled, nil
}

// Ensure_watch_folder registers path if it is not already tracked, without
// altering the enabled state of an existing row.
func (s *Store) Ensure_watch_folder(ctx context.Context, path string, enabled bool) error {
	return s.exec_tx(ctx, `
		INSERT INTO watch_folder (path, enabled) VALUES (?, ?)
		ON CONFLICT(path) DO NOTHING`, path, bool_to_int(enabled))
}

// Set_watch_folder enables or disables watching for path, creating the row if
// needed. Disabling preserves the recorded last_scan time.
func (s *Store) Set_watch_folder(ctx context.Context, path string, enabled bool) error {
	return s.exec_tx(ctx, `
		INSERT INTO watch_folder (path, enabled) VALUES (?, ?)
		ON CONFLICT(path) DO UPDATE SET enabled = excluded.enabled`,
		path, bool_to_int(enabled))
}

// Touch_watch_folder records the completion time of a watcher's scan.
func (s *Store) Touch_watch_folder(ctx context.Context, path, last_scan string) error {
	return s.exec_tx(ctx, "UPDATE watch_folder SET last_scan = ? WHERE path = ?", last_scan, path)
}
