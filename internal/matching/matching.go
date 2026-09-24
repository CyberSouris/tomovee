// Package matching runs title matching as a separate background job over the
// catalog, decoupled from scanning. Scanning persists files offline (storing
// each file's OpenSubtitles hash); matching then looks every "needs_lookup"
// entry up through the hash, TMDB search, and the offline IMDb datasets, and
// enriches series episode titles from title.episode.
package matching

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/scan"
	"github.com/cybersouris/tomovee/internal/scanner"
)

// Progress reports one entry as the job processes it.
type Progress struct {
	Phase     string
	Title     string
	Total     int
	Done      int
	Matched   int
	Unmatched int
	Errors    int
}

// Result summarizes a completed matching run.
type Result struct {
	Total     int
	Matched   int
	Unmatched int
	// Candidates is how many ambiguous matches were left for manual review.
	Candidates int
	Errors     []string
}

// Matching runs the background matching job.
type Matching struct {
	store    *database.Store
	matcher  *matcher.Matcher
	datasets atomic.Pointer[imdb_datasets.Index]
	logger   *slog.Logger
}

// New builds a Matching service. datasets may be nil when no offline dataset
// is loaded yet; matcher must not be nil for matching to do anything useful.
func New(store *database.Store, m *matcher.Matcher, datasets *imdb_datasets.Index, logger *slog.Logger) *Matching {
	if logger == nil {
		logger = slog.Default()
	}
	service := &Matching{store: store, matcher: m, logger: logger}
	service.Set_datasets(datasets)
	return service
}

// Set_datasets attaches or replaces the offline dataset used to fill episode
// titles. A nil source disables it. It is safe to call while matching jobs are
// running, so a dataset that finished loading in the background can activate
// without restarting the service.
func (m *Matching) Set_datasets(index *imdb_datasets.Index) {
	if index == nil {
		m.datasets.Store(nil)
		return
	}
	m.datasets.Store(index)
}

// Run matches every catalog entry that is waiting for a lookup and reports
// per-entry progress. It is safe to call concurrently with scans because it
// only reads the database and the persisted hashes the scans wrote.
func (m *Matching) Run(ctx context.Context, libraries []string, progress func(Progress)) (*Result, error) {
	entries, err := m.store.List_catalog_entries(ctx, database.Catalog_filter{
		Status:    "needs_lookup",
		Sort:      "added",
		Libraries: libraries,
	})
	if err != nil {
		return nil, err
	}
	return m.run_entries(ctx, entries, progress), nil
}

// Run_rematch re-runs the automatic matcher over every entry that has a known
// title — matched or waiting for a lookup — so a refreshed or fixed matching
// source can update metadata (genres included) without waiting for a re-scan.
// Entries that still match confidently are re-persisted; the rest are left
// exactly as they were. Truly missing entries (deleted files) are left out so
// they are not reported as fresh match failures.
func (m *Matching) Run_rematch(ctx context.Context, libraries []string, progress func(Progress)) (*Result, error) {
	entries, err := m.store.List_catalog_entries(ctx, database.Catalog_filter{
		Statuses:  []string{"matched", "needs_lookup"},
		Sort:      "added",
		Libraries: libraries,
	})
	if err != nil {
		return nil, err
	}
	return m.run_entries(ctx, entries, progress), nil
}

// run_entries matches a bounded worker pool over the given entries and reports
// per-entry progress.
func (m *Matching) run_entries(ctx context.Context, entries []database.Catalog_entry, progress func(Progress)) *Result {
	result := &Result{}
	result.Total = len(entries)

	// The offline dataset, when attached, is its own SQLite database, so the
	// FTS searches it serves can run concurrently. Match entries in parallel
	// with a bounded worker pool sized to the CPU count so the hash, search,
	// and offline lookups overlap instead of serializing.
	num_workers := runtime.NumCPU()
	if num_workers < 1 {
		num_workers = 1
	}
	if n := len(entries); n > 0 && n < num_workers {
		num_workers = n
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		done      int
		matched   int
		unmatched int
	)
	jobs := make(chan database.Catalog_entry)
	for i := 0; i < num_workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range jobs {
				ok, err := m.match_entry(ctx, entry)
				if err != nil {
					m.logger.Warn("matching: entry failed", "title", entry.Title, "error", err)
				}
				mu.Lock()
				if err != nil {
					result.Errors = append(result.Errors, entry.Title+": "+err.Error())
				} else if ok {
					matched++
					result.Matched++
				} else {
					unmatched++
					result.Unmatched++
				}
				done++
				p := Progress{
					Phase:     "file",
					Title:     entry.Title,
					Total:     result.Total,
					Done:      done,
					Matched:   result.Matched,
					Unmatched: result.Unmatched,
					Errors:    len(result.Errors),
				}
				mu.Unlock()
				if progress != nil {
					progress(p)
				}
			}
		}()
	}
send_loop:
	for _, entry := range entries {
		select {
		case jobs <- entry:
		case <-ctx.Done():
			break send_loop
		}
	}
	close(jobs)
	wg.Wait()
	return result
}

// match_entry identifies one catalog entry and persists the result. It uses
// the largest stored version so the OpenSubtitles hash can be looked up without
// touching the file again.
func (m *Matching) match_entry(ctx context.Context, entry database.Catalog_entry) (bool, error) {
	version, ok, err := m.largest_version(ctx, entry)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	kind := scanner.Movie
	if entry.Media_type == "series" {
		kind = scanner.Series
	}
	match := m.matcher.Match(ctx, matcher.Input{
		Path:      version.File_path,
		File_name: filepath.Base(version.File_path),
		Hash:      version.Hash,
		Kind:      kind,
	})
	m.logger.Debug("matching: entry done",
		"title", entry.Title, "matched", match.Matched, "source", match.Source)

	if !match.Matched {
		return false, nil
	}

	updated := scan.Catalog_entry_from_match(match)
	updated.Id = entry.Id
	if err := m.store.Update_catalog_entry(ctx, entry.Id, updated); err != nil {
		return false, err
	}
	if entry.Media_type == "series" {
		if err := m.enrich_series(ctx, entry.Id, match); err != nil {
			m.logger.Warn("matching: episode enrichment failed", "title", entry.Title, "error", err)
		}
	}
	return true, nil
}

// largest_version picks the biggest stored file for an entry, walking series
// episodes when the entry groups them.
func (m *Matching) largest_version(ctx context.Context, entry database.Catalog_entry) (database.Version, bool, error) {
	if entry.Media_type == "series" {
		episodes, err := m.store.List_episodes(ctx, entry.Id)
		if err != nil {
			return database.Version{}, false, err
		}
		var best database.Version
		for _, episode := range episodes {
			versions, err := m.store.List_versions_for_episode(ctx, episode.Id)
			if err != nil {
				return database.Version{}, false, err
			}
			if len(versions) > 0 && versions[0].Size_bytes >= best.Size_bytes {
				best = versions[0]
			}
		}
		return best, best.File_path != "", nil
	}
	versions, err := m.store.List_versions_for_entry(ctx, entry.Id)
	if err != nil {
		return database.Version{}, false, err
	}
	if len(versions) == 0 {
		return database.Version{}, false, nil
	}
	return versions[0], true, nil
}

// enrich_series stores show-level metadata and offline episode titles for a
// matched series. Episode titles and air-date years come from title.episode
// resolved through title.basics when the offline dataset is loaded.
func (m *Matching) enrich_series(ctx context.Context, entry_id int64, match *matcher.Result) error {
	meta := database.Series_metadata{
		Catalog_entry_id: entry_id,
		First_air_date:   match.First_air_date,
		Last_air_date:    match.Last_air_date,
		Num_seasons:      match.Number_of_seasons,
		Num_episodes:     match.Number_of_episodes,
	}
	if err := m.store.Upsert_series_metadata(ctx, meta); err != nil {
		return err
	}

	episodes, err := m.store.List_episodes(ctx, entry_id)
	if err != nil {
		return err
	}
	datasets := m.datasets.Load()
	if datasets == nil {
		return nil
	}
	parent := match.Imdb_id
	if parent == "" {
		return nil
	}
	episode_titles := make(map[[2]int]imdb_datasets.Episode_title, len(episodes))
	for _, et := range datasets.Episodes_titles(parent) {
		episode_titles[[2]int{et.Season, et.Episode}] = et
	}

	for _, episode := range episodes {
		episode.Status = "matched"
		if et, ok := episode_titles[[2]int{episode.Season_number, episode.Episode_number}]; ok {
			episode.Title = et.Primary_title
			if et.Start_year > 0 {
				episode.Airdate = fmt.Sprintf("%04d", et.Start_year)
			}
		}
		if _, err := m.store.Upsert_episode(ctx, episode); err != nil {
			return err
		}
	}
	return nil
}

// Enrich_episodes is a convenience for the manual re-match handler: given a
// freshly matched series entry, it stores show-level metadata and fills
// episode titles from the offline dataset when available.
func (m *Matching) Enrich_episodes(ctx context.Context, entry_id int64, match *matcher.Result) error {
	if match.Media_type != scanner.Series {
		return nil
	}
	return m.enrich_series(ctx, entry_id, match)
}

// Rematch_one re-runs automatic matching for a single catalog entry. When the
// matcher is confident it persists the result exactly like a background pass
// would. When the match stays ambiguous it returns the candidate shortlist
// without persisting anything, so the caller can let the user choose.
func (m *Matching) Rematch_one(ctx context.Context, entry_id int64) (applied bool, candidates []matcher.Candidate, err error) {
	entry, err := m.store.Get_catalog_entry(ctx, entry_id)
	if err != nil {
		return false, nil, err
	}
	if entry == nil {
		return false, nil, nil
	}
	version, ok, err := m.largest_version(ctx, *entry)
	if err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, nil
	}
	kind := scanner.Movie
	if entry.Media_type == "series" {
		kind = scanner.Series
	}
	result := m.matcher.Match(ctx, matcher.Input{
		Path:      version.File_path,
		File_name: filepath.Base(version.File_path),
		Hash:      version.Hash,
		Kind:      kind,
	})
	if result == nil || !result.Matched {
		if result == nil {
			return false, nil, nil
		}
		return false, result.Candidates, nil
	}
	updated := scan.Catalog_entry_from_match(result)
	updated.Id = entry.Id
	if err := m.store.Update_catalog_entry(ctx, entry.Id, updated); err != nil {
		return false, nil, err
	}
	if entry.Media_type == "series" {
		if err := m.enrich_series(ctx, entry.Id, result); err != nil {
			m.logger.Warn("matching: episode enrichment failed", "title", entry.Title, "error", err)
		}
	}
	return true, nil, nil
}

// Reclassify flips a single catalog entry between series and movie (or the
// other way around) and immediately re-runs automatic matching for it under
// the new media type, persisting the new classification exactly like a
// background pass would. It is what the web UI calls when the user decides a
// catalog title was recorded with the wrong media type: the entry is re-typed,
// any stale episode rows are dropped (movie has none), and the automatic
// matcher runs once against the flipped kind. Confident matches are persisted;
// ambiguous ones return the candidate shortlist for the user to pick from.
func (m *Matching) Reclassify(ctx context.Context, entry_id int64, as_series bool) (applied bool, candidates []matcher.Candidate, err error) {
	entry, err := m.store.Get_catalog_entry(ctx, entry_id)
	if err != nil {
		return false, nil, err
	}
	if entry == nil {
		return false, nil, nil
	}
	flipped := *entry
	flipped.Media_type = "movie"
	if as_series {
		flipped.Media_type = "series"
	}
	if entry.Media_type == flipped.Media_type {
		return false, nil, nil
	}
	if err := m.store.Update_catalog_entry(ctx, entry_id, flipped); err != nil {
		return false, nil, err
	}
	if !as_series {
		if err := m.store.Delete_episodes(ctx, entry_id); err != nil {
			return false, nil, err
		}
	}
	return m.Rematch_one(ctx, entry_id)
}
