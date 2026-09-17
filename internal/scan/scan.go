// Package scan orchestrates the end-to-end scanning pipeline: discovery,
// re-scan detection, hashing, ffprobe metadata extraction, matching, and
// persistence (including version grouping). It lives apart from the scanner
// package because the matcher already depends on scanner's classification
// types; orchestrating both here keeps the dependency graph acyclic.
package scan

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/metadata"
	"github.com/cybersouris/tomovee/internal/opensubtitles"
	"github.com/cybersouris/tomovee/internal/scanner"
)

// Options configures a Scanner.
type Options struct {
	Directories      []string
	Exclude_patterns []string
	Min_size_bytes   int64
	Logger           *slog.Logger
	// Progress, when set, receives a snapshot after each processed file and at
	// phase transitions. It must not block for long; it is called synchronously.
	Progress func(Progress)
}

// Progress is a point-in-time report of scanning activity.
type Progress struct {
	Phase         string
	Path          string
	Files_found   int
	Files_scanned int
	New_files     int
	Matched       int
	Unmatched     int
	Skipped       int
	Errors        int
}

// Result summarizes a completed scan.
type Result struct {
	Found     int
	Scanned   int
	New       int
	Matched   int
	Unmatched int
	Skipped   int
	Missing   int
	Errors    []string
}

// Scanner runs the scanning pipeline against the persisted catalog.
type Scanner struct {
	store   *database.Store
	matcher *matcher.Matcher
	opts    Options

	run_mu sync.Mutex

	// probe and hash are seams for testing.
	probe func(context.Context, string) (*metadata.File_info, error)
	hash  func(string) (string, error)
}

// New builds a Scanner.
func New(store *database.Store, m *matcher.Matcher, opts Options) *Scanner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opts.Logger = logger
	return &Scanner{
		store:   store,
		matcher: m,
		opts:    opts,
		probe:   metadata.Probe,
		hash:    opensubtitles.Compute_hash,
	}
}

// Run executes one full scan of the configured directories.
func (s *Scanner) Run(ctx context.Context) (*Result, error) {
	return s.Run_paths(ctx, nil, nil)
}

// Run_with_progress behaves like Run but reports progress to the supplied
// callback for this invocation only.
func (s *Scanner) Run_with_progress(ctx context.Context, progress func(Progress)) (*Result, error) {
	return s.Run_paths(ctx, nil, progress)
}

// Run_paths runs a scan, optionally scanning directories instead of the
// configured ones (used by folder watching) and reporting progress. Scans are
// serialized: a call blocks until any in-flight scan on this Scanner finishes.
func (s *Scanner) Run_paths(ctx context.Context, directories []string, progress func(Progress)) (*Result, error) {
	s.run_mu.Lock()
	defer s.run_mu.Unlock()

	previous_dirs := s.opts.Directories
	previous_progress := s.opts.Progress
	if directories != nil {
		s.opts.Directories = directories
	}
	if progress != nil {
		s.opts.Progress = progress
	}
	defer func() {
		s.opts.Directories = previous_dirs
		s.opts.Progress = previous_progress
	}()
	return s.run(ctx)
}

func (s *Scanner) run(ctx context.Context) (*Result, error) {
	result := &Result{}
	seen := make(map[string]bool)

	roots := make([]string, 0, len(s.opts.Directories))
	for _, dir := range s.opts.Directories {
		abs, err := filepath.Abs(dir)
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
		s.report(Progress{Phase: "discover", Files_found: result.Found})

		for _, file := range files {
			seen[file.Path] = true
			s.process_file(ctx, file, result)
		}
	}

	if err := s.mark_missing(ctx, roots, seen, result); err != nil {
		result.Errors = append(result.Errors, "mark missing: "+err.Error())
	}
	s.report(Progress{
		Phase:       "done",
		Files_found: result.Found,
		New_files:   result.New,
		Matched:     result.Matched,
		Unmatched:   result.Unmatched,
		Skipped:     result.Skipped,
		Errors:      len(result.Errors),
	})
	return result, nil
}

func (s *Scanner) process_file(ctx context.Context, file scanner.Found_file, result *Result) {
	mtime := format_mtime(file.Mtime)

	if ref, found, err := s.store.Find_version_by_path(ctx, file.Path); err != nil {
		s.add_error(result, file.Path, err)
	} else if found && ref.Size_bytes == file.Size_bytes && ref.Mtime == mtime && ref.Status == "matched" {
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

	match := s.matcher.Match(ctx, matcher.Input{
		Path:      file.Path,
		File_name: file.Name,
		Hash:      hash,
		Kind:      file.Media_type,
	})

	if err := s.persist(ctx, file, info, match); err != nil {
		s.add_error(result, file.Path, err)
		s.report_file(result, file.Path)
		return
	}

	result.New++
	if match.Matched {
		result.Matched++
	} else {
		result.Unmatched++
	}
	s.report_file(result, file.Path)
}

func (s *Scanner) report_file(result *Result, path string) {
	s.report(Progress{
		Phase:         "file",
		Path:          path,
		Files_found:   result.Found,
		Files_scanned: result.Scanned,
		New_files:     result.New,
		Matched:       result.Matched,
		Unmatched:     result.Unmatched,
		Skipped:       result.Skipped,
		Errors:        len(result.Errors),
	})
}

func (s *Scanner) report(progress Progress) {
	if s.opts.Progress != nil {
		s.opts.Progress(progress)
	}
}

func (s *Scanner) add_error(result *Result, path string, err error) {
	result.Errors = append(result.Errors, path+": "+err.Error())
	s.opts.Logger.Warn("scan: file failed", "path", path, "error", err)
}

// mark_missing flags entries and episodes under the scanned roots whose files
// were not seen during this scan. A catalog entry is only flagged missing when
// none of its versions remain present.
func (s *Scanner) mark_missing(ctx context.Context, roots []string, seen map[string]bool, result *Result) error {
	refs, err := s.store.List_version_refs(ctx)
	if err != nil {
		return err
	}
	entries := make(map[int64]bool)
	present := make(map[int64]bool)
	for _, ref := range refs {
		if !under_any_root(ref.File_path, roots) {
			continue
		}
		if ref.Episode_id > 0 {
			if !seen[ref.File_path] {
				if err := s.store.Set_episode_status(ctx, ref.Episode_id, "missing"); err != nil {
					return err
				}
				result.Missing++
			}
		}
		if ref.Catalog_entry_id > 0 {
			entries[ref.Catalog_entry_id] = true
			if seen[ref.File_path] {
				present[ref.Catalog_entry_id] = true
			}
		}
	}
	for entry_id := range entries {
		if present[entry_id] {
			continue
		}
		if err := s.store.Set_catalog_status(ctx, entry_id, "missing"); err != nil {
			return err
		}
		result.Missing++
	}
	return nil
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
