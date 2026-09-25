package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Save_version inserts or updates the version row identified by its library
// and (library-relative) file path, and replaces its audio and subtitle track
// rows. It returns the version id. Versions with a zero Library_id are keyed by
// file path alone (legacy absolute-path rows).
func (s *Store) Save_version(ctx context.Context, v Version) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM version WHERE library_id IS ? AND file_path = ?`,
			nullable_int(int(v.Library_id)), v.File_path).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `
				INSERT INTO version
					(catalog_entry_id, episode_id, library_id, file_path, size_bytes, mtime,
					 duration_seconds, container, resolution_width, resolution_height,
					 resolution_label, video_codec, hdr, frame_rate, bit_depth, hash)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nullable_int(int(v.Catalog_entry_id)), nullable_int(int(v.Episode_id)),
				nullable_int(int(v.Library_id)), v.File_path,
				v.Size_bytes, nullable_string(v.Mtime), nullable_float(v.Duration_seconds),
				nullable_string(v.Container), nullable_int(v.Resolution_width),
				nullable_int(v.Resolution_height), nullable_string(v.Resolution_label),
				nullable_string(v.Video_codec), bool_to_int(v.Hdr),
				nullable_float(v.Frame_rate), nullable_int(v.Bit_depth),
				nullable_string(v.Hash))
			if err != nil {
				return fmt.Errorf("insert version: %w", err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, `
				UPDATE version SET
					catalog_entry_id = ?, episode_id = ?, size_bytes = ?, mtime = ?,
					duration_seconds = ?, container = ?, resolution_width = ?,
					resolution_height = ?, resolution_label = ?, video_codec = ?,
					hdr = ?, frame_rate = ?, bit_depth = ?, updated_at = datetime('now')
				WHERE id = ?`,
				nullable_int(int(v.Catalog_entry_id)), nullable_int(int(v.Episode_id)),
				v.Size_bytes, nullable_string(v.Mtime), nullable_float(v.Duration_seconds),
				nullable_string(v.Container), nullable_int(v.Resolution_width),
				nullable_int(v.Resolution_height), nullable_string(v.Resolution_label),
				nullable_string(v.Video_codec), bool_to_int(v.Hdr),
				nullable_float(v.Frame_rate), nullable_int(v.Bit_depth), id); err != nil {
				return fmt.Errorf("update version %d: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE version SET hash = ? WHERE id = ? AND hash IS NULL",
				nullable_string(v.Hash), id); err != nil {
				return err
			}
		}

		if err := replace_audio_tracks_tx(ctx, tx, id, v.Audio); err != nil {
			return err
		}
		return replace_subtitle_tracks_tx(ctx, tx, id, v.Subtitles)
	})
	return id, err
}

func replace_audio_tracks_tx(ctx context.Context, tx *sql.Tx, version_id int64, tracks []Audio_track) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM audio_track WHERE version_id = ?", version_id); err != nil {
		return err
	}
	for _, track := range tracks {
		source := track.Source
		if source == "" {
			source = "ffprobe"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audio_track (version_id, language, codec, channels, source)
			VALUES (?, ?, ?, ?, ?)`,
			version_id, nullable_string(track.Language), nullable_string(track.Codec),
			nullable_int(track.Channels), source); err != nil {
			return err
		}
	}
	return nil
}

func replace_subtitle_tracks_tx(ctx context.Context, tx *sql.Tx, version_id int64, tracks []Subtitle_track) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM subtitle_track WHERE version_id = ?", version_id); err != nil {
		return err
	}
	for _, track := range tracks {
		source := track.Source
		if source == "" {
			source = "ffprobe"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO subtitle_track (version_id, language, format, source)
			VALUES (?, ?, ?, ?)`,
			version_id, nullable_string(track.Language), nullable_string(track.Format), source); err != nil {
			return err
		}
	}
	return nil
}

// Move_version_to_entry reassigns a stored version to a catalog entry,
// detaching it from any episode. It is used by reclassification grouping to
// fold files of a re-typed entry under one series.
func (s *Store) Move_version_to_entry(ctx context.Context, version_id, catalog_entry_id int64) error {
	if _, err := s.db.db.ExecContext(ctx, `
		UPDATE version SET catalog_entry_id = ?, episode_id = NULL, updated_at = datetime('now')
		WHERE id = ?`, catalog_entry_id, version_id); err != nil {
		return fmt.Errorf("move version %d to entry %d: %w", version_id, catalog_entry_id, err)
	}
	return nil
}

// Move_version_to_episode reassigns a stored version to an episode row,
// detaching it from its catalog entry. It is used by reclassification grouping
// when a folded movie file turns out to be a numbered episode.
func (s *Store) Move_version_to_episode(ctx context.Context, version_id, episode_id int64) error {
	if _, err := s.db.db.ExecContext(ctx, `
		UPDATE version SET episode_id = ?, catalog_entry_id = NULL, updated_at = datetime('now')
		WHERE id = ?`, episode_id, version_id); err != nil {
		return fmt.Errorf("move version %d to episode %d: %w", version_id, episode_id, err)
	}
	return nil
}

// Find_version_in_library returns the re-scan reference for a file path within
// a library, if it is already catalogued. A zero library_id matches legacy
// rows stored with absolute paths. The returned status reflects the owning
// entry (or episode).
func (s *Store) Find_version_in_library(ctx context.Context, library_id int64, path string) (Version_ref, bool, error) {
	row := s.db.db.QueryRowContext(ctx, `
		SELECT v.id, COALESCE(v.catalog_entry_id, 0), COALESCE(v.episode_id, 0),
		       COALESCE(v.library_id, 0), v.file_path, COALESCE(v.size_bytes, 0),
		       COALESCE(v.mtime, ''), COALESCE(v.hash, ''),
		       COALESCE(ce.status, ep.status, 'needs_lookup')
		FROM version v
		LEFT JOIN catalog_entry ce ON ce.id = v.catalog_entry_id
		LEFT JOIN episode ep ON ep.id = v.episode_id
		WHERE v.library_id IS ? AND v.file_path = ?`, nullable_int(int(library_id)), path)
	var ref Version_ref
	if err := row.Scan(&ref.Id, &ref.Catalog_entry_id, &ref.Episode_id,
		&ref.Library_id, &ref.File_path, &ref.Size_bytes, &ref.Mtime, &ref.Hash, &ref.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Version_ref{}, false, nil
		}
		return Version_ref{}, false, err
	}
	return ref, true, nil
}

// List_version_refs returns every catalogued version, used to detect files
// that disappeared since the previous scan.
func (s *Store) List_version_refs(ctx context.Context) ([]Version_ref, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT v.id, COALESCE(v.catalog_entry_id, 0), COALESCE(v.episode_id, 0),
		       COALESCE(v.library_id, 0), v.file_path, COALESCE(v.size_bytes, 0),
		       COALESCE(v.mtime, ''), COALESCE(v.hash, ''),
		       COALESCE(ce.status, ep.status, 'needs_lookup')
		FROM version v
		LEFT JOIN catalog_entry ce ON ce.id = v.catalog_entry_id
		LEFT JOIN episode ep ON ep.id = v.episode_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version_ref
	for rows.Next() {
		var ref Version_ref
		if err := rows.Scan(&ref.Id, &ref.Catalog_entry_id, &ref.Episode_id,
			&ref.Library_id, &ref.File_path, &ref.Size_bytes, &ref.Mtime, &ref.Hash, &ref.Status); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// Owned_version ties a stored file to the catalog entry it backs, whether the
// version attaches directly or through an episode. It is used by reclassify
// grouping to find the other entries living under a series' show folder.
type Owned_version struct {
	Version_id       int64
	Entry_id         int64
	Entry_media_type string
	Entry_title      string
	Library_id       int64
	File_path        string
	// Mtime is the file's modification time, empty when it was never recorded.
	// Reclassify numbering uses it to order files with no episode number.
	Mtime string
	// Episode_id is the episode owning the version, or 0 when the file
	// attaches to its catalog entry directly. Reclassify grouping uses it to
	// tell a show's own episode files from loose files it may renumber.
	Episode_id int64
}

// List_owned_versions returns every stored version together with the catalog
// entry that owns it.
func (s *Store) List_owned_versions(ctx context.Context) ([]Owned_version, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT v.id,
		       COALESCE(v.catalog_entry_id, ep.catalog_entry_id, 0),
		       COALESCE(ce.media_type, ''),
		       COALESCE(ce.title, ''),
		       COALESCE(v.library_id, 0),
		       v.file_path,
		       COALESCE(v.mtime, ''),
		       COALESCE(v.episode_id, 0)
		FROM version v
		LEFT JOIN episode ep ON ep.id = v.episode_id
		LEFT JOIN catalog_entry ce ON ce.id = COALESCE(v.catalog_entry_id, ep.catalog_entry_id, 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Owned_version
	for rows.Next() {
		var owned Owned_version
		if err := rows.Scan(&owned.Version_id, &owned.Entry_id, &owned.Entry_media_type,
			&owned.Entry_title, &owned.Library_id, &owned.File_path, &owned.Mtime, &owned.Episode_id); err != nil {
			return nil, err
		}
		out = append(out, owned)
	}
	return out, rows.Err()
}
