package matcher

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/cybersouris/tomovee/internal/opensubtitles"
	"github.com/cybersouris/tomovee/internal/scanner"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

const (
	// confidence_hash is assigned to content-hash matches, which are highly
	// reliable.
	confidence_hash = 0.95
	// default_min_confidence is the threshold a search result must reach to be
	// accepted without manual review.
	default_min_confidence = 0.6
	// max_candidates is how many alternatives are surfaced for manual matching.
	max_candidates = 5
)

// Subtitles_source looks up features by OpenSubtitles file hash.
type Subtitles_source interface {
	Search_by_hash(ctx context.Context, hash string) ([]opensubtitles.Feature, error)
}

// Metadata_source provides TMDB search and detail lookups.
type Metadata_source interface {
	Search_movie(ctx context.Context, query string, year int) ([]tmdb.Movie_search_result, error)
	Search_tv(ctx context.Context, query string, year int) ([]tmdb.Tv_search_result, error)
	Movie_details(ctx context.Context, id int) (*tmdb.Movie_details, error)
	Tv_details(ctx context.Context, id int) (*tmdb.Tv_details, error)
	Find_by_imdb(ctx context.Context, imdb_id string) (*tmdb.Find_result, error)
}

// Offline_source supplies candidate matches from a local dataset (IMDb
// datasets) when online services are unreachable or inconclusive. It is the
// third layer of the matching pipeline.
type Offline_source interface {
	Search(ctx context.Context, query string, year int, media_type scanner.Media_type) ([]Offline_candidate, error)
}

// Offline_candidate is one local-dataset match.
type Offline_candidate struct {
	Imdb_id    string
	Title      string
	Year       int
	Media_type scanner.Media_type
}

// Input describes a single file to identify.
type Input struct {
	Path      string
	File_name string
	Hash      string
	Kind      scanner.Media_type
}

// Candidate is one possible match, kept for manual review.
type Candidate struct {
	Source      string
	Tmdb_id     int
	Imdb_id     string
	Title       string
	Year        int
	Score       float64
	Overview    string
	Poster_path string
}

// Result is the outcome of matching a single file.
type Result struct {
	Matched            bool
	Confidence         float64
	Source             string
	Media_type         scanner.Media_type
	Tmdb_id            int
	Imdb_id            string
	Title              string
	Original_title     string
	Year               int
	Overview           string
	Rating             float64
	Vote_count         int
	Genres             []string
	Runtime_minutes    int
	Poster_path        string
	Number_of_seasons  int
	Number_of_episodes int
	First_air_date     string
	Last_air_date      string
	Candidates         []Candidate
	Warnings           []string
}

// Options configures a Matcher.
type Options struct {
	Subtitles      Subtitles_source
	Metadata       Metadata_source
	Offline        Offline_source
	Min_confidence float64
	Logger         *slog.Logger
}

// Matcher runs the matching pipeline: OpenSubtitles hash first, then a TMDB
// title search, then the offline IMDb dataset, leaving unmatched files for
// manual review.
type Matcher struct {
	subtitles      Subtitles_source
	metadata       Metadata_source
	offline        Offline_source
	min_confidence float64
	logger         *slog.Logger
}

// New builds a Matcher from opts.
func New(opts Options) *Matcher {
	min_confidence := opts.Min_confidence
	if min_confidence <= 0 {
		min_confidence = default_min_confidence
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Matcher{
		subtitles:      opts.Subtitles,
		metadata:       opts.Metadata,
		offline:        opts.Offline,
		min_confidence: min_confidence,
		logger:         logger,
	}
}

// Match identifies a file, always returning a result. A result with
// Matched == false carries the parsed title/year and any candidates for manual
// matching.
func (m *Matcher) Match(ctx context.Context, input Input) *Result {
	hint := Parse_filename(input.File_name)
	result := &Result{Media_type: input.Kind, Title: hint.Title, Year: hint.Year}

	if input.Hash != "" && m.subtitles != nil {
		hash_result, err := m.match_by_hash(ctx, input, hint)
		if err != nil {
			result.Warnings = append(result.Warnings, "opensubtitles hash lookup: "+err.Error())
		} else if hash_result != nil {
			return hash_result
		}
	}

	if m.metadata != nil {
		search_result := m.match_by_search(ctx, input, hint)
		search_result.Warnings = append(search_result.Warnings, result.Warnings...)
		if search_result.Matched {
			return search_result
		}
		result = search_result
	}
	if m.offline != nil {
		m.match_offline(ctx, input, hint, result)
	}
	if !result.Matched && len(result.Warnings) == 0 && m.metadata == nil && m.offline == nil {
		result.Warnings = append(result.Warnings, "no metadata source configured")
	}
	return result
}

func (m *Matcher) match_by_hash(ctx context.Context, input Input, hint Filename_hint) (*Result, error) {
	features, err := m.subtitles.Search_by_hash(ctx, input.Hash)
	if err != nil {
		return nil, err
	}
	feature, ok := pick_feature(features, input.Kind)
	if !ok {
		return nil, nil
	}

	result := &Result{
		Matched:    true,
		Confidence: confidence_hash,
		Source:     "opensubtitles",
		Media_type: media_type_for_feature(feature),
		Tmdb_id:    feature.Tmdb_id,
		Imdb_id:    feature.Imdb_id,
		Title:      feature.Title,
		Year:       feature.Year,
	}
	if result.Title == "" {
		result.Title = hint.Title
	}
	if result.Year == 0 {
		result.Year = hint.Year
	}

	if m.metadata != nil {
		if err := m.enrich(ctx, result); err != nil {
			result.Warnings = append(result.Warnings, "tmdb enrichment: "+err.Error())
		}
	}
	return result, nil
}

func (m *Matcher) match_by_search(ctx context.Context, input Input, hint Filename_hint) *Result {
	result := &Result{Media_type: input.Kind, Title: hint.Title, Year: hint.Year}
	if hint.Title == "" {
		result.Warnings = append(result.Warnings, "could not derive a title from the file name")
		return result
	}

	if input.Kind == scanner.Series {
		return m.search_series(ctx, hint, result)
	}
	return m.search_movie(ctx, hint, result)
}

func (m *Matcher) search_movie(ctx context.Context, hint Filename_hint, result *Result) *Result {
	results, err := m.metadata.Search_movie(ctx, hint.Title, hint.Year)
	if err != nil {
		result.Warnings = append(result.Warnings, "tmdb movie search: "+err.Error())
		return result
	}
	scored := make([]Candidate, 0, len(results))
	for _, r := range results {
		year := tmdb.Year_from_date(r.Release_date)
		scored = append(scored, Candidate{
			Source:      "tmdb",
			Tmdb_id:     r.Id,
			Title:       r.Title,
			Year:        year,
			Score:       score_match(hint.Title, hint.Year, r.Title, year),
			Overview:    r.Overview,
			Poster_path: r.Poster_path,
		})
	}
	return m.finish_search(ctx, result, scored, hint)
}

func (m *Matcher) search_series(ctx context.Context, hint Filename_hint, result *Result) *Result {
	results, err := m.metadata.Search_tv(ctx, hint.Title, hint.Year)
	if err != nil {
		result.Warnings = append(result.Warnings, "tmdb tv search: "+err.Error())
		return result
	}
	scored := make([]Candidate, 0, len(results))
	for _, r := range results {
		year := tmdb.Year_from_date(r.First_air_date)
		scored = append(scored, Candidate{
			Source:      "tmdb",
			Tmdb_id:     r.Id,
			Title:       r.Name,
			Year:        year,
			Score:       score_match(hint.Title, hint.Year, r.Name, year),
			Overview:    r.Overview,
			Poster_path: r.Poster_path,
		})
	}
	return m.finish_search(ctx, result, scored, hint)
}

// finish_search sorts candidates, accepts the best if confident enough, and
// otherwise records the candidates for manual matching.
func (m *Matcher) finish_search(ctx context.Context, result *Result, scored []Candidate, hint Filename_hint) *Result {
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	result.Candidates = top_candidates(scored, max_candidates)
	if len(scored) == 0 || scored[0].Score < m.min_confidence {
		return result
	}

	best := scored[0]
	result.Matched = true
	result.Confidence = best.Score
	result.Source = "tmdb"
	result.Tmdb_id = best.Tmdb_id

	var err error
	if result.Media_type == scanner.Series {
		err = m.fill_from_tv_id(ctx, result, best.Tmdb_id)
	} else {
		err = m.fill_from_movie_id(ctx, result, best.Tmdb_id)
	}
	if err != nil {
		result.Warnings = append(result.Warnings, "tmdb details: "+err.Error())
	}
	_ = hint
	return result
}

// match_offline queries the offline dataset for candidates. It never discards
// a TMDB match; when TMDB was inconclusive it appends offline candidates and
// auto-accepts an unambiguous, confident one.
func (m *Matcher) match_offline(ctx context.Context, input Input, hint Filename_hint, result *Result) {
	if hint.Title == "" {
		return
	}
	candidates, err := m.offline.Search(ctx, hint.Title, hint.Year, input.Kind)
	if err != nil {
		result.Warnings = append(result.Warnings, "imdb datasets lookup: "+err.Error())
		return
	}
	scored := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		scored = append(scored, Candidate{
			Source:  "imdb-datasets",
			Imdb_id: c.Imdb_id,
			Title:   c.Title,
			Year:    c.Year,
			Score:   score_match(hint.Title, hint.Year, c.Title, c.Year),
		})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	result.Candidates = top_candidates(merge_candidates(result.Candidates, scored), max_candidates)
	if result.Matched || len(scored) == 0 {
		return
	}

	best := scored[0]
	if best.Score < m.min_confidence {
		return
	}
	if len(scored) > 1 && scored[1].Score >= best.Score {
		return // ambiguous (e.g. identical title/year remakes): manual review
	}
	result.Matched = true
	result.Confidence = best.Score
	result.Source = "imdb-datasets"
	result.Imdb_id = best.Imdb_id
	result.Title = best.Title
	result.Year = best.Year
}

// merge_candidates appends extra candidates to existing, dropping duplicates
// while preserving the original order.
func merge_candidates(existing, extra []Candidate) []Candidate {
	seen := make(map[string]bool, len(existing)+len(extra))
	for _, c := range existing {
		seen[candidate_key(c)] = true
	}
	for _, c := range extra {
		key := candidate_key(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		existing = append(existing, c)
	}
	return existing
}

func candidate_key(c Candidate) string {
	return fmt.Sprintf("%s|%d|%s|%s|%d", c.Source, c.Tmdb_id, c.Imdb_id, c.Title, c.Year)
}

// enrich fills a hash-matched result with full metadata from TMDB, resolving
// by TMDB id first and falling back to the IMDb id.
func (m *Matcher) enrich(ctx context.Context, result *Result) error {
	series := result.Media_type == scanner.Series
	if result.Tmdb_id > 0 {
		if series {
			return m.fill_from_tv_id(ctx, result, result.Tmdb_id)
		}
		return m.fill_from_movie_id(ctx, result, result.Tmdb_id)
	}
	if result.Imdb_id == "" {
		return nil
	}
	found, err := m.metadata.Find_by_imdb(ctx, result.Imdb_id)
	if err != nil {
		return err
	}
	if series && len(found.Tv_results) > 0 {
		result.Tmdb_id = found.Tv_results[0].Id
		return m.fill_from_tv_id(ctx, result, result.Tmdb_id)
	}
	if len(found.Movie_results) > 0 {
		result.Tmdb_id = found.Movie_results[0].Id
		return m.fill_from_movie_id(ctx, result, result.Tmdb_id)
	}
	return nil
}

func (m *Matcher) fill_from_movie_id(ctx context.Context, result *Result, id int) error {
	details, err := m.metadata.Movie_details(ctx, id)
	if err != nil {
		return err
	}
	result.Media_type = scanner.Movie
	result.Tmdb_id = details.Id
	if details.Imdb_id != "" {
		result.Imdb_id = details.Imdb_id
	}
	result.Title = details.Title
	result.Original_title = details.Original_title
	result.Year = tmdb.Year_from_date(details.Release_date)
	result.Overview = details.Overview
	result.Rating = details.Vote_average
	result.Vote_count = details.Vote_count
	result.Genres = genre_names(details.Genres)
	result.Runtime_minutes = details.Runtime
	result.Poster_path = details.Poster_path
	return nil
}

func (m *Matcher) fill_from_tv_id(ctx context.Context, result *Result, id int) error {
	details, err := m.metadata.Tv_details(ctx, id)
	if err != nil {
		return err
	}
	result.Media_type = scanner.Series
	result.Tmdb_id = details.Id
	if details.Imdb_id != "" {
		result.Imdb_id = details.Imdb_id
	}
	result.Title = details.Name
	result.Original_title = details.Original_name
	result.Year = tmdb.Year_from_date(details.First_air_date)
	result.Overview = details.Overview
	result.Rating = details.Vote_average
	result.Vote_count = details.Vote_count
	result.Genres = genre_names(details.Genres)
	result.Number_of_seasons = details.Number_of_seasons
	result.Number_of_episodes = details.Number_of_episodes
	result.First_air_date = details.First_air_date
	result.Last_air_date = details.Last_air_date
	result.Poster_path = details.Poster_path
	return nil
}

// pick_feature chooses the feature whose type matches the file kind, preferring
// the most-downloaded entry. It falls back to the first feature when no type
// matches.
func pick_feature(features []opensubtitles.Feature, kind scanner.Media_type) (opensubtitles.Feature, bool) {
	if len(features) == 0 {
		return opensubtitles.Feature{}, false
	}
	want_series := kind == scanner.Series
	best := -1
	for i, f := range features {
		is_series := f.Feature_type == "episode" || f.Feature_type == "tvshow"
		if is_series != want_series {
			continue
		}
		if best == -1 || f.Download_count > features[best].Download_count {
			best = i
		}
	}
	if best == -1 {
		return features[0], true
	}
	return features[best], true
}

func media_type_for_feature(f opensubtitles.Feature) scanner.Media_type {
	if f.Feature_type == "episode" || f.Feature_type == "tvshow" {
		return scanner.Series
	}
	return scanner.Movie
}

func genre_names(genres []tmdb.Genre) []string {
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if g.Name != "" {
			out = append(out, g.Name)
		}
	}
	return out
}

func top_candidates(candidates []Candidate, n int) []Candidate {
	if len(candidates) <= n {
		return candidates
	}
	return candidates[:n]
}
