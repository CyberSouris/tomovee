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
	"strings"
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
	// A series is searched by its show folder rather than the episode file
	// name, so the folder's title drives the lookup even when episode names
	// are inconsistent. This only applies to library-scanned rows, whose file
	// paths are stored relative to the library root; for them a parent of "."
	// (episodes directly in the root) means there is no show folder and the
	// file name is used. Legacy rows with absolute paths keep the file-name
	// behaviour, since their root (and thus whether the folder really names a
	// show) is unknown.
	file_name := filepath.Base(version.File_path)
	folder_name := false
	if entry.Media_type == "series" && !filepath.IsAbs(version.File_path) {
		if folder := scanner.Series_folder_name(version.File_path); folder != "" {
			file_name = folder
			folder_name = true
		}
	}
	match := m.matcher.Match(ctx, matcher.Input{
		Path:        version.File_path,
		File_name:   file_name,
		Folder_name: folder_name,
		Hash:        version.Hash,
		Kind:        kind,
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
// other way around) and immediately re-runs automatic matching under the new
// media type, persisting the new classification exactly like a background pass
// would. It is what the web UI calls when the user decides a catalog title was
// recorded with the wrong media type. Flipping a movie into a series first
// groups it with the rest of its show folder: sibling movie entries that sit
// under the same directory tree are folded into one series and the drained
// entries are dropped. Confident matches are persisted; ambiguous ones return
// the candidate shortlist for the user to pick from. The returned target_id is
// the catalog entry that ended up as the series (it can differ from entry_id
// when an existing show entry absorbed the re-typed one).
func (m *Matching) Reclassify(ctx context.Context, entry_id int64, as_series bool) (target_id int64, applied bool, candidates []matcher.Candidate, err error) {
	entry, err := m.store.Get_catalog_entry(ctx, entry_id)
	if err != nil {
		return entry_id, false, nil, err
	}
	if entry == nil {
		return entry_id, false, nil, nil
	}
	flipped := *entry
	flipped.Media_type = "movie"
	if as_series {
		flipped.Media_type = "series"
	}
	if entry.Media_type == flipped.Media_type {
		return entry_id, false, nil, nil
	}
	if err := m.store.Update_catalog_entry(ctx, entry_id, flipped); err != nil {
		return entry_id, false, nil, err
	}
	target_id = entry_id
	if as_series {
		target_id, err = m.group_folder_series(ctx, entry_id)
		if err != nil {
			return entry_id, false, nil, err
		}
	} else {
		if err := m.store.Delete_episodes(ctx, entry_id); err != nil {
			return entry_id, false, nil, err
		}
	}
	applied, candidates, err = m.Rematch_one(ctx, target_id)
	return target_id, applied, candidates, err
}

// group_folder_series folds a re-typed series entry together with every other
// entry whose files live under the same show folder, so the folder becomes one
// series instead of many loose titles. The show root is derived from the
// flipped entry's own versions (stepping out of season/special folders); files
// directly in a library root are left alone. When an existing series already
// owns files under the root it becomes the merge target and the re-typed entry
// is drained into it. Movies that still own files outside the root survive.
// Versions that parse as numbered episodes land in episode rows — exactly what
// a scan's folder grouping would have produced — so the series' largest-episode
// matching walk still works.
func (m *Matching) group_folder_series(ctx context.Context, entry_id int64) (int64, error) {
	libraries, err := m.store.List_libraries(ctx)
	if err != nil {
		return entry_id, err
	}
	root_by_library := make(map[int64]string, len(libraries))
	for _, library := range libraries {
		root_by_library[library.Id] = library.Path
	}

	roots := make([]string, 0, 1)
	if versions, err := m.store.List_versions_for_entry(ctx, entry_id); err != nil {
		return entry_id, err
	} else {
		for _, version := range versions {
			path := root_by_library[version.Library_id]
			if path == "" {
				continue
			}
			root := scanner.Series_root_of(filepath.Join(path, version.File_path))
			if root == filepath.Clean(path) {
				continue // no show folder, files sit directly in the library root
			}
			if !contains_path(roots, root) {
				roots = append(roots, root)
			}
		}
	}
	if len(roots) == 0 {
		return entry_id, nil
	}

	owned, err := m.store.List_owned_versions(ctx)
	if err != nil {
		return entry_id, err
	}
	type bucket struct {
		versions []database.Owned_version
		series   bool
	}
	owners := make(map[int64]*bucket)
	for _, version := range owned {
		path := root_by_library[version.Library_id]
		if path == "" {
			continue
		}
		if !under_any_root(filepath.Join(path, version.File_path), roots) {
			continue
		}
		if version.Entry_id == 0 {
			continue
		}
		b := owners[version.Entry_id]
		if b == nil {
			b = &bucket{}
			owners[version.Entry_id] = b
		}
		b.versions = append(b.versions, version)
		if version.Entry_media_type == "series" {
			b.series = true
		}
	}
	if len(owners) == 0 {
		return entry_id, nil
	}

	target := entry_id
	drain := make(map[int64]bool)
	for id, b := range owners {
		if b.series && id != entry_id && len(b.versions) > 0 {
			target = id
		}
	}
	for id, b := range owners {
		if b.series || id == entry_id {
			continue
		}
		drain[id] = true
	}
	if target != entry_id {
		for id := range owners {
			if id != target {
				drain[id] = true
			}
		}
	}

	// Fold every file of a drained entry, plus the anchor's own files when it
	// re-uses the freshly flipped row, into the target series.
	for id, b := range owners {
		if id != target && !drain[id] {
			continue
		}
		for _, version := range b.versions {
			if err := m.fold_series_version(ctx, target, version); err != nil {
				return entry_id, err
			}
		}
	}

	for id := range drain {
		versions, err := m.store.List_versions_for_entry(ctx, id)
		if err != nil {
			return entry_id, err
		}
		if len(versions) > 0 {
			continue // the entry still owns files outside the folder, leave it alone
		}
		if err := m.store.Delete_catalog_entry(ctx, id); err != nil {
			return entry_id, err
		}
	}

	if target == entry_id {
		// The flipped entry becomes the show-wide entry; name it after the
		// folder so re-scans group it again instead of spawning a duplicate.
		anchor, err := m.store.Get_catalog_entry(ctx, target)
		if err != nil {
			return entry_id, err
		}
		folder := matcher.Parse_foldername(filepath.Base(roots[0]))
		if anchor != nil && folder.Title != "" && anchor.Title != folder.Title {
			anchor.Title = folder.Title
			anchor.Release_year = 0
			if err := m.store.Update_catalog_entry(ctx, target, *anchor); err != nil {
				return entry_id, err
			}
		}
	}
	return target, nil
}

// fold_series_version moves one stored file under a series' show folder. Files
// that parse as numbered episodes become episode rows; everything else attaches
// directly to the show, mirroring the scanner's folder grouping.
func (m *Matching) fold_series_version(ctx context.Context, target_id int64, version database.Owned_version) error {
	hint := matcher.Parse_filename(filepath.Base(version.File_path))
	if hint.Is_series && hint.Episode > 0 {
		episode_id, err := m.store.Upsert_episode(ctx, database.Episode{
			Catalog_entry_id: target_id,
			Season_number:    hint.Season,
			Episode_number:   hint.Episode,
			Is_special:       hint.Season == 0,
			Status:           "needs_lookup",
		})
		if err != nil {
			return err
		}
		return m.store.Move_version_to_episode(ctx, version.Version_id, episode_id)
	}
	return m.store.Move_version_to_entry(ctx, version.Version_id, target_id)
}

// Split_version_to_movie cuts one stored file out of the entry or episode that
// currently owns it and gives it a movie entry of its own, so files that were
// grouped under the wrong title can be separated into distinct movies. The new
// entry starts waiting for a lookup and the caller is expected to rematch it.
// When the split drains the last file out of the old owner, the owner is
// dropped (its episodes included, since they lost their files too) so the
// catalog does not keep empty ghost rows. It returns the id of the new movie
// entry, or 0 when the version does not exist.
func (m *Matching) Split_version_to_movie(ctx context.Context, version_id int64) (int64, error) {
	version, err := m.store.Get_version(ctx, version_id)
	if err != nil {
		return 0, err
	}
	if version == nil {
		return 0, nil
	}
	owner_id, err := m.version_owner(ctx, version)
	if err != nil {
		return 0, err
	}
	hint := matcher.Parse_filename(filepath.Base(version.File_path))
	title := hint.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(version.File_path), filepath.Ext(version.File_path))
	}
	new_id, err := m.store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type:   "movie",
		Title:        title,
		Release_year: hint.Year,
		Status:       "needs_lookup",
	})
	if err != nil {
		return 0, err
	}
	if err := m.store.Move_version_to_entry(ctx, version_id, new_id); err != nil {
		return 0, err
	}
	if err := m.cleanup_old_owner(ctx, version, owner_id, new_id); err != nil {
		return 0, err
	}
	return new_id, nil
}

// Assign_version_to_episode attaches a stored file to the episode a user chose
// for it. The user enters the season and episode numbers directly, so files
// whose names did not parse as episodes can still be placed under a show. The
// owning entry must be a series; the episode row is created on demand and the
// version is moved onto it. It returns the id of the episode, or 0 when the
// version does not exist.
func (m *Matching) Assign_version_to_episode(ctx context.Context, version_id int64, season, episode int) (int64, error) {
	version, err := m.store.Get_version(ctx, version_id)
	if err != nil {
		return 0, err
	}
	if version == nil {
		return 0, nil
	}
	owner_id, err := m.version_owner(ctx, version)
	if err != nil {
		return 0, err
	}
	if owner_id == 0 {
		return 0, nil
	}
	entry, err := m.store.Get_catalog_entry(ctx, owner_id)
	if err != nil {
		return 0, err
	}
	if entry == nil {
		return 0, nil
	}
	if entry.Media_type != "series" {
		return 0, fmt.Errorf("cannot assign version %d to an episode of the %s %q", version_id, entry.Media_type, entry.Title)
	}
	episode_id, err := m.store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: owner_id,
		Season_number:    season,
		Episode_number:   episode,
		Is_special:       season == 0,
		Status:           "needs_lookup",
	})
	if err != nil {
		return 0, err
	}
	if err := m.store.Move_version_to_episode(ctx, version_id, episode_id); err != nil {
		return 0, err
	}
	if err := m.cleanup_old_owner(ctx, version, owner_id, owner_id); err != nil {
		return 0, err
	}
	return episode_id, nil
}

// version_owner returns the catalog entry a version currently backs, resolving
// episode-owned files through their episode row.
func (m *Matching) version_owner(ctx context.Context, version *database.Version) (int64, error) {
	if version.Catalog_entry_id != 0 {
		return version.Catalog_entry_id, nil
	}
	if version.Episode_id == 0 {
		return 0, nil
	}
	episode, err := m.store.Get_episode(ctx, version.Episode_id)
	if err != nil {
		return 0, err
	}
	if episode == nil {
		return 0, nil
	}
	return episode.Catalog_entry_id, nil
}

// cleanup_old_owner removes the empty remains of a version's previous owner
// after it was moved out: an episode that lost its only file, and a catalog
// entry that ended up with neither versions nor episodes. Entries that still
// own files or episodes are left untouched.
func (m *Matching) cleanup_old_owner(ctx context.Context, version *database.Version, owner_id, new_id int64) error {
	if version.Episode_id != 0 {
		versions, err := m.store.List_versions_for_episode(ctx, version.Episode_id)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			if err := m.store.Delete_episode(ctx, version.Episode_id); err != nil {
				return err
			}
		}
	}
	if owner_id == 0 || owner_id == new_id {
		return nil
	}
	versions, err := m.store.List_versions_for_entry(ctx, owner_id)
	if err != nil {
		return err
	}
	if len(versions) > 0 {
		return nil
	}
	episodes, err := m.store.List_episodes(ctx, owner_id)
	if err != nil {
		return err
	}
	if len(episodes) > 0 {
		return nil
	}
	return m.store.Delete_catalog_entry(ctx, owner_id)
}

func contains_path(paths []string, candidate string) bool {
	for _, path := range paths {
		if path == candidate {
			return true
		}
	}
	return false
}

func under_any_root(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
