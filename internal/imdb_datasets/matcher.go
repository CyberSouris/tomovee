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
