package imdb_datasets

import (
	"database/sql"
	"strings"
	"unicode"

	"github.com/cybersouris/tomovee/internal/scanner"
)

// Result is one offline match produced by Index.Search.
type Result struct {
	Id              string
	Title           string
	Original_title  string
	Year            int
	End_year        int
	Media_type      scanner.Media_type
	Runtime_minutes int
	Genres          []string
	Rating          float64
	Votes           int
}

// Episode_ref links an episode's tconst to its position within a series.
type Episode_ref struct {
	Id         string
	Season     int
	Episode    int
	Is_special bool
}

// Episode_title is one offline episode of a parent series joined to its
// title.basics record. It is the batch counterpart of Episode_ref plus a
// Lookup: Episodes_titles fills these in a single query for a whole series.
type Episode_title struct {
	Season        int
	Episode       int
	Primary_title string
	Start_year    int
	Is_special    bool
}

// Index serves exact-match lookups over the IMDb datasets. It is backed by an
// indexed SQLite database (an in-memory one for programmatically built
// indexes, or a file written next to the dataset exports), so only the rows
// actually queried are ever held in RAM.
type Index struct {
	db *sql.DB
}

// New_index builds an in-memory index from parsed title.basics records. The
// returned index is otherwise identical to one built by Open.
func New_index(titles []Title) *Index {
	db, err := open_memory_db()
	if err != nil || create_schema(db) != nil {
		return &Index{}
	}
	idx := &Index{db: db}
	idx.insert_titles(titles)
	_ = populate_fts(db)
	return idx
}

// Index_akas adds the alternative titles from title.akas to the search index.
// Rows that are just the original title are skipped; the deduplication in
// Search collapses any remaining overlaps.
func (idx *Index) Index_akas(akas []Akas) {
	if idx.db == nil {
		return
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare("INSERT INTO title_aka (key, title_id) VALUES (?, ?)")
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, aka := range akas {
		if aka.Id == "" || aka.Is_original {
			continue
		}
		key := Normalize_title(aka.Title)
		if key == "" {
			continue
		}
		if _, err := stmt.Exec(key, aka.Id); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// Index_episodes adds the parent → episode mapping from title.episode.
func (idx *Index) Index_episodes(episodes []Episode) {
	if idx.db == nil {
		return
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare("INSERT INTO title_episode (id, parent_id, season, episode) VALUES (?, ?, ?, ?)")
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, ep := range episodes {
		if ep.Id == "" || ep.Parent_id == "" {
			continue
		}
		if _, err := stmt.Exec(ep.Id, ep.Parent_id, ep.Season, ep.Episode); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// Index_ratings adds the title.ratings data.
func (idx *Index) Index_ratings(ratings []Rating) {
	if idx.db == nil {
		return
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare("INSERT INTO title_rating (id, average_rating, num_votes) VALUES (?, ?, ?)")
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, rating := range ratings {
		if rating.Id == "" {
			continue
		}
		if _, err := stmt.Exec(rating.Id, rating.Average_rating, rating.Num_votes); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

func (idx *Index) insert_titles(titles []Title) {
	if idx.db == nil {
		return
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`
		INSERT INTO title (id, title_type, primary_title, original_title, pri_key, orig_key,
		                   start_year, end_year, runtime_minutes, genres)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, title := range titles {
		var orig_key any
		if title.Original_title != "" && title.Original_title != title.Primary_title {
			orig_key = Normalize_title(title.Original_title)
		}
		if _, err := stmt.Exec(title.Id, title.Title_type, title.Primary_title, title.Original_title,
			Normalize_title(title.Primary_title), orig_key, title.Start_year, title.End_year,
			title.Runtime_minutes, title.Genres); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// Close releases the underlying database handle. It is a no-op on indexes
// built by New_index.
func (idx *Index) Close() error {
	if idx.db == nil {
		return nil
	}
	return idx.db.Close()
}

const title_columns = `
	SELECT t.id, t.title_type, t.primary_title, t.original_title,
	       t.start_year, t.end_year, t.runtime_minutes, t.genres,
	       COALESCE(r.average_rating, 0), COALESCE(r.num_votes, 0)
	FROM title t LEFT JOIN title_rating r ON r.id = t.id`

// Count returns how many catalogued titles the index holds.
func (idx *Index) Count() int {
	if idx.db == nil {
		return 0
	}
	var n int
	_ = idx.db.QueryRow(`
		SELECT COUNT(*) FROM title
		WHERE title_type IN ('movie', 'tvMovie', 'tvSpecial', 'tvSeries', 'tvMiniSeries')`).Scan(&n)
	return n
}

// Lookup returns the title record for an IMDb id (tconst form), if present.
func (idx *Index) Lookup(id string) (*Title, bool) {
	if idx.db == nil {
		return nil, false
	}
	row, err := scan_title(idx.db.QueryRow(title_columns+" WHERE t.id = ?", id))
	if err != nil {
		return nil, false
	}
	return &row.Title, true
}

// Rating returns the ratings record for an IMDb id, if present.
func (idx *Index) Rating(id string) (Rating, bool) {
	if idx.db == nil {
		return Rating{}, false
	}
	var rating Rating
	err := idx.db.QueryRow(`
		SELECT COALESCE(average_rating, 0), COALESCE(num_votes, 0)
		FROM title_rating WHERE id = ?`, id).Scan(&rating.Average_rating, &rating.Num_votes)
	if err != nil {
		return Rating{}, false
	}
	return rating, true
}

// Episodes returns the episode references of a series in dataset order.
func (idx *Index) Episodes(parent string) []Episode_ref {
	if idx.db == nil {
		return nil
	}
	rows, err := idx.db.Query(`
		SELECT id, season, episode FROM title_episode
		WHERE parent_id = ? ORDER BY rowid`, parent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Episode_ref
	for rows.Next() {
		var ref Episode_ref
		if err := rows.Scan(&ref.Id, &ref.Season, &ref.Episode); err != nil {
			return out
		}
		ref.Is_special = ref.Season == 0
		out = append(out, ref)
	}
	return out
}

// Episode_lookup returns the episode reference for a series at the given
// season and episode numbers, if the dataset has it.
func (idx *Index) Episode_lookup(parent string, season, episode int) (Episode_ref, bool) {
	if idx.db == nil {
		return Episode_ref{}, false
	}
	var ref Episode_ref
	err := idx.db.QueryRow(`
		SELECT id, season, episode FROM title_episode
		WHERE parent_id = ? AND season = ? AND episode = ?
		LIMIT 1`, parent, season, episode).Scan(&ref.Id, &ref.Season, &ref.Episode)
	if err != nil {
		return Episode_ref{}, false
	}
	ref.Is_special = ref.Season == 0
	return ref, true
}

// Episodes_titles resolves every episode of a series in a single query. It
// returns one entry per offline title_episode row joined to its title.basics
// record, so enrichment can fill the episode titles of a whole catalog series
// without issuing a point lookup per episode.
func (idx *Index) Episodes_titles(parent string) []Episode_title {
	if idx.db == nil {
		return nil
	}
	rows, err := idx.db.Query(`
		SELECT e.season, e.episode, t.primary_title, t.start_year
		FROM title_episode e
		LEFT JOIN title t ON t.id = e.id
		WHERE e.parent_id = ?
		ORDER BY e.rowid`, parent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Episode_title
	for rows.Next() {
		var et Episode_title
		var primary sql.NullString
		if err := rows.Scan(&et.Season, &et.Episode, &primary, &et.Start_year); err != nil {
			return out
		}
		et.Primary_title = primary.String
		et.Is_special = et.Season == 0
		out = append(out, et)
	}
	return out
}

// Search returns the titles whose normalized primary, original, or alternative
// title equals the normalized query. Results are restricted to the requested
// media type when want is Movie or Series. A non-zero year filters nothing on
// its own; callers rank matches by year closeness.
func (idx *Index) Search(query string, year int, want scanner.Media_type) []Result {
	key := Normalize_title(query)
	if key == "" || idx.db == nil {
		return nil
	}
	rows, err := idx.db.Query(`
		SELECT DISTINCT t.id, t.title_type, t.primary_title, t.original_title,
		       t.start_year, t.end_year, t.runtime_minutes, t.genres,
		       COALESCE(r.average_rating, 0), COALESCE(r.num_votes, 0)
		FROM title t
		LEFT JOIN title_rating r ON r.id = t.id
		WHERE t.pri_key = ?
		   OR (t.orig_key IS NOT NULL AND t.orig_key = ?)
		   OR EXISTS (SELECT 1 FROM title_aka a WHERE a.title_id = t.id AND a.key = ?)`,
		key, key, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := make(map[string]bool)
	var out []Result
	for rows.Next() {
		title, err := scan_title(rows)
		if err != nil {
			return out
		}
		media_type, ok := media_type_of(title.Title_type)
		if !ok || seen[title.Id] {
			continue
		}
		if want == scanner.Movie && media_type != scanner.Movie {
			continue
		}
		if want == scanner.Series && media_type != scanner.Series {
			continue
		}
		seen[title.Id] = true
		out = append(out, Result{
			Id:              title.Id,
			Title:           title.Primary_title,
			Original_title:  title.Original_title,
			Year:            title.Start_year,
			End_year:        title.End_year,
			Media_type:      media_type,
			Runtime_minutes: title.Runtime_minutes,
			Genres:          split_genres(title.Genres),
			Rating:          title.Average_rating,
			Votes:           title.Num_votes,
		})
	}
	return out
}

// Search_autocomplete performs a fuzzy prefix search over the normalized
// primary and original titles, for the manual-match autocomplete backed by the
// local IMDb data. Every token of the query is required, but the final token
// is matched as a prefix, so typing "matrix relo" finds "The Matrix
// Reloaded". Results are ranked by bm25 relevance then vote count and
// restricted to the requested media type when want is Movie or Series.
func (idx *Index) Search_autocomplete(query string, year int, want scanner.Media_type, limit int) []Result {
	if idx.db == nil {
		return nil
	}
	match := fts_match_query(Normalize_title(query))
	if match == "" {
		return nil
	}
	want_types := `'movie', 'tvMovie', 'tvSpecial', 'tvSeries', 'tvMiniSeries'`
	switch want {
	case scanner.Movie:
		want_types = `'movie', 'tvMovie', 'tvSpecial'`
	case scanner.Series:
		want_types = `'tvSeries', 'tvMiniSeries'`
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := idx.db.Query(`
		SELECT DISTINCT t.id, t.title_type, t.primary_title, t.original_title,
		       t.start_year, t.end_year, t.runtime_minutes, t.genres,
		       COALESCE(r.average_rating, 0), COALESCE(r.num_votes, 0)
		FROM title_fts f
		JOIN title t ON t.id = f.id
		LEFT JOIN title_rating r ON r.id = t.id
		WHERE title_fts MATCH ?
		  AND t.title_type IN (`+want_types+`)
		  AND (? = 0 OR t.start_year = ? OR t.end_year = ?)
		ORDER BY bm25(title_fts), r.num_votes DESC
		LIMIT ?`,
		match, year, year, year, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := make(map[string]bool)
	var out []Result
	for rows.Next() {
		title, err := scan_title(rows)
		if err != nil {
			return out
		}
		if seen[title.Id] {
			continue
		}
		seen[title.Id] = true
		out = append(out, Result{
			Id:              title.Id,
			Title:           title.Primary_title,
			Original_title:  title.Original_title,
			Year:            title.Start_year,
			End_year:        title.End_year,
			Media_type:      media_type_of_result(title.Title_type),
			Runtime_minutes: title.Runtime_minutes,
			Genres:          split_genres(title.Genres),
			Rating:          title.Average_rating,
			Votes:           title.Num_votes,
		})
	}
	return out
}

// fts_match_query turns a normalized title into an FTS5 MATCH expression.
// Tokens are already lowercase alphanumeric (see Normalize_title), so they are
// safe as bare query terms; the last one is turned into a prefix match so the
// search completes as the user types.
func fts_match_query(normalized string) string {
	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for i, token := range tokens {
		if i == len(tokens)-1 {
			parts = append(parts, token+"*")
		} else {
			parts = append(parts, token)
		}
	}
	return strings.Join(parts, " AND ")
}

func media_type_of_result(title_type string) scanner.Media_type {
	media_type, _ := media_type_of(title_type)
	return media_type
}

// Normalize_title lower-cases and reduces a title to space-separated
// alphanumeric tokens, matching how filenames are normalized during matching.
func Normalize_title(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func split_genres(raw string) []string {
	raw = clean_null(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// title_row adds the optional pieces (rating, votes) carried by title_columns.
type title_row struct {
	Title
	Average_rating float64
	Num_votes      int
}

type row_scanner interface {
	Scan(dest ...any) error
}

func scan_title(row row_scanner) (*title_row, error) {
	var title title_row
	err := row.Scan(&title.Id, &title.Title_type, &title.Primary_title, &title.Original_title,
		&title.Start_year, &title.End_year, &title.Runtime_minutes, &title.Genres,
		&title.Average_rating, &title.Num_votes)
	if err != nil {
		return nil, err
	}
	return &title, nil
}
