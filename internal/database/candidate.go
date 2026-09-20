package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Candidate is one persisted automatic-match suggestion for a catalog entry
// that stayed ambiguous and is pending manual review.
type Candidate struct {
	Catalog_entry_id int64
	Tmdb_id          int
	Imdb_id          string
	Title            string
	Year             int
	Media_type       string
	Score            float64
	Overview         string
	Poster_path      string
}

// Replace_candidates overwrites an entry's shortlist atomically, so a rematch
// always leaves the stored suggestions in sync with the latest match attempt.
func (s *Store) Replace_candidates(ctx context.Context, entry_id int64, candidates []Candidate) error {
	return s.with_tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM candidate WHERE catalog_entry_id = ?", entry_id); err != nil {
			return err
		}
		for _, c := range candidates {
			c.Catalog_entry_id = entry_id
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO candidate
					(catalog_entry_id, tmdb_id, imdb_id, title, year, media_type, score, overview, poster_path)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.Catalog_entry_id, c.Tmdb_id, c.Imdb_id,
				c.Title, c.Year, c.Media_type, c.Score,
				c.Overview, c.Poster_path); err != nil {
				return fmt.Errorf("insert candidate: %w", err)
			}
		}
		return nil
	})
}

// List_candidates returns the persisted shortlist for one entry, best first.
func (s *Store) List_candidates(ctx context.Context, entry_id int64) ([]Candidate, error) {
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT catalog_entry_id, tmdb_id, imdb_id, title, year, media_type, score, overview, poster_path
		FROM candidate WHERE catalog_entry_id = ? ORDER BY score DESC, id`, entry_id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scan_candidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Clear_candidates removes a stored shortlist, used once an entry is matched.
func (s *Store) Clear_candidates(ctx context.Context, entry_id int64) error {
	return s.exec_tx(ctx, "DELETE FROM candidate WHERE catalog_entry_id = ?", entry_id)
}

// List_candidates_for_entries returns the persisted shortlists for a set of
// catalog entries, keyed by entry id, best-first within each entry.
func (s *Store) List_candidates_for_entries(ctx context.Context, entry_ids []int64) (map[int64][]Candidate, error) {
	if len(entry_ids) == 0 {
		return map[int64][]Candidate{}, nil
	}
	ids := make([]any, 0, len(entry_ids))
	placeholders := make([]string, 0, len(entry_ids))
	for _, id := range entry_ids {
		ids = append(ids, id)
		placeholders = append(placeholders, "?")
	}
	rows, err := s.db.db.QueryContext(ctx, `
		SELECT catalog_entry_id, tmdb_id, imdb_id, title, year, media_type, score, overview, poster_path
		FROM candidate WHERE catalog_entry_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY score DESC, id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by_entry := make(map[int64][]Candidate)
	for rows.Next() {
		c, err := scan_candidate(rows)
		if err != nil {
			return nil, err
		}
		by_entry[c.Catalog_entry_id] = append(by_entry[c.Catalog_entry_id], c)
	}
	return by_entry, rows.Err()
}

func scan_candidate(row interface{ Scan(...any) error }) (Candidate, error) {
	var c Candidate
	var tmdb_id, year sql.NullInt64
	var imdb_id, title, media_type, overview, poster_path sql.NullString
	var score sql.NullFloat64
	if err := row.Scan(&c.Catalog_entry_id, &tmdb_id, &imdb_id, &title, &year, &media_type, &score, &overview, &poster_path); err != nil {
		return Candidate{}, err
	}
	c.Tmdb_id = int(tmdb_id.Int64)
	c.Imdb_id = imdb_id.String
	c.Title = title.String
	c.Year = int(year.Int64)
	c.Media_type = media_type.String
	c.Score = score.Float64
	c.Overview = overview.String
	c.Poster_path = poster_path.String
	return c, nil
}
