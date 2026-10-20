package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Upsert_catalog_entry inserts a catalog entry or updates the existing one
// matched by IMDb id, then TMDB id, then (media type, title, year). Genres are
// replaced with entry.Genres. It returns the entry id.
func (s *Store) Upsert_catalog_entry(ctx context.Context, entry Catalog_entry) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		found, err := find_entry_tx(ctx, tx, entry)
		if err != nil {
			return err
		}
		if found > 0 {
			id = found
			if err := update_entry_tx(ctx, tx, id, entry); err != nil {
				return err
			}
		} else {
			created, err := insert_entry_tx(ctx, tx, entry)
			if err != nil {
				return err
			}
			id = created
		}
		// Genres are only replaced when the caller supplies them, so partial
		// updates (e.g. retrying an unmatched entry) do not wipe them.
		if len(entry.Genres) > 0 {
			return set_genres_tx(ctx, tx, id, entry.Genres)
		}
		return nil
	})
	return id, err
}

// Find_catalog_entry returns the id of the existing catalog entry that a new
// or updated entry would group under (IMDb id, then TMDB id, then media type +
// title + year), mirroring the upsert grouping logic. The second return is
// false when no such entry exists.
func (s *Store) Find_catalog_entry(ctx context.Context, entry Catalog_entry) (int64, bool, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		found, err := find_entry_tx(ctx, tx, entry)
		if err != nil {
			return err
		}
		id = found
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return id, id > 0, nil
}

// Find_entry_by_external_id returns the id of the catalog entry that already
// holds the given IMDb or TMDB id, or 0 when none does. exclude skips one entry
// (typically the one being matched) so a caller can ask "does any *other* entry
// already own this id?". The unique indexes on both columns mean at most one
// entry can answer each lookup, and IMDb id wins when both are set.
func (s *Store) Find_entry_by_external_id(ctx context.Context, imdb_id string, tmdb_id int64, exclude int64) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		if imdb_id != "" {
			found, ok, err := query_id_tx(ctx, tx,
				"SELECT id FROM catalog_entry WHERE imdb_id = ? AND id != ? LIMIT 1", imdb_id, exclude)
			if err != nil {
				return err
			}
			if ok {
				id = found
				return nil
			}
		}
		if tmdb_id > 0 {
			found, ok, err := query_id_tx(ctx, tx,
				"SELECT id FROM catalog_entry WHERE tmdb_id = ? AND id != ? LIMIT 1", tmdb_id, exclude)
			if err != nil {
				return err
			}
			if ok {
				id = found
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func find_entry_tx(ctx context.Context, tx *sql.Tx, entry Catalog_entry) (int64, error) {
	if entry.Imdb_id != "" {
		if id, ok, err := query_id_tx(ctx, tx, "SELECT id FROM catalog_entry WHERE imdb_id = ? LIMIT 1", entry.Imdb_id); err != nil || ok {
			return id, err
		}
	}
	if entry.Tmdb_id > 0 {
		if id, ok, err := query_id_tx(ctx, tx, "SELECT id FROM catalog_entry WHERE tmdb_id = ? LIMIT 1", entry.Tmdb_id); err != nil || ok {
			return id, err
		}
	}
	query := "SELECT id FROM catalog_entry WHERE media_type = ? AND title = ? AND (release_year IS NULL OR release_year = 0) LIMIT 1"
	args := []any{Media_type_code(entry.Media_type), entry.Title}
	if entry.Release_year > 0 {
		query = "SELECT id FROM catalog_entry WHERE media_type = ? AND title = ? AND release_year = ? LIMIT 1"
		args = append(args, entry.Release_year)
	}
	id, ok, err := query_id_tx(ctx, tx, query, args...)
	if err != nil || !ok {
		return 0, err
	}
	return id, nil
}

func query_id_tx(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func insert_entry_tx(ctx context.Context, tx *sql.Tx, entry Catalog_entry) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO catalog_entry
			(media_type, title, release_year, imdb_id, tmdb_id, poster_path)
		VALUES (?, ?, ?, ?, ?, ?)`,
		Media_type_code(entry.Media_type), entry.Title, nullable_int(entry.Release_year),
		nullable_string(entry.Imdb_id), nullable_int(entry.Tmdb_id),
		nullable_string(entry.Poster_path))
	if err != nil {
		return 0, fmt.Errorf("insert catalog entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func update_entry_tx(ctx context.Context, tx *sql.Tx, id int64, entry Catalog_entry) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE catalog_entry SET
			media_type = ?, title = ?, release_year = ?,
			imdb_id = ?, tmdb_id = ?, poster_path = COALESCE(?, poster_path),
			updated_at = datetime('now')
		WHERE id = ?`,
		Media_type_code(entry.Media_type), entry.Title, nullable_int(entry.Release_year),
		nullable_string(entry.Imdb_id), nullable_int(entry.Tmdb_id),
		nullable_string(entry.Poster_path), id)
	if err != nil {
		return fmt.Errorf("update catalog entry %d: %w", id, err)
	}
	return nil
}

// Update_catalog_entry overwrites the metadata of an existing entry without
// re-grouping, preserving its media files. It does not touch the pending
// candidate shortlist: that is cleared separately by the paths that record a
// match decision, so a poster-only update never discards a picker.
func (s *Store) Update_catalog_entry(ctx context.Context, id int64, entry Catalog_entry) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if err := update_entry_tx(ctx, tx, id, entry); err != nil {
			return err
		}
		if len(entry.Genres) > 0 {
			if err := set_genres_tx(ctx, tx, id, entry.Genres); err != nil {
				return err
			}
		}
		return nil
	})
}

// Reset_entry_match drops one entry's match-derived state so a force rematch
// re-evaluates it from scratch. Identifiers, poster, genres, the show record,
// episode titles and pending candidates are cleared, and every file it owns is
// put back to "needs_lookup". Files, episode numbering and media type survive,
// so the media stays catalogued; only what matching added is thrown away. The
// force rematch derives title/year from the files the way a scan would, so an
// entry that no longer matches falls back to its raw name instead of the title
// of the match it just dropped.
func (s *Store) Reset_entry_match(ctx context.Context, id int64, title string, year int) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE catalog_entry SET
				title = ?, release_year = ?, imdb_id = NULL, tmdb_id = NULL,
				poster_path = NULL, updated_at = datetime('now')
			WHERE id = ?`, title, nullable_int(year), id); err != nil {
			return fmt.Errorf("reset catalog entry %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM candidate WHERE catalog_entry_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM series_metadata WHERE catalog_entry_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM catalog_entry_genre WHERE catalog_entry_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE episode SET title = NULL, updated_at = datetime('now')
			WHERE catalog_entry_id = ?`, id); err != nil {
			return err
		}
		return set_files_status_tx(ctx, tx, id, File_status_needs_lookup)
	})
}

// set_files_status_tx marks every file owned by an entry — directly or through
// one of its episodes — with the given status.
func set_files_status_tx(ctx context.Context, tx *sql.Tx, entry_id int64, status int) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE media_file SET status = ?, updated_at = datetime('now')
		WHERE catalog_entry_id = ?
		   OR episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ?)`,
		status, entry_id, entry_id)
	return err
}

// Set_entry_files_status marks every file owned by a catalog entry (directly or
// through its episodes) as matched or needs_lookup. The file status is the only
// place a title's match state is stored.
func (s *Store) Set_entry_files_status(ctx context.Context, entry_id int64, status string) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		return set_files_status_tx(ctx, tx, entry_id, File_status_code(status))
	})
}

// Delete_catalog_entry removes a catalog entry and the rows that only make
// sense with it: episodes, metadata, candidates, and genre links cascade away.
// Its media files survive, detached (catalog_entry_id and episode_id become
// NULL) and reset to needs_lookup, so the library listing keeps the files.
func (s *Store) Delete_catalog_entry(ctx context.Context, id int64) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_file SET catalog_entry_id = NULL, episode_id = NULL,
				status = ?, updated_at = datetime('now')
			WHERE catalog_entry_id = ?
			   OR episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ?)`,
			File_status_needs_lookup, id, id); err != nil {
			return fmt.Errorf("detach files of catalog entry %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM catalog_entry WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete catalog entry %d: %w", id, err)
		}
		return nil
	})
}

// Upsert_series_metadata writes the show-level record for a series entry.
func (s *Store) Upsert_series_metadata(ctx context.Context, meta Series_metadata) error {
	return s.exec_tx(ctx, `
		INSERT INTO series_metadata (catalog_entry_id, first_air_date, last_air_date, num_seasons, num_episodes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(catalog_entry_id) DO UPDATE SET
			first_air_date = excluded.first_air_date,
			last_air_date = excluded.last_air_date,
			num_seasons = excluded.num_seasons,
			num_episodes = excluded.num_episodes`,
		meta.Catalog_entry_id, nullable_string(meta.First_air_date), nullable_string(meta.Last_air_date),
		nullable_int(meta.Num_seasons), nullable_int(meta.Num_episodes))
}

// Upsert_episode inserts or updates the episode identified by its catalog
// entry, season, and episode numbers. It returns the episode id.
func (s *Store) Upsert_episode(ctx context.Context, episode Episode) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM episode
			WHERE catalog_entry_id = ? AND season_number = ? AND episode_number = ?`,
			episode.Catalog_entry_id, episode.Season_number, episode.Episode_number).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `
				INSERT INTO episode
					(catalog_entry_id, season_number, episode_number, title)
				VALUES (?, ?, ?, ?)`,
				episode.Catalog_entry_id, episode.Season_number, episode.Episode_number,
				nullable_string(episode.Title))
			if err != nil {
				return fmt.Errorf("insert episode: %w", err)
			}
			id, err = res.LastInsertId()
			return err
		case err != nil:
			return err
		default:
			_, err := tx.ExecContext(ctx, `
				UPDATE episode SET title = ?, updated_at = datetime('now')
				WHERE id = ?`,
				nullable_string(episode.Title), id)
			return err
		}
	})
	return id, err
}

// Delete_episodes removes every episode row belonging to a catalog entry. It is
// used when an entry is reclassified from series to movie, so stale offline
// episode titles are not left behind once the entry stops being a series. Files
// that pointed at those episodes are detached rather than dropped.
func (s *Store) Delete_episodes(ctx context.Context, catalog_entry_id int64) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_file SET episode_id = NULL, updated_at = datetime('now')
			WHERE episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ?)`,
			catalog_entry_id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM episode WHERE catalog_entry_id = ?", catalog_entry_id)
		return err
	})
}

// Delete_episode removes a single episode row. It is used to clean up an
// episode that lost its last file after a manual re-assignment. Files that
// pointed at it are detached rather than dropped.
func (s *Store) Delete_episode(ctx context.Context, id int64) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE media_file SET episode_id = NULL, updated_at = datetime('now')
			WHERE episode_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM episode WHERE id = ?", id)
		return err
	})
}

func set_genres_tx(ctx context.Context, tx *sql.Tx, entry_id int64, genres []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM catalog_entry_genre WHERE catalog_entry_id = ?", entry_id); err != nil {
		return err
	}
	for _, name := range genres {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO genre (name) VALUES (?)", name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO catalog_entry_genre (catalog_entry_id, genre_id)
			SELECT ?, id FROM genre WHERE name = ?`, entry_id, name); err != nil {
			return err
		}
	}
	return nil
}

func bool_to_int(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullable_string(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullable_int(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullable_float(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}
