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
	args := []any{entry.Media_type, entry.Title}
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
			(media_type, title, original_title, release_year, overview, runtime_minutes,
			 rating, vote_count, imdb_id, tmdb_id, poster_path, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Media_type, entry.Title, entry.Original_title, nullable_int(entry.Release_year),
		nullable_string(entry.Overview), nullable_int(entry.Runtime_minutes),
		nullable_float(entry.Rating), nullable_int(entry.Vote_count),
		nullable_string(entry.Imdb_id), nullable_int(entry.Tmdb_id),
		nullable_string(entry.Poster_path), status_or_default(entry.Status))
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
			media_type = ?, title = ?, original_title = ?, release_year = ?,
			overview = ?, runtime_minutes = ?, rating = ?, vote_count = ?,
			imdb_id = ?, tmdb_id = ?, poster_path = ?, status = ?,
			updated_at = datetime('now')
		WHERE id = ?`,
		entry.Media_type, entry.Title, entry.Original_title, nullable_int(entry.Release_year),
		nullable_string(entry.Overview), nullable_int(entry.Runtime_minutes),
		nullable_float(entry.Rating), nullable_int(entry.Vote_count),
		nullable_string(entry.Imdb_id), nullable_int(entry.Tmdb_id),
		nullable_string(entry.Poster_path), status_or_default(entry.Status), id)
	if err != nil {
		return fmt.Errorf("update catalog entry %d: %w", id, err)
	}
	return nil
}

// Update_catalog_entry overwrites the metadata of an existing entry without
// re-grouping, preserving its versions. It is used by manual re-matching.
func (s *Store) Update_catalog_entry(ctx context.Context, id int64, entry Catalog_entry) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if err := update_entry_tx(ctx, tx, id, entry); err != nil {
			return err
		}
		if len(entry.Genres) > 0 {
			return set_genres_tx(ctx, tx, id, entry.Genres)
		}
		return nil
	})
}

// Set_catalog_status updates the status flag of one catalog entry.
func (s *Store) Set_catalog_status(ctx context.Context, id int64, status string) error {
	return s.exec_tx(ctx,
		"UPDATE catalog_entry SET status = ?, updated_at = datetime('now') WHERE id = ?", status, id)
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
					(catalog_entry_id, season_number, episode_number, title, overview, airdate, is_special, status)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				episode.Catalog_entry_id, episode.Season_number, episode.Episode_number,
				nullable_string(episode.Title), nullable_string(episode.Overview),
				nullable_string(episode.Airdate), bool_to_int(episode.Is_special),
				status_or_default(episode.Status))
			if err != nil {
				return fmt.Errorf("insert episode: %w", err)
			}
			id, err = res.LastInsertId()
			return err
		case err != nil:
			return err
		default:
			_, err := tx.ExecContext(ctx, `
				UPDATE episode SET title = ?, overview = ?, airdate = ?, is_special = ?, status = ?,
					updated_at = datetime('now')
				WHERE id = ?`,
				nullable_string(episode.Title), nullable_string(episode.Overview),
				nullable_string(episode.Airdate), bool_to_int(episode.Is_special),
				status_or_default(episode.Status), id)
			return err
		}
	})
	return id, err
}

// Set_episode_status updates the status flag of one episode.
func (s *Store) Set_episode_status(ctx context.Context, id int64, status string) error {
	return s.exec_tx(ctx,
		"UPDATE episode SET status = ?, updated_at = datetime('now') WHERE id = ?", status, id)
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

func status_or_default(status string) string {
	if status == "" {
		return "needs_lookup"
	}
	return status
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
