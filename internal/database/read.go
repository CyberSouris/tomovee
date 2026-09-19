package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Catalog_filter narrows a catalog listing. Zero values mean "no filter".
type Catalog_filter struct {
	Media_type string
	Status     string
	Search     string
	Genre      string
	Year       int
	Resolution string
	Language   string
	Sort       string // "title" | "year" | "rating" | "added"
	Desc       bool
	Limit      int
	Offset     int
}

// Get_catalog_entry returns one entry with its genres, or (nil, nil) when the
// id does not exist.
func (s *Store) Get_catalog_entry(ctx context.Context, id int64) (*Catalog_entry, error) {
	row := s.db.db.QueryRowContext(ctx, catalog_columns+" WHERE ce.id = ?", id)
	entry, err := scan_catalog_entry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries := []Catalog_entry{*entry}
	if err := s.attach_genres(ctx, entries); err != nil {
		return nil, err
	}
	return &entries[0], nil
}

// List_catalog_entries returns catalog entries matching filter, with genres
// attached.
func (s *Store) List_catalog_entries(ctx context.Context, filter Catalog_filter) ([]Catalog_entry, error) {
	where, args := catalog_where(filter)
	query := catalog_columns + where + catalog_order(filter)
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			query += " OFFSET ?"
			args = append(args, filter.Offset)
		}
	}
	rows, err := s.db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Catalog_entry
	for rows.Next() {
		entry, err := scan_catalog_entry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attach_genres(ctx, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Count_catalog_entries returns how many entries match filter, ignoring any
// limit or offset. It is the pagination total for List_catalog_entries.
func (s *Store) Count_catalog_entries(ctx context.Context, filter Catalog_filter) (int, error) {
	where, args := catalog_where(filter)
	var n int
	err := s.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM catalog_entry ce"+where, args...).Scan(&n)
	return n, err
}

// List_episodes returns the episodes of a series entry in broadcast order.
func (s *Store) List_episodes(ctx context.Context, catalog_entry_id int64) ([]Episode, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT id, catalog_entry_id, COALESCE(season_number, 0), COALESCE(episode_number, 0),
		       COALESCE(title, ''), COALESCE(overview, ''), COALESCE(airdate, ''),
		       is_special, status
		FROM episode WHERE catalog_entry_id = ?
		ORDER BY season_number, episode_number`, catalog_entry_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var episode Episode
		var special int
		if err := rows.Scan(&episode.Id, &episode.Catalog_entry_id, &episode.Season_number,
			&episode.Episode_number, &episode.Title, &episode.Overview, &episode.Airdate,
			&special, &episode.Status); err != nil {
			return nil, err
		}
		episode.Is_special = special == 1
		out = append(out, episode)
	}
	return out, rows.Err()
}

// Get_series_metadata returns the show-level record, or (nil, nil) if absent.
func (s *Store) Get_series_metadata(ctx context.Context, catalog_entry_id int64) (*Series_metadata, error) {
	var meta Series_metadata
	err := s.db.db.QueryRowContext(ctx, `
		SELECT catalog_entry_id, COALESCE(first_air_date, ''), COALESCE(last_air_date, ''),
		       COALESCE(num_seasons, 0), COALESCE(num_episodes, 0)
		FROM series_metadata WHERE catalog_entry_id = ?`, catalog_entry_id).
		Scan(&meta.Catalog_entry_id, &meta.First_air_date, &meta.Last_air_date, &meta.Num_seasons, &meta.Num_episodes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// List_versions_for_entry returns the versions attached to a catalog entry.
func (s *Store) List_versions_for_entry(ctx context.Context, catalog_entry_id int64) ([]Version, error) {
	return s.list_versions(ctx, "v.catalog_entry_id = ?", catalog_entry_id)
}

// List_versions_for_episode returns the versions attached to an episode.
func (s *Store) List_versions_for_episode(ctx context.Context, episode_id int64) ([]Version, error) {
	return s.list_versions(ctx, "v.episode_id = ?", episode_id)
}

// Get_version returns a single version row by id, or nil when it does not
// exist.
func (s *Store) Get_version(ctx context.Context, id int64) (*Version, error) {
	version, err := scan_version(s.db.db.QueryRowContext(ctx, version_columns+" WHERE v.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return version, nil
}

func (s *Store) list_versions(ctx context.Context, where string, arg any) ([]Version, error) {
	rows, err := s.db.db.QueryContext(ctx, version_columns+" WHERE "+where+" ORDER BY v.size_bytes DESC", arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		version, err := scan_version(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *version)
	}
	return out, rows.Err()
}

// Version_tracks returns the audio and subtitle tracks of one version.
func (s *Store) Version_tracks(ctx context.Context, version_id int64) ([]Audio_track, []Subtitle_track, error) {
	audio, err := s.list_audio_tracks(ctx, version_id)
	if err != nil {
		return nil, nil, err
	}
	subtitles, err := s.list_subtitle_tracks(ctx, version_id)
	if err != nil {
		return nil, nil, err
	}
	return audio, subtitles, nil
}

func (s *Store) list_audio_tracks(ctx context.Context, version_id int64) ([]Audio_track, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT COALESCE(language, ''), COALESCE(codec, ''), COALESCE(channels, 0), source
		FROM audio_track WHERE version_id = ? ORDER BY id`, version_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audio_track
	for rows.Next() {
		var track Audio_track
		if err := rows.Scan(&track.Language, &track.Codec, &track.Channels, &track.Source); err != nil {
			return nil, err
		}
		out = append(out, track)
	}
	return out, rows.Err()
}

func (s *Store) list_subtitle_tracks(ctx context.Context, version_id int64) ([]Subtitle_track, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT COALESCE(language, ''), COALESCE(format, ''), source
		FROM subtitle_track WHERE version_id = ? ORDER BY id`, version_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subtitle_track
	for rows.Next() {
		var track Subtitle_track
		if err := rows.Scan(&track.Language, &track.Format, &track.Source); err != nil {
			return nil, err
		}
		out = append(out, track)
	}
	return out, rows.Err()
}

const catalog_columns = `
	SELECT ce.id, ce.media_type, ce.title, COALESCE(ce.original_title, ''),
	       COALESCE(ce.release_year, 0), COALESCE(ce.overview, ''),
	       COALESCE(ce.runtime_minutes, 0), COALESCE(ce.rating, 0), COALESCE(ce.vote_count, 0),
	       COALESCE(ce.imdb_id, ''), COALESCE(ce.tmdb_id, 0), COALESCE(ce.poster_path, ''),
	       ce.status
	FROM catalog_entry ce`

const version_columns = `
	SELECT v.id, COALESCE(v.catalog_entry_id, 0), COALESCE(v.episode_id, 0),
	       COALESCE(v.library_id, 0), v.file_path,
	       COALESCE(v.size_bytes, 0), COALESCE(v.mtime, ''), COALESCE(v.duration_seconds, 0),
	       COALESCE(v.container, ''), COALESCE(v.resolution_width, 0), COALESCE(v.resolution_height, 0),
	       COALESCE(v.resolution_label, ''), COALESCE(v.video_codec, ''), COALESCE(v.hdr, 0),
	       COALESCE(v.frame_rate, 0), COALESCE(v.bit_depth, 0), COALESCE(v.hash, '')
	FROM version v`

func catalog_where(filter Catalog_filter) (string, []any) {
	var clauses []string
	var args []any
	if filter.Media_type != "" {
		clauses = append(clauses, "ce.media_type = ?")
		args = append(args, filter.Media_type)
	}
	if filter.Status != "" {
		clauses = append(clauses, "ce.status = ?")
		args = append(args, filter.Status)
	}
	if filter.Search != "" {
		clauses = append(clauses, "(ce.title LIKE ? OR ce.original_title LIKE ?)")
		like := "%" + filter.Search + "%"
		args = append(args, like, like)
	}
	if filter.Year > 0 {
		clauses = append(clauses, "ce.release_year = ?")
		args = append(args, filter.Year)
	}
	if filter.Genre != "" {
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM catalog_entry_genre ceg JOIN genre g ON g.id = ceg.genre_id
			WHERE ceg.catalog_entry_id = ce.id AND g.name = ?)`)
		args = append(args, filter.Genre)
	}
	if filter.Resolution != "" {
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM version v WHERE v.catalog_entry_id = ce.id AND v.resolution_label = ?)`)
		args = append(args, filter.Resolution)
	}
	if filter.Language != "" {
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM version v JOIN audio_track a ON a.version_id = v.id
			WHERE v.catalog_entry_id = ce.id AND a.language = ?)`)
		args = append(args, filter.Language)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func catalog_order(filter Catalog_filter) string {
	column := "ce.title COLLATE NOCASE"
	switch filter.Sort {
	case "year":
		column = "ce.release_year"
	case "rating":
		column = "ce.rating"
	case "added":
		column = "ce.created_at"
	}
	direction := "ASC"
	if filter.Desc {
		direction = "DESC"
	}
	return fmt.Sprintf(" ORDER BY %s %s, ce.id ASC", column, direction)
}

type row_scanner interface {
	Scan(dest ...any) error
}

func scan_catalog_entry(row row_scanner) (*Catalog_entry, error) {
	var entry Catalog_entry
	err := row.Scan(&entry.Id, &entry.Media_type, &entry.Title, &entry.Original_title,
		&entry.Release_year, &entry.Overview, &entry.Runtime_minutes, &entry.Rating,
		&entry.Vote_count, &entry.Imdb_id, &entry.Tmdb_id, &entry.Poster_path, &entry.Status)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func scan_version(row row_scanner) (*Version, error) {
	var version Version
	var hdr int
	err := row.Scan(&version.Id, &version.Catalog_entry_id, &version.Episode_id,
		&version.Library_id, &version.File_path,
		&version.Size_bytes, &version.Mtime, &version.Duration_seconds, &version.Container,
		&version.Resolution_width, &version.Resolution_height, &version.Resolution_label,
		&version.Video_codec, &hdr, &version.Frame_rate, &version.Bit_depth, &version.Hash)
	if err != nil {
		return nil, err
	}
	version.Hdr = hdr == 1
	return &version, nil
}

func (s *Store) attach_genres(ctx context.Context, entries []Catalog_entry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]any, 0, len(entries))
	placeholders := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.Id)
		placeholders = append(placeholders, "?")
	}
	query := `SELECT ceg.catalog_entry_id, g.name
		FROM catalog_entry_genre ceg JOIN genre g ON g.id = ceg.genre_id
		WHERE ceg.catalog_entry_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY g.name`
	rows, err := s.db.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	by_entry := make(map[int64][]string)
	for rows.Next() {
		var entry_id int64
		var name string
		if err := rows.Scan(&entry_id, &name); err != nil {
			return err
		}
		by_entry[entry_id] = append(by_entry[entry_id], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range entries {
		entries[i].Genres = by_entry[entries[i].Id]
	}
	return nil
}

// List_poster_refs returns the ids of catalog entries that have a poster path.
// It is used by the poster cache to determine which images to keep.
func (s *Store) List_poster_refs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.db.QueryContext(ctx,
		"SELECT id FROM catalog_entry WHERE poster_path IS NOT NULL AND poster_path != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		refs[id] = true
	}
	return refs, rows.Err()
}
