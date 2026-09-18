// Library repository: named, independently refreshable media roots. Each
// library persists its own path and watching state; version file paths are
// stored relative to the library's root.
package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Library is a named media root directory.
type Library struct {
	Id        int64
	Name      string
	Path      string
	Enabled   bool
	Last_scan string
}

// List_libraries returns every registered library, ordered by name.
func (s *Store) List_libraries(ctx context.Context) ([]Library, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT id, name, path, enabled, COALESCE(last_scan, '')
		FROM library ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var library Library
		var enabled int
		if err := rows.Scan(&library.Id, &library.Name, &library.Path, &enabled, &library.Last_scan); err != nil {
			return nil, err
		}
		library.Enabled = enabled == 1
		out = append(out, library)
	}
	return out, rows.Err()
}

// Enabled_libraries returns only the libraries with watching turned on.
func (s *Store) Enabled_libraries(ctx context.Context) ([]Library, error) {
	all, err := s.List_libraries(ctx)
	if err != nil {
		return nil, err
	}
	enabled := all[:0]
	for _, library := range all {
		if library.Enabled {
			enabled = append(enabled, library)
		}
	}
	return enabled, nil
}

// Ensure_library registers name if it is not already tracked, using
// default_enabled for the row. When a row for the same path exists under a
// different name (e.g. a folder migrated from an older release), it is renamed
// to name so configuration remains the source of truth. An existing row's own
// enabled state is never altered.
func (s *Store) Ensure_library(ctx context.Context, name, path string, default_enabled bool) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		_, found, err := query_id_tx(ctx, tx, "SELECT id FROM library WHERE name = ?", name)
		if err != nil || found {
			return err
		}
		matching_path, found, err := query_id_tx(ctx, tx, "SELECT id FROM library WHERE path = ? LIMIT 1", path)
		if err != nil {
			return err
		}
		if found {
			_, err := tx.ExecContext(ctx, "UPDATE library SET name = ? WHERE id = ?", name, matching_path)
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO library (name, path, enabled) VALUES (?, ?, ?)",
			name, path, bool_to_int(default_enabled))
		return err
	})
}

// Upsert_library registers name with the given path and enabled state, or
// updates those fields when the library already exists. It is used by the
// settings web API.
func (s *Store) Upsert_library(ctx context.Context, name, path string, enabled bool) error {
	return s.exec_tx(ctx, `
		INSERT INTO library (name, path, enabled) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET path = excluded.path, enabled = excluded.enabled`,
		name, path, bool_to_int(enabled))
}

// Touch_library records the completion time of a watcher's scan.
func (s *Store) Touch_library(ctx context.Context, name, last_scan string) error {
	return s.exec_tx(ctx, "UPDATE library SET last_scan = ? WHERE name = ?", last_scan, name)
}

// Normalize_library_versions rewrites version rows that were stored with an
// absolute file path under library.Path so they carry library.Id and a
// library-relative file path, converting pre-library installs on their next
// scan. A legacy row whose (library, relative path) slot is already taken by a
// fresher row is dropped. It returns the number of rows rewritten.
func (s *Store) Normalize_library_versions(ctx context.Context, library Library) (int, error) {
	rows, err := s.db.db.QueryContext(ctx,
		"SELECT id, file_path FROM version WHERE library_id IS NULL")
	if err != nil {
		return 0, err
	}
	type legacy struct {
		id   int64
		path string
	}
	var candidates []legacy
	for rows.Next() {
		var candidate legacy
		if err := rows.Scan(&candidate.id, &candidate.path); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	rewritten := 0
	err = s.with_tx(ctx, func(tx *sql.Tx) error {
		for _, candidate := range candidates {
			relative, ok := relative_within(candidate.path, library.Path)
			if !ok {
				continue
			}
			var replaced int64
			dup := tx.QueryRowContext(ctx, `
				SELECT id FROM version WHERE library_id = ? AND file_path = ? LIMIT 1`,
				library.Id, relative).Scan(&replaced)
			if dup == nil {
				if _, err := tx.ExecContext(ctx, "DELETE FROM version WHERE id = ?", candidate.id); err != nil {
					return err
				}
				rewritten++
				continue
			}
			if !errors.Is(dup, sql.ErrNoRows) {
				return dup
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE version SET library_id = ?, file_path = ?
				WHERE id = ? AND library_id IS NULL`,
				library.Id, relative, candidate.id); err != nil {
				return err
			}
			rewritten++
		}
		return nil
	})
	return rewritten, err
}

// relative_within relativizes path against root when path is an absolute path
// inside root, returning false otherwise.
func relative_within(path, root string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", false
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return relative, true
}
