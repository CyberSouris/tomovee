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
	Statuses   []string
	Search     string
	Genre      string
	// Genres narrows to entries carrying every listed genre (an intersection),
	// unlike the single Genre field which is kept for simpler callers.
	Genres []string
	Year   int
	// Has_poster restricts to entries that have a poster path, so a caller
	// building preview thumbnails never picks an entry with no artwork.
	Has_poster bool
	Resolution string
	Language   string
	Libraries  []string
	Sort       string // "title" | "year" | "rating" | "added"
	Desc       bool
	Limit      int
	Offset     int
	Library    string
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
		       COALESCE(title, '')
		FROM episode WHERE catalog_entry_id = ?
		ORDER BY season_number, episode_number`, catalog_entry_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var episode Episode
		if err := rows.Scan(&episode.Id, &episode.Catalog_entry_id, &episode.Season_number,
			&episode.Episode_number, &episode.Title); err != nil {
			return nil, err
		}
		out = append(out, episode)
	}
	return out, rows.Err()
}

// Get_episode returns a single episode row by id, or nil when it does not
// exist.
func (s *Store) Get_episode(ctx context.Context, id int64) (*Episode, error) {
	var episode Episode
	err := s.db.db.QueryRowContext(ctx, `
		SELECT id, catalog_entry_id, COALESCE(season_number, 0), COALESCE(episode_number, 0),
		       COALESCE(title, '')
		FROM episode WHERE id = ?`, id).
		Scan(&episode.Id, &episode.Catalog_entry_id, &episode.Season_number,
			&episode.Episode_number, &episode.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &episode, nil
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

// List_versions_for_series_entry returns every version owned by a series entry,
// whether attached directly to the entry or to one of its episodes, ordered by
// descending file size. It collapses the per-episode lookups that largest_version
// and other series walks previously issued (one query per episode) into a single
// pass.
func (s *Store) List_versions_for_series_entry(ctx context.Context, catalog_entry_id int64) ([]Version, error) {
	return s.list_versions(ctx, "v.catalog_entry_id = ? OR v.episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ?)", catalog_entry_id, catalog_entry_id)
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

func (s *Store) list_versions(ctx context.Context, where string, args ...any) ([]Version, error) {
	rows, err := s.db.db.QueryContext(ctx, version_columns+" WHERE "+where+" ORDER BY v.size_bytes DESC", args...)
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
		FROM audio_track WHERE media_file_id = ? ORDER BY id`, version_id)
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
		FROM subtitle_track WHERE media_file_id = ? ORDER BY id`, version_id)
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
	SELECT ce.id, ce.media_type, ce.title,
	       COALESCE(ce.release_year, 0),
	       COALESCE(ce.imdb_id, ''), COALESCE(ce.tmdb_id, 0), COALESCE(ce.poster_path, ''),
	       EXISTS (SELECT 1 FROM media_file mf
	               WHERE mf.status = 1
	                 AND (mf.catalog_entry_id = ce.id
	                      OR mf.episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ce.id)))
	FROM catalog_entry ce`

const version_columns = `
	SELECT v.id, COALESCE(v.catalog_entry_id, 0), COALESCE(v.episode_id, 0),
	       COALESCE(v.library_id, 0), v.file_path,
	       COALESCE(v.size_bytes, 0), COALESCE(v.mtime, ''), COALESCE(v.duration_seconds, 0),
	       COALESCE(v.container, ''), COALESCE(v.resolution_width, 0), COALESCE(v.resolution_height, 0),
	       COALESCE(v.video_codec, ''), COALESCE(v.hdr, 0),
	       COALESCE(v.frame_rate, 0), COALESCE(v.bit_depth, 0), COALESCE(v.hash, ''), v.status
	FROM media_file v`

// where_builder accumulates SQL WHERE clauses and their bind arguments, one
// pair per filter dimension, so each clause helper stays a flat two-liner.
type where_builder struct {
	clauses []string
	args    []any
}

func (w *where_builder) add(clause string, args ...any) {
	w.clauses = append(w.clauses, clause)
	w.args = append(w.args, args...)
}

// string_args widens a []string into []any so it can be spread over add.
func string_args(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func add_media_type_clause(w *where_builder, filter Catalog_filter) {
	if filter.Media_type != "" {
		w.add("ce.media_type = ?", Media_type_code(filter.Media_type))
	}
}

// add_status_clause narrows to entries whose files are matched or not. Status is
// not a catalog column any more: an entry is "matched" when any of its files is,
// and "needs_lookup" when none is.
func add_status_clause(w *where_builder, filter Catalog_filter) {
	if filter.Status != "" {
		w.add(matched_exists_clause()+" = ?", bool_to_int(filter.Status == "matched"))
	}
}

func add_statuses_clause(w *where_builder, filter Catalog_filter) {
	if len(filter.Statuses) == 0 {
		return
	}
	// Both known statuses together match every entry, so the clause only bites
	// when exactly one of them is requested.
	matched := false
	needs := false
	for _, status := range filter.Statuses {
		switch status {
		case "matched":
			matched = true
		case "needs_lookup":
			needs = true
		}
	}
	if matched && !needs {
		w.add(matched_exists_clause() + " = 1")
	} else if needs && !matched {
		w.add(matched_exists_clause() + " = 0")
	}
}

// matched_exists_clause reports whether an entry owns at least one matched file,
// either directly or through one of its episodes.
func matched_exists_clause() string {
	return `EXISTS (SELECT 1 FROM media_file mf
		WHERE mf.status = 1
		  AND (mf.catalog_entry_id = ce.id
		       OR mf.episode_id IN (SELECT id FROM episode WHERE catalog_entry_id = ce.id)))`
}

func add_search_clause(w *where_builder, filter Catalog_filter) {
	if filter.Search != "" {
		w.add("ce.title LIKE ?", "%"+filter.Search+"%")
	}
}

func add_year_clause(w *where_builder, filter Catalog_filter) {
	if filter.Year > 0 {
		w.add("ce.release_year = ?", filter.Year)
	}
}

// genre_exists is the per-genre EXISTS shared by the single-genre filter and
// the genre list; it needs one bind argument per call.
func genre_exists() string {
	return `EXISTS (
		SELECT 1 FROM catalog_entry_genre ceg JOIN genre g ON g.id = ceg.genre_id
		WHERE ceg.catalog_entry_id = ce.id AND g.name = ?)`
}

func add_genre_clauses(w *where_builder, filter Catalog_filter) {
	if filter.Genre != "" {
		w.add(genre_exists(), filter.Genre)
	}
	// One EXISTS per genre means an entry must carry all of them, so browsing
	// into a second category intersects rather than widens the previous one.
	for _, genre := range filter.Genres {
		if genre == "" {
			continue
		}
		w.add(genre_exists(), genre)
	}
}

func add_has_poster_clause(w *where_builder, filter Catalog_filter) {
	if filter.Has_poster {
		w.add("ce.poster_path IS NOT NULL AND ce.poster_path != ''")
	}
}

// resolution_label_sql derives the stored file's resolution label from its
// height, the way metadata.Resolution_label does, so filters over the dropped
// resolution_label column still work. Interlaced content (the "i" suffix) is
// not distinguishable from stored data, so only the progressive labels match.
func resolution_label_sql(prefix string) string {
	return `CASE
		WHEN ` + prefix + `resolution_height >= 4320 THEN '8Kp'
		WHEN ` + prefix + `resolution_height >= 2160 THEN '4Kp'
		WHEN ` + prefix + `resolution_height >= 1440 THEN '2Kp'
		WHEN ` + prefix + `resolution_height >= 1080 THEN '1080p'
		WHEN ` + prefix + `resolution_height >= 720  THEN '720p'
		WHEN ` + prefix + `resolution_height >= 576  THEN '576p'
		WHEN ` + prefix + `resolution_height >= 480  THEN '480p'
		WHEN ` + prefix + `resolution_height > 0    THEN 'SDp'
		ELSE ''
	END`
}

func add_resolution_clause(w *where_builder, filter Catalog_filter) {
	if filter.Resolution != "" {
		w.add(`EXISTS (
			SELECT 1 FROM media_file v WHERE v.catalog_entry_id = ce.id AND `+
			resolution_label_sql("v.")+` = ?)`,
			filter.Resolution)
	}
}

func add_language_clause(w *where_builder, filter Catalog_filter) {
	if filter.Language != "" {
		w.add(`EXISTS (
			SELECT 1 FROM media_file v JOIN audio_track a ON a.media_file_id = v.id
			WHERE v.catalog_entry_id = ce.id AND a.language = ?)`,
			filter.Language)
	}
}

func add_library_clauses(w *where_builder, filter Catalog_filter) {
	if filter.Library != "" {
		w.add(`EXISTS (
			SELECT 1 FROM media_file v JOIN library lib ON lib.id = v.library_id
			WHERE v.catalog_entry_id = ce.id AND lib.name = ?)`,
			filter.Library)
	}
	if len(filter.Libraries) == 0 {
		return
	}
	placeholders := strings.Repeat("?,", len(filter.Libraries))
	placeholders = strings.TrimSuffix(placeholders, ",")
	w.add(`EXISTS (
		SELECT 1 FROM media_file v JOIN library lib ON lib.id = v.library_id
		WHERE v.catalog_entry_id = ce.id AND lib.name IN (`+placeholders+`))`,
		string_args(filter.Libraries)...)
}

func catalog_where(filter Catalog_filter) (string, []any) {
	var w where_builder
	add_media_type_clause(&w, filter)
	add_status_clause(&w, filter)
	add_statuses_clause(&w, filter)
	add_search_clause(&w, filter)
	add_year_clause(&w, filter)
	add_genre_clauses(&w, filter)
	add_has_poster_clause(&w, filter)
	add_resolution_clause(&w, filter)
	add_language_clause(&w, filter)
	add_library_clauses(&w, filter)

	if len(w.clauses) == 0 {
		return "", w.args
	}
	return " WHERE " + strings.Join(w.clauses, " AND "), w.args
}

func catalog_order(filter Catalog_filter) string {
	column := "ce.title COLLATE NOCASE"
	switch filter.Sort {
	case "year":
		column = "ce.release_year"
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
	var media_type int
	var matched int
	err := row.Scan(&entry.Id, &media_type, &entry.Title, &entry.Release_year,
		&entry.Imdb_id, &entry.Tmdb_id, &entry.Poster_path, &matched)
	if err != nil {
		return nil, err
	}
	entry.Media_type = Media_type_name(media_type)
	entry.Matched = matched == 1
	return &entry, nil
}

func scan_version(row row_scanner) (*Version, error) {
	var version Version
	var hdr int
	var status int
	err := row.Scan(&version.Id, &version.Catalog_entry_id, &version.Episode_id,
		&version.Library_id, &version.File_path,
		&version.Size_bytes, &version.Mtime, &version.Duration_seconds, &version.Container,
		&version.Resolution_width, &version.Resolution_height,
		&version.Video_codec, &hdr, &version.Frame_rate, &version.Bit_depth, &version.Hash, &status)
	if err != nil {
		return nil, err
	}
	version.Hdr = hdr == 1
	version.Status = File_status_name(status)
	version.Resolution_label = resolution_label(version.Resolution_height)
	return &version, nil
}

// resolution_label maps a frame height to a human label such as "1080p", the
// same mapping metadata.Resolution_label uses. It lives here too because the
// label is derived on read now that media_file no longer stores it.
func resolution_label(height int) string {
	switch {
	case height >= 4320:
		return "8Kp"
	case height >= 2160:
		return "4Kp"
	case height >= 1440:
		return "2Kp"
	case height >= 1080:
		return "1080p"
	case height >= 720:
		return "720p"
	case height >= 576:
		return "576p"
	case height >= 480:
		return "480p"
	case height > 0:
		return "SDp"
	}
	return ""
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

// Genre_count pairs a genre name with how many catalog entries carry it.
type Genre_count struct {
	Name  string
	Count int
}

// Genre_counts returns each genre name and how many entries of the given
// match state carry it, ordered by count descending for the browse category
// chips. A "matched" status counts entries with at least one matched file;
// "needs_lookup" counts the rest.
func (s *Store) Genre_counts(ctx context.Context, status string) ([]Genre_count, error) {
	filter := Catalog_filter{Status: status}
	where, args := catalog_where(filter)
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT g.name, COUNT(*) FROM genre g
		JOIN catalog_entry_genre ceg ON ceg.genre_id = g.id
		JOIN catalog_entry ce ON ce.id = ceg.catalog_entry_id`+where+`
		GROUP BY g.id ORDER BY COUNT(*) DESC, g.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var genres []Genre_count
	for rows.Next() {
		var genre Genre_count
		if err := rows.Scan(&genre.Name, &genre.Count); err != nil {
			return nil, err
		}
		genres = append(genres, genre)
	}
	return genres, rows.Err()
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

// Genre_counts_for_filter returns genre counts among entries matching filter.
func (s *Store) Genre_counts_for_filter(ctx context.Context, filter Catalog_filter) ([]Genre_count, error) {
	where, args := catalog_where(filter)
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT g.name, COUNT(DISTINCT ce.id) FROM genre g
		JOIN catalog_entry_genre ceg ON ceg.genre_id = g.id
		JOIN catalog_entry ce ON ce.id = ceg.catalog_entry_id`+where+`
		GROUP BY g.id ORDER BY COUNT(DISTINCT ce.id) DESC, g.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var genres []Genre_count
	for rows.Next() {
		var genre Genre_count
		if err := rows.Scan(&genre.Name, &genre.Count); err != nil {
			return nil, err
		}
		genres = append(genres, genre)
	}
	return genres, rows.Err()
}
