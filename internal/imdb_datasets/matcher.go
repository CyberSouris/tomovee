package imdb_datasets

import (
	"context"

	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/scanner"
)

// Matcher_source adapts an Index to the matcher.Offline_source interface so a
// locally loaded title.basics dataset can serve as the offline matching layer.
type Matcher_source struct {
	index *Index
}

// New_matcher_source wraps index for use with the matching pipeline.
func New_matcher_source(index *Index) *Matcher_source {
	return &Matcher_source{index: index}
}

// Search implements matcher.Offline_source by querying the in-memory index.
func (s *Matcher_source) Search(_ context.Context, query string, year int, media_type scanner.Media_type) ([]matcher.Offline_candidate, error) {
	results := s.index.Search(query, year, media_type)
	out := make([]matcher.Offline_candidate, 0, len(results))
	for _, r := range results {
		out = append(out, matcher.Offline_candidate{
			Imdb_id:    r.Id,
			Title:      r.Title,
			Year:       r.Year,
			Media_type: r.Media_type,
		})
	}
	return out, nil
}

// Search_local implements matcher.Local_search_source by serving fuzzy prefix
// autocomplete results from the local title index.
func (s *Matcher_source) Search_local(_ context.Context, query string, year int, media_type scanner.Media_type, limit int) ([]matcher.Offline_candidate, error) {
	results := s.index.Search_autocomplete(query, year, media_type, limit)
	out := make([]matcher.Offline_candidate, 0, len(results))
	for _, r := range results {
		out = append(out, matcher.Offline_candidate{
			Imdb_id:    r.Id,
			Title:      r.Title,
			Year:       r.Year,
			Media_type: r.Media_type,
		})
	}
	return out, nil
}

// Lookup implements matcher.Offline_resolver so a manual candidate pick can be
// applied purely from the local index, with no TMDB key required.
func (s *Matcher_source) Lookup(_ context.Context, imdb_id string) (*matcher.Result, bool) {
	title, ok := s.index.Lookup(imdb_id)
	if !ok {
		return nil, false
	}
	media_type, ok := media_type_of(title.Title_type)
	if !ok {
		return nil, false
	}
	result := &matcher.Result{
		Matched:         true,
		Confidence:      1,
		Source:          "imdb-datasets",
		Media_type:      media_type,
		Imdb_id:         title.Id,
		Title:           title.Primary_title,
		Original_title:  title.Original_title,
		Year:            title.Start_year,
		Runtime_minutes: title.Runtime_minutes,
		Genres:          split_genres(title.Genres),
	}
	if rating, ok := s.index.Rating(imdb_id); ok {
		result.Rating = rating.Average_rating
		result.Vote_count = rating.Num_votes
	}
	return result, true
}
