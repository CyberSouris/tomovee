package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Save_version inserts or updates the media_file row identified by its library
// and (library-relative) file path, and replaces its audio and subtitle track
// rows. It returns the file id. Files with a zero Library_id are keyed by file
// path alone (legacy absolute-path rows).
func (s *Store) Save_version(ctx context.Context, v Version) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM media_file WHERE library_id IS ? AND file_path = ?`,
			nullable_int(int(v.Library_id)), v.File_path).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `
				INSERT INTO media_file
					(catalog_entry_id, episode_id, library_id, file_path, size_bytes, mtime,
					 duration_seconds, container, resolution_width, resolution_height,
					 video_codec, hdr, frame_rate, bit_depth, hash, status)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nullable_int(int(v.Catalog_entry_id)), nullable_int(int(v.Episode_id)),
				nullable_int(int(v.Library_id)), v.File_path,
				v.Size_bytes, nullable_string(v.Mtime), nullable_float(v.Duration_seconds),
				nullable_string(v.Container), nullable_int(v.Resolution_width),
				nullable_int(v.Resolution_height),
				nullable_string(v.Video_codec), bool_to_int(v.Hdr),
				nullable_float(v.Frame_rate), nullable_int(v.Bit_depth),
				nullable_string(v.Hash), File_status_code(v.Status))
			if err != nil {
				return fmt.Errorf("insert media file: %w", err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.ExecContext(ctx, `
				UPDATE media_file SET
					catalog_entry_id = ?, episode_id = ?, size_bytes = ?, mtime = ?,
					duration_seconds = ?, container = ?, resolution_width = ?,
					resolution_height = ?, video_codec = ?,
					hdr = ?, frame_rate = ?, bit_depth = ?, status = ?, updated_at = datetime('now')
				WHERE id = ?`,
				nullable_int(int(v.Catalog_entry_id)), nullable_int(int(v.Episode_id)),
				v.Size_bytes, nullable_string(v.Mtime), nullable_float(v.Duration_seconds),
				nullable_string(v.Container), nullable_int(v.Resolution_width),
				nullable_int(v.Resolution_height),
				nullable_string(v.Video_codec), bool_to_int(v.Hdr),
				nullable_float(v.Frame_rate), nullable_int(v.Bit_depth),
				File_status_code(v.Status), id); err != nil {
				return fmt.Errorf("update media file %d: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE media_file SET hash = ? WHERE id = ? AND hash IS NULL",
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

func replace_audio_tracks_tx(ctx context.Context, tx *sql.Tx, media_file_id int64, tracks []Audio_track) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM audio_track WHERE media_file_id = ?", media_file_id); err != nil {
		return err
	}
	for _, track := range tracks {
		source := track.Source
		if source == "" {
			source = "ffprobe"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO audio_track (media_file_id, language, codec, channels, source)
			VALUES (?, ?, ?, ?, ?)`,
			media_file_id, nullable_string(track.Language), nullable_string(track.Codec),
			nullable_int(track.Channels), source); err != nil {
			return err
		}
	}
	return nil
}

func replace_subtitle_tracks_tx(ctx context.Context, tx *sql.Tx, media_file_id int64, tracks []Subtitle_track) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM subtitle_track WHERE media_file_id = ?", media_file_id); err != nil {
		return err
	}
	for _, track := range tracks {
		source := track.Source
		if source == "" {
			source = "ffprobe"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO subtitle_track (media_file_id, language, format, source)
			VALUES (?, ?, ?, ?)`,
			media_file_id, nullable_string(track.Language), nullable_string(track.Format), source); err != nil {
			return err
		}
	}
	return nil
}

// Delete_version removes a media file and its track rows (cascade). It is used
// when a scan finds that the file no longer exists on disk: without a file
// there is nothing to catalogue, so the row is dropped rather than flagged.
func (s *Store) Delete_version(ctx context.Context, id int64) error {
	if _, err := s.db.db.ExecContext(ctx, "DELETE FROM media_file WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete media file %d: %w", id, err)
	}
	return nil
}

// Delete_orphan_entries removes catalog entries that no longer have any media
// file, directly or through an episode, and returns how many were removed.
// A title exists only as long as a file backs it, so an entry left with no
// files after a scan is dropped rather than kept empty.
func (s *Store) Delete_orphan_entries(ctx context.Context) (int, error) {
	res, err := s.db.db.ExecContext(ctx, `
		DELETE FROM catalog_entry
		WHERE NOT EXISTS (
			SELECT 1 FROM media_file mf
			WHERE mf.catalog_entry_id = catalog_entry.id
			   OR mf.episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = catalog_entry.id))`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan catalog entries: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Move_version_to_entry reassigns a stored media file to a catalog entry,
// detaching it from any episode. It is used by reclassification grouping to
// fold files of a re-typed entry under one series.
func (s *Store) Move_version_to_entry(ctx context.Context, version_id, catalog_entry_id int64) error {
	if _, err := s.db.db.ExecContext(ctx, `
		UPDATE media_file SET catalog_entry_id = ?, episode_id = NULL, updated_at = datetime('now')
		WHERE id = ?`, catalog_entry_id, version_id); err != nil {
		return fmt.Errorf("move media file %d to entry %d: %w", version_id, catalog_entry_id, err)
	}
	return nil
}

// Move_version_to_episode reassigns a stored media file to an episode row,
// detaching it from its catalog entry. It is used by reclassification grouping
// when a folded movie file turns out to be a numbered episode.
func (s *Store) Move_version_to_episode(ctx context.Context, version_id, episode_id int64) error {
	if _, err := s.db.db.ExecContext(ctx, `
		UPDATE media_file SET episode_id = ?, catalog_entry_id = NULL, updated_at = datetime('now')
		WHERE id = ?`, episode_id, version_id); err != nil {
		return fmt.Errorf("move media file %d to episode %d: %w", version_id, episode_id, err)
	}
	return nil
}

// Find_version_in_library returns the re-scan reference for a file path within
// a library, if it is already catalogued. A zero library_id matches legacy
// rows stored with absolute paths. The returned status is the file's own.
func (s *Store) Find_version_in_library(ctx context.Context, library_id int64, path string) (Version_ref, bool, error) {
	row := s.db.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(catalog_entry_id, 0), COALESCE(episode_id, 0),
		       COALESCE(library_id, 0), file_path, COALESCE(size_bytes, 0),
		       COALESCE(mtime, ''), COALESCE(hash, ''), status
		FROM media_file
		WHERE library_id IS ? AND file_path = ?`, nullable_int(int(library_id)), path)
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

// List_version_refs returns every catalogued media file, used to detect files
// that disappeared since the previous scan.
func (s *Store) List_version_refs(ctx context.Context) ([]Version_ref, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT id, COALESCE(catalog_entry_id, 0), COALESCE(episode_id, 0),
		       COALESCE(library_id, 0), file_path, COALESCE(size_bytes, 0),
		       COALESCE(mtime, ''), COALESCE(hash, ''), status
		FROM media_file`)
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
// file attaches directly or through an episode. It is used by reclassify
// grouping to find the other entries living under a series' show folder.
type Owned_version struct {
	Version_id int64
	Entry_id   int64
	// Entry_media_type is the stored media type code (0 movie, 1 series).
	Entry_media_type int
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

// List_owned_versions returns every stored media file together with the catalog
// entry that owns it.
func (s *Store) List_owned_versions(ctx context.Context) ([]Owned_version, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT v.id,
		       COALESCE(v.catalog_entry_id, ep.catalog_entry_id, 0),
		       COALESCE(ce.media_type, 0),
		       COALESCE(ce.title, ''),
		       COALESCE(v.library_id, 0),
		       v.file_path,
		       COALESCE(v.mtime, ''),
		       COALESCE(v.episode_id, 0)
		FROM media_file v
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
