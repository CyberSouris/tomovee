// Command tomovee is the movie and TV database daemon and one-shot scanner.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/metacache"
	"github.com/cybersouris/tomovee/internal/opensubtitles"
	"github.com/cybersouris/tomovee/internal/poster_cache"
	"github.com/cybersouris/tomovee/internal/scan"
	"github.com/cybersouris/tomovee/internal/tmdb"
	"github.com/cybersouris/tomovee/internal/watcher"
	"github.com/cybersouris/tomovee/internal/webserver"
	"github.com/cybersouris/tomovee/internal/webui"
)

const version = "0.1.0"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version", "--version":
		fmt.Printf("tomovee %s\n", version)
	case "serve":
		err = cmd_serve(logger, os.Args[2:])
	case "scan":
		err = cmd_scan(logger, os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `tomovee %s — movie and TV database

Usage:
  tomovee serve [--config PATH]              run the daemon (REST API + web UI)
  tomovee scan  [--config PATH] [--library NAME...]  run a one-shot scan and exit
  tomovee version                            print the version
  tomovee help                               show this help

  --config PATH    config file (default: %s)
  --library NAME   scan only the named library (repeatable; default: all)
`, version, config.Default_config_path())
}

// string_list is a repeatable command-line flag.
type string_list []string

func (s *string_list) String() string { return strings.Join(*s, ",") }
func (s *string_list) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// load_config parses the command line, reads the config file, and validates
// it. extra_flags may register additional flags for the invoking command.
func load_config(args []string, extra_flags ...func(*flag.FlagSet)) (*config.Config, error) {
	fs := flag.NewFlagSet("tomovee", flag.ContinueOnError)
	config_path := fs.String("config", "", "path to the YAML config file")
	for _, extra := range extra_flags {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	cfg := config.Defaults()
	if *config_path == "" {
		*config_path = config.Default_config_path()
	}
	loaded, err := config.Load(*config_path)
	if err != nil {
		return nil, err
	}
	cfg = loaded
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// register_libraries mirrors the config's libraries into the database so the
// scanner, watcher, and web UI all see the same set. Existing rows keep their
// enabled state and scan timestamps.
func register_libraries(ctx context.Context, store *database.Store, cfg *config.Config, default_enabled bool) error {
	for name, dir := range cfg.Libraries {
		if err := store.Ensure_library(ctx, name, dir, default_enabled); err != nil {
			return fmt.Errorf("register library %q: %w", name, err)
		}
	}
	return nil
}

func open_database(cfg *config.Config) (*database.Database, error) {
	d, err := database.Open(cfg.Database_path)
	if err != nil {
		return nil, err
	}
	applied, err := d.Migrations_applied()
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	slog.Info("database ready", "path", cfg.Database_path, "migrations", applied)
	return d, nil
}

// pipeline bundles the matching components so both the background matching job
// and the web API share one configured instance.
type pipeline struct {
	matcher  *matcher.Matcher
	metadata matcher.Metadata_source
	datasets *imdb_datasets.Index
}

// build_pipeline assembles the matching pipeline from the configured API keys
// and the optional IMDb datasets directory or file. Missing keys are warnings,
// not errors: the pipeline degrades gracefully per the specification.
func build_pipeline(logger *slog.Logger, cfg *config.Config, store *database.Store) (*pipeline, error) {
	opts := matcher.Options{Logger: logger}

	if cfg.Api.Opensubtitles_api_key != "" {
		opts.Subtitles = opensubtitles.New(opensubtitles.Config{
			Api_key:    cfg.Api.Opensubtitles_api_key,
			User_agent: cfg.Api.Opensubtitles_user_agent,
		})
	} else {
		logger.Warn("opensubtitles api key not configured; hash lookup disabled")
	}

	if cfg.Api.Tmdb_key != "" {
		opts.Metadata = tmdb.New(tmdb.Config{Api_key: cfg.Api.Tmdb_key})
	} else {
		logger.Warn("tmdb api key not configured; falling back to offline matching")
	}
	if store != nil && opts.Metadata != nil {
		opts.Metadata = metacache.New(opts.Metadata, store, metacache.Default_ttl, logger)
	}

	var datasets *imdb_datasets.Index
	if cfg.Imdb_datasets_path != "" {
		index, err := imdb_datasets.Open(cfg.Imdb_datasets_path)
		if err != nil {
			return nil, err
		}
		datasets = index
		logger.Info("imdb datasets loaded", "path", cfg.Imdb_datasets_path, "titles", index.Count())
		opts.Offline = imdb_datasets.New_matcher_source(index)
	}

	return &pipeline{matcher: matcher.New(opts), metadata: opts.Metadata, datasets: datasets}, nil
}

func cmd_serve(logger *slog.Logger, args []string) error {
	cfg, err := load_config(args)
	if err != nil {
		return err
	}
	db, err := open_database(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := os.MkdirAll(cfg.Poster_cache_dir, 0o755); err != nil {
		return err
	}

	store := database.New_store(db)
	pipe, err := build_pipeline(logger, cfg, store)
	if err != nil {
		return err
	}
	runner := scan.New(store, scan.Options{
		Min_size_bytes: int64(cfg.Scan.Min_file_size_mb) * 1024 * 1024,
		Poster_dir:     cfg.Poster_cache_dir,
		Logger:         logger,
	})

	if err := register_libraries(context.Background(), store, cfg, cfg.Watch_enabled); err != nil {
		return err
	}

	posters := poster_cache.New(poster_cache.Options{
		Dir:        cfg.Poster_cache_dir,
		User_agent: cfg.Api.Opensubtitles_user_agent,
		Logger:     logger,
	})

	matching_service := matching.New(store, pipe.matcher, pipe.datasets, logger)

	server := webserver.New(webserver.Options{
		Store:    store,
		Config:   cfg,
		Matcher:  pipe.matcher,
		Metadata: pipe.metadata,
		Scanner:  runner,
		Matching: matching_service,
		Posters:  posters,
		Static:   webui.FS(),
		Logger:   logger,
	})

	if matching_configured(cfg) {
		logger.Info("starting background matching at startup")
		server.Auto_match()
	}

	watch_ctx, stop_watch := context.WithCancel(context.Background())
	defer stop_watch()
	go func() {
		w := watcher.New(store, runner, watcher.Options{
			Default_enabled: cfg.Watch_enabled,
			Logger:          logger,
		})
		if err := w.Run(watch_ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("folder watcher stopped", "error", err)
		}
	}()

	http_server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		logger.Info("serving", "listen", cfg.Listen, "web_ui", true)
		if err := http_server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-stop:
	}

	logger.Info("shutting down")
	stop_watch()
	shutdown_ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return http_server.Shutdown(shutdown_ctx)
}

func cmd_scan(logger *slog.Logger, args []string) error {
	var libraries string_list
	cfg, err := load_config(args, func(fs *flag.FlagSet) {
		fs.Var(&libraries, "library", "scan only the named library (repeatable)")
	})
	if err != nil {
		return err
	}
	db, err := open_database(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	store := database.New_store(db)
	if err := register_libraries(context.Background(), store, cfg, cfg.Watch_enabled); err != nil {
		return err
	}

	progress := func(p scan.Progress) {
		if p.Phase == "file" {
			logger.Info("scanning", "scanned", p.Files_scanned, "new", p.New_files, "file", p.Path)
		}
	}
	runner := scan.New(store, scan.Options{
		Min_size_bytes: int64(cfg.Scan.Min_file_size_mb) * 1024 * 1024,
		Poster_dir:     cfg.Poster_cache_dir,
		Logger:         logger,
		Progress:       progress,
	})

	result, err := runner.Run_libraries(context.Background(), []string(libraries), nil)
	if err != nil {
		return err
	}

	fmt.Printf("scan complete: %d found, %d new, %d skipped, %d missing, %d error(s)\n",
		result.Found, result.New, result.Skipped, result.Missing, len(result.Errors))
	for _, e := range result.Errors {
		fmt.Fprintln(os.Stderr, "error:", e)
	}
	return nil
}

// matching_configured reports whether any source for the background matching
// job is present.
func matching_configured(cfg *config.Config) bool {
	return cfg.Api.Tmdb_key != "" || cfg.Api.Opensubtitles_api_key != "" || cfg.Imdb_datasets_path != ""
}
