package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Save_version inserts or updates the version row for v.File_path (unique) and
// replaces its audio and subtitle track rows. It returns the version id.
func (s *Store) Save_version(ctx context.Context, v Version) (int64, error) {
	var id int64
	err := s.with_tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM version WHERE file_path = ?", v.File_path).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx, `
				INSERT INTO version
					(catalog_entry_id, episode_id, file_path, size_bytes, mtime, duration_seconds,
					 container, resolution_width, resolution_height, resolution_label,
					 video_codec, hdr, frame_rate, bit_depth, hash)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nullable_int(int(v.Catalog_entry_id)), nullable_int(int(v.Episode_id)), v.File_path,
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

// Find_version_by_path returns the re-scan reference for a file path, if it is
// already catalogued. The returned status reflects the owning entry (or
// episode).
func (s *Store) Find_version_by_path(ctx context.Context, path string) (Version_ref, bool, error) {
	row := s.db.db.QueryRowContext(ctx, `
		SELECT v.id, COALESCE(v.catalog_entry_id, 0), COALESCE(v.episode_id, 0),
		       v.file_path, COALESCE(v.size_bytes, 0), COALESCE(v.mtime, ''),
		       COALESCE(v.hash, ''),
		       COALESCE(ce.status, ep.status, 'needs_lookup')
		FROM version v
		LEFT JOIN catalog_entry ce ON ce.id = v.catalog_entry_id
		LEFT JOIN episode ep ON ep.id = v.episode_id
		WHERE v.file_path = ?`, path)
	var ref Version_ref
	if err := row.Scan(&ref.Id, &ref.Catalog_entry_id, &ref.Episode_id,
		&ref.File_path, &ref.Size_bytes, &ref.Mtime, &ref.Hash, &ref.Status); err != nil {
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
		       v.file_path, COALESCE(v.size_bytes, 0), COALESCE(v.mtime, ''),
		       COALESCE(v.hash, ''),
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
			&ref.File_path, &ref.Size_bytes, &ref.Mtime, &ref.Hash, &ref.Status); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
