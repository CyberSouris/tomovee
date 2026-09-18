// Package scan orchestrates the scanning pipeline: discovery, re-scan
// detection, hashing, ffprobe metadata extraction, and persistence (including
// version grouping). It lives apart from the scanner package because the
// matcher already depends on scanner's classification types; orchestrating both
// here keeps the dependency graph acyclic. Scanning is fully offline: matching
// runs separately (internal/matching) and streams its own progress.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/metadata"
	"github.com/cybersouris/tomovee/internal/opensubtitles"
	"github.com/cybersouris/tomovee/internal/scanner"
	"github.com/cybersouris/tomovee/internal/thumbnail"
)

// Options configures a Scanner.
type Options struct {
	Exclude_patterns []string
	Min_size_bytes   int64
	// Poster_dir, when set, receives generated frame posters for entries that
	// have no online artwork. Empty disables the fallback.
	Poster_dir string
	Logger     *slog.Logger
	// Progress, when set, receives a snapshot after each processed file and at
	// phase transitions. It must not block for long; it is called synchronously.
	Progress func(Progress)
}

// Progress is a point-in-time report of scanning activity. Match counts are
// deliberately absent: matching now runs as a separate background job.
type Progress struct {
	Phase         string
	Path          string
	Files_found   int
	Files_scanned int
	New_files     int
	Skipped       int
	Errors        int
}

// Result summarizes a completed scan.
type Result struct {
	Found   int
	Scanned int
	New     int
	Skipped int
	Missing int
	Errors  []string
}

// Scanner runs the offline scanning pipeline against the persisted catalog.
type Scanner struct {
	store *database.Store
	opts  Options

	run_mu sync.Mutex

	ffmpeg_warn sync.Once

	// probe and hash are seams for testing.
	probe func(context.Context, string) (*metadata.File_info, error)
	hash  func(string) (string, error)
	// extract is a seam for the frame-poster fallback.
	extract func(context.Context, string, float64, string) error
}

// New builds a Scanner.
func New(store *database.Store, opts Options) *Scanner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opts.Logger = logger
	return &Scanner{
		store:   store,
		opts:    opts,
		probe:   metadata.Probe,
		hash:    opensubtitles.Compute_hash,
		extract: thumbnail.Extract,
	}
}

// Run executes one full scan of every registered library.
func (s *Scanner) Run(ctx context.Context) (*Result, error) {
	return s.Run_libraries(ctx, nil, s.opts.Progress)
}

// Run_libraries runs a scan. When names is non-empty only those libraries are
// scanned (used by the web UI's per-library refresh and folder watching);
// otherwise every registered library is scanned. The progress callback applies
// to this invocation only. Scans are serialized: a call blocks until any
// in-flight scan on this Scanner finishes.
func (s *Scanner) Run_libraries(ctx context.Context, names []string, progress func(Progress)) (*Result, error) {
	s.run_mu.Lock()
	defer s.run_mu.Unlock()

	previous_progress := s.opts.Progress
	if progress != nil {
		s.opts.Progress = progress
	}
	defer func() {
		s.opts.Progress = previous_progress
	}()
	return s.run(ctx, names)
}

func (s *Scanner) run(ctx context.Context, names []string) (*Result, error) {
	result := &Result{}

	libraries, err := s.store.List_libraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	if len(names) > 0 {
		selected := make(map[string]bool, len(names))
		for _, name := range names {
			selected[name] = true
		}
		filtered := libraries[:0]
		for _, library := range libraries {
			if selected[library.Name] {
				filtered = append(filtered, library)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no such library: %s", strings.Join(names, ", "))
		}
		libraries = filtered
	}
	if len(libraries) == 0 {
		result.Errors = append(result.Errors, "no libraries configured")
		return result, nil
	}

	roots := make([]string, 0, len(libraries))
	lib_by_id := make(map[int64]database.Library, len(libraries))
	seen := make(map[string]bool)

	for _, library := range libraries {
		lib_by_id[library.Id] = library
		// Convert absolute-path rows left over from pre-library installs so
		// this library's relative paths resume working.
		if _, err := s.store.Normalize_library_versions(ctx, library); err != nil {
			result.Errors = append(result.Errors, library.Name+": normalize: "+err.Error())
		}

		abs, err := filepath.Abs(library.Path)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		roots = append(roots, abs)

		files, err := scanner.Discover(ctx, scanner.Discover_options{
			Root:             abs,
			Min_size_bytes:   s.opts.Min_size_bytes,
			Exclude_patterns: s.opts.Exclude_patterns,
		})
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
		}
		result.Found += len(files)
		s.report(Progress{Phase: "discover", Path: library.Name, Files_found: result.Found})

		for _, file := range files {
			seen[file.Path] = true
			s.process_file(ctx, library, abs, file, result)
		}
	}

	if err := s.mark_missing(ctx, roots, lib_by_id, seen, result); err != nil {
		result.Errors = append(result.Errors, "mark missing: "+err.Error())
	}
	s.report(Progress{
		Phase:       "done",
		Files_found: result.Found,
		New_files:   result.New,
		Skipped:     result.Skipped,
		Errors:      len(result.Errors),
	})
	return result, nil
}

func (s *Scanner) process_file(ctx context.Context, library database.Library, root string, file scanner.Found_file, result *Result) {
	mtime := format_mtime(file.Mtime)
	relative, err := filepath.Rel(root, file.Path)
	if err != nil {
		s.add_error(result, file.Path, err)
		return
	}

	if ref, found, err := s.store.Find_version_in_library(ctx, library.Id, relative); err != nil {
		s.add_error(result, file.Path, err)
	} else if found && ref.Size_bytes == file.Size_bytes && ref.Mtime == mtime {
		result.Skipped++
		s.report_file(result, file.Path)
		return
	}

	result.Scanned++

	var hash string
	if computed, err := s.hash(file.Path); err == nil {
		hash = computed
	} else if !errors.Is(err, opensubtitles.Err_file_too_small) {
		s.add_error(result, file.Path, err)
	}

	info, err := s.probe(ctx, file.Path)
	if err != nil {
		s.add_error(result, file.Path, err)
		s.report_file(result, file.Path)
		return
	}

	if err := s.persist(ctx, library, root, file, info, hash); err != nil {
		s.add_error(result, file.Path, err)
		s.report_file(result, file.Path)
		return
	}

	result.New++
	s.report_file(result, file.Path)
}

func (s *Scanner) report_file(result *Result, path string) {
	s.report(Progress{
		Phase:         "file",
		Path:          path,
		Files_found:   result.Found,
		Files_scanned: result.Scanned,
		New_files:     result.New,
		Skipped:       result.Skipped,
		Errors:        len(result.Errors),
	})
}

func (s *Scanner) report(progress Progress) {
	if s.opts.Progress != nil {
		s.opts.Progress(progress)
	}
}

// add_error records a per-file failure without aborting the scan.
func (s *Scanner) add_error(result *Result, path string, err error) {
	result.Errors = append(result.Errors, path+": "+err.Error())
	s.opts.Logger.Warn("scan: file failed", "path", path, "error", err)
}

// mark_missing flags entries and episodes under the scanned roots whose files
// were not seen during this scan. A catalog entry is only flagged missing when
// none of its versions remain present. Entries that still have versions in a
// library that was not part of this scan (or in legacy absolute-path rows) are
// never flagged, so a partial library refresh cannot hide files elsewhere.
func (s *Scanner) mark_missing(ctx context.Context, roots []string, lib_by_id map[int64]database.Library, seen map[string]bool, result *Result) error {
	refs, err := s.store.List_version_refs(ctx)
	if err != nil {
		return err
	}
	entries := make(map[int64]bool)
	present := make(map[int64]bool)
	excluded := make(map[int64]bool)
	for _, ref := range refs {
		path := existing_version_path(ref, lib_by_id)
		in_scope := under_any_root(path, roots)
		if ref.Catalog_entry_id > 0 {
			if in_scope {
				entries[ref.Catalog_entry_id] = true
				if seen[path] {
					present[ref.Catalog_entry_id] = true
				}
			} else {
				excluded[ref.Catalog_entry_id] = true
			}
		}
		if !in_scope {
			continue
		}
		if ref.Episode_id > 0 && !seen[path] {
			if err := s.store.Set_episode_status(ctx, ref.Episode_id, "missing"); err != nil {
				return err
			}
			result.Missing++
		}
	}
	for entry_id := range entries {
		if excluded[entry_id] || present[entry_id] {
			continue
		}
		if err := s.store.Set_catalog_status(ctx, entry_id, "missing"); err != nil {
			return err
		}
		result.Missing++
	}
	return nil
}

// existing_version_path reconstructs the filesystem path of a version row.
// Library-relative rows are joined against their library's root; rows with no
// library (legacy absolute paths, or libraries outside this scan) pass through
// unchanged.
func existing_version_path(ref database.Version_ref, lib_by_id map[int64]database.Library) string {
	if ref.Library_id > 0 {
		if library, ok := lib_by_id[ref.Library_id]; ok && library.Path != "" {
			return filepath.Join(library.Path, ref.File_path)
		}
	}
	return ref.File_path
}

func under_any_root(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func format_mtime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
