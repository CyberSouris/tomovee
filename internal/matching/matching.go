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
	Errors    []string
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
func (m *Matching) Run(ctx context.Context, progress func(Progress)) (*Result, error) {
	result := &Result{}
	report := func(entry database.Catalog_entry) {
		if progress != nil {
			progress(Progress{
				Phase:     "file",
				Title:     entry.Title,
				Total:     result.Total,
				Done:      result.Matched + result.Unmatched,
				Matched:   result.Matched,
				Unmatched: result.Unmatched,
				Errors:    len(result.Errors),
			})
		}
	}

	entries, err := m.store.List_catalog_entries(ctx, database.Catalog_filter{
		Status: "needs_lookup",
		Sort:   "added",
	})
	if err != nil {
		return nil, err
	}
	result.Total = len(entries)

	for i := range entries {
		entry := entries[i]
		if err := ctx.Err(); err != nil {
			break
		}
		matched, err := m.match_entry(ctx, entry)
		if err != nil {
			m.logger.Warn("matching: entry failed", "title", entry.Title, "error", err)
			result.Errors = append(result.Errors, entry.Title+": "+err.Error())
		} else if matched {
			result.Matched++
		} else {
			result.Unmatched++
		}
		report(entry)
	}
	return result, nil
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
	for _, episode := range episodes {
		episode.Status = "matched"
		if ref, ok := datasets.Episode_lookup(parent, episode.Season_number, episode.Episode_number); ok {
			if title, ok := datasets.Lookup(ref.Id); ok {
				episode.Title = title.Primary_title
				if title.Start_year > 0 {
					episode.Airdate = fmt.Sprintf("%04d", title.Start_year)
				}
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
