// Package watcher periodically scans enabled watch folders so newly added or
// changed files are picked up without a manual scan.
package watcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/scan"
)

// Default_interval is the polling cadence used when none is configured.
const Default_interval = 5 * time.Minute

// Runner is the scanning behavior the watcher depends on. *scan.Scanner
// satisfies it.
type Runner interface {
	Run_paths(ctx context.Context, directories []string, progress func(scan.Progress)) (*scan.Result, error)
}

// Options configures a Watcher.
type Options struct {
	Interval        time.Duration
	Default_enabled bool
	Logger          *slog.Logger
	On_scan         func(scan.Result)
}

// Watcher polls the database for enabled watch folders and scans them.
type Watcher struct {
	store           *database.Store
	runner          Runner
	interval        time.Duration
	default_enabled bool
	logger          *slog.Logger
	on_scan         func(scan.Result)
}

// New builds a Watcher.
func New(store *database.Store, runner Runner, opts Options) *Watcher {
	interval := opts.Interval
	if interval <= 0 {
		interval = Default_interval
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{
		store:           store,
		runner:          runner,
		interval:        interval,
		default_enabled: opts.Default_enabled,
		logger:          logger,
		on_scan:         opts.On_scan,
	}
}

// Run polls until ctx is cancelled. It performs one check immediately, then
// repeats every interval.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.Scan_once(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Warn("watch scan failed", "error", err)
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.Scan_once(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.Warn("watch scan failed", "error", err)
			}
		}
	}
}

// Scan_once scans all enabled watch folders a single time, if watching is on.
func (w *Watcher) Scan_once(ctx context.Context) error {
	if !w.enabled(ctx) {
		return nil
	}
	folders, err := w.store.Enabled_watch_folders(ctx)
	if err != nil {
		return err
	}
	if len(folders) == 0 {
		return nil
	}
	directories := make([]string, 0, len(folders))
	for _, folder := range folders {
		directories = append(directories, folder.Path)
	}
	w.logger.Info("watch scan starting", "folders", len(directories))
	result, err := w.runner.Run_paths(ctx, directories, nil)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, folder := range folders {
		if err := w.store.Touch_watch_folder(ctx, folder.Path, now); err != nil {
			return err
		}
	}
	w.logger.Info("watch scan complete",
		"new", result.New, "skipped", result.Skipped, "errors", len(result.Errors))
	if w.on_scan != nil {
		w.on_scan(*result)
	}
	return nil
}

func (w *Watcher) enabled(ctx context.Context) bool {
	value, ok, err := w.store.Config_get(ctx, "watch_enabled")
	if err != nil {
		w.logger.Warn("watch: read setting failed", "error", err)
		return false
	}
	if ok {
		return value == "true"
	}
	return w.default_enabled
}
