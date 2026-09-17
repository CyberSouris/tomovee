package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Discover_options configures a discovery walk.
type Discover_options struct {
	Root           string
	Min_size_bytes int64
	// Flat disables recursion, scanning only the root directory. The default
	// is a recursive walk per the specification.
	Flat bool
	// Exclude_patterns are glob patterns (relative to Root) whose matches are
	// skipped.
	Exclude_patterns []string
}

// Found_file is a single discoverable media file.
type Found_file struct {
	Path       string
	Name       string
	Size_bytes int64
	Mtime      time.Time
	Episode    *Episode_hint
	Media_type Media_type
	Is_special bool
}

// Discover walks Root and returns all files classified as usable content
// (movie or series episode). Permission errors are collected and returned
// alongside the partial result rather than aborting the walk.
func Discover(ctx context.Context, opts Discover_options) ([]Found_file, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("scanner: root must not be empty")
	}
	var (
		files []Found_file
		errs  []string
		excl  []string
	)
	for _, p := range opts.Exclude_patterns {
		abs, err := filepath.Abs(filepath.Join(opts.Root, p))
		if err != nil {
			continue
		}
		excl = append(excl, abs)
	}

	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}

	walk_fn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		if ctx != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}
		name := d.Name()
		if d.IsDir() {
			if name == "." || name == ".." {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if opts.Flat && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if is_excluded(path, excl) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		classified := Classify(name, info.Size(), opts.Min_size_bytes)
		if classified.Kind != Video {
			return nil
		}
		files = append(files, Found_file{
			Path:       path,
			Name:       name,
			Size_bytes: info.Size(),
			Mtime:      info.ModTime(),
			Episode:    classified.Episode,
			Media_type: classified.Type,
			Is_special: classified.Episode != nil && classified.Episode.Is_special,
		})
		return nil
	}

	err = filepath.WalkDir(root, walk_fn)
	if err != nil {
		return files, err
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return files, fmt.Errorf("discovery reported %d problem(s): %s", len(errs), strings.Join(errs, "; "))
	}
	return files, nil
}

func is_excluded(path string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
	}
	return false
}
