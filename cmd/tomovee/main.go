// Command tomovee is the movie and TV database daemon and one-shot scanner.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
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

// version is stamped at build time via -ldflags "-X main.version=<tag>".
var version = "0.1.0"

// build_logger returns a text handler logger writing to stderr at the given
// level (debug, info, warn, error; anything else falls back to info).
func build_logger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func main() {
	logger := build_logger("info")
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
  tomovee serve [--config PATH] [--log-level LEVEL]              run the daemon (REST API + web UI)
  tomovee scan  [--config PATH] [--log-level LEVEL] [--library NAME...]  run a one-shot scan and exit
  tomovee version                            print the version
  tomovee help                               show this help

  --config PATH    config file (default: %s)
  --log-level LEVEL  log verbosity: debug, info, warn, error (overrides config file)
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
	log_level := fs.String("log-level", "", "log verbosity: debug, info, warn, error (overrides config file)")
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
	if *log_level != "" {
		cfg.Log_level = *log_level
	}
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
	datasets atomic.Pointer[imdb_datasets.Index]
	status   *imdb_datasets.Tracker
}

// attach_datasets makes a freshly loaded offline datasets index part of the
// pipeline: the matcher's offline layer is activated immediately and the index
// is kept for shutdown cleanup.
func (p *pipeline) attach_datasets(index *imdb_datasets.Index) {
	p.datasets.Store(index)
	p.matcher.Set_offline(imdb_datasets.New_matcher_source(index))
}

// load_datasets opens the IMDb datasets in the background so the web service
// can start serving immediately. The offline matching layer and the episode
// enrichment service are attached once the index is ready; startup matching is
// then kicked off so it does not contend with the build for CPU during the
// index construction.
func (p *pipeline) load_datasets(logger *slog.Logger, path string, service *matching.Matching, on_ready func(), status *imdb_datasets.Tracker) {
	status.Set_has_path(path != "")
	progress := func(step imdb_datasets.Build_progress) {
		status.Observe(step)
		switch step.Step {
		case imdb_datasets.Build_stale:
			logger.Info("imdb datasets: export is newer than the index, rebuilding", "path", path)
		case imdb_datasets.Build_reuse:
			logger.Info("imdb datasets: index is up to date, reopening", "path", path)
		case imdb_datasets.Build_import:
			if step.Done {
				logger.Info("imdb datasets: imported", "dataset", step.Dataset, "rows", step.Rows)
			} else if step.Rows > 0 {
				logger.Info("imdb datasets: importing", "dataset", step.Dataset, "rows", step.Rows)
			} else {
				logger.Info("imdb datasets: importing dataset", "dataset", step.Dataset)
			}
		case imdb_datasets.Build_ready:
			logger.Info("imdb datasets: index rebuilt")
		}
	}
	go func() {
		index, err := imdb_datasets.Open_with_progress(path, logger, progress)
		if err != nil {
			status.Fail(err.Error())
			logger.Error("imdb datasets: not available for offline matching", "path", path, "error", err)
			if on_ready != nil {
				on_ready()
			}
			return
		}
		p.attach_datasets(index)
		service.Set_datasets(index)
		logger.Info("imdb datasets: ready for offline matching", "path", path, "titles", index.Count())
		if on_ready != nil {
			on_ready()
		}
	}()
}

// stream_datasets downloads the IMDb datasets over the network straight into a
// new index at index_db_path (decompressing each export as it streams, never
// saving the originals to disk) in the background, then attaches the built
// index to the matching services. Startup matching is deferred to on_ready so
// it does not contend with the build for CPU during the index construction.
func (p *pipeline) stream_datasets(logger *slog.Logger, index_db_path string, service *matching.Matching, on_ready func(), status *imdb_datasets.Tracker) {
	status.Set_has_path(true)
	progress := func(step imdb_datasets.Build_progress) {
		status.Observe(step)
		switch step.Step {
		case imdb_datasets.Build_stale:
			logger.Info("imdb datasets: fetching fresh exports", "index", index_db_path)
		case imdb_datasets.Build_import:
			if step.Done {
				logger.Info("imdb datasets: imported", "dataset", step.Dataset, "rows", step.Rows)
			} else if step.Rows > 0 {
				logger.Info("imdb datasets: importing", "dataset", step.Dataset, "rows", step.Rows)
			} else {
				logger.Info("imdb datasets: importing dataset", "dataset", step.Dataset)
			}
		case imdb_datasets.Build_ready:
			logger.Info("imdb datasets: index built")
		}
	}
	go func() {
		index, err := imdb_datasets.Open_stream(context.Background(), index_db_path, imdb_datasets.Datasets_base_url, logger, progress)
		if err != nil {
			status.Fail(err.Error())
			logger.Error("imdb datasets: import failed", "index", index_db_path, "error", err)
			if on_ready != nil {
				on_ready()
			}
			return
		}
		p.attach_datasets(index)
		service.Set_datasets(index)
		logger.Info("imdb datasets: ready for offline matching", "index", index_db_path, "titles", index.Count())
		if on_ready != nil {
			on_ready()
		}
	}()
}

// build_matcher_sources builds the online matching sources (OpenSubtitles and
// TMDB) from cfg. TMDB is wrapped in the metadata cache when a store is
// present so repeated lookups stay cheap.
func build_matcher_sources(store *database.Store, cfg *config.Config, logger *slog.Logger) (matcher.Subtitles_source, matcher.Metadata_source) {
	var subtitles matcher.Subtitles_source
	if cfg.Api.Opensubtitles_api_key != "" {
		subtitles = opensubtitles.New(opensubtitles.Config{
			Api_key:    cfg.Api.Opensubtitles_api_key,
			User_agent: cfg.Api.Opensubtitles_user_agent,
		})
	}
	var metadata matcher.Metadata_source
	if cfg.Api.Tmdb_key != "" {
		metadata = tmdb.New(tmdb.Config{Api_key: cfg.Api.Tmdb_key})
		if store != nil {
			metadata = metacache.New(metadata, store, metacache.Default_ttl, logger)
		}
	}
	return subtitles, metadata
}

// build_pipeline assembles the matching pipeline from the configured API keys
// and the optional IMDb datasets directory or file. Missing keys are warnings,
// not errors: the pipeline degrades gracefully per the specification.
//
// The IMDb datasets are deliberately left out here: building their index can
// take a long time, so cmd_serve loads them in the background and attaches
// them with pipeline.load_datasets once they are ready.
func build_pipeline(logger *slog.Logger, cfg *config.Config, store *database.Store) (*pipeline, error) {
	opts := matcher.Options{Logger: logger}
	opts.Subtitles, opts.Metadata = build_matcher_sources(store, cfg, logger)
	if opts.Subtitles == nil {
		logger.Warn("opensubtitles api key not configured; hash lookup disabled")
	}
	if opts.Metadata == nil {
		logger.Warn("tmdb api key not configured; falling back to offline matching")
	}

	return &pipeline{
		matcher:  matcher.New(opts),
		metadata: opts.Metadata,
		status:   imdb_datasets.New_tracker(),
	}, nil
}

// reload_sources re-reads the persisted source overrides (API keys saved from
// the web UI) from the database, merges them over the config, and swaps the
// live online matching sources (TMDB, OpenSubtitles) onto the running matcher
// without a restart. The offline IMDb datasets layer is unaffected.
func (p *pipeline) reload_sources(logger *slog.Logger, store *database.Store, cfg *config.Config) error {
	overrides, err := store.Config_all(context.Background())
	if err != nil {
		return err
	}
	if len(overrides) > 0 {
		config.Apply_overrides(cfg, overrides)
	}
	subtitles, metadata := build_matcher_sources(store, cfg, logger)
	p.matcher.Set_sources(subtitles, metadata)
	p.metadata = metadata
	logger.Info("sources reloaded",
		"tmdb", cfg.Api.Tmdb_key != "",
		"opensubtitles", cfg.Api.Opensubtitles_api_key != "")
	return nil
}

// listen_address confines loopback-style listen hosts to a concrete loopback
// address, so a config that asks to listen on the loopback never binds to
// non-loopback interfaces (e.g. the name "localhost" resolving through a
// custom resolver or VPN). Explicit all-interface ("", "0.0.0.0", "::") and
// other concrete addresses are passed through unchanged.
func listen_address(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

func cmd_serve(logger *slog.Logger, args []string) error {
	cfg, err := load_config(args)
	if err != nil {
		return err
	}
	logger = build_logger(cfg.Log_level)
	db, err := open_database(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := os.MkdirAll(cfg.Poster_cache_dir, 0o755); err != nil {
		return err
	}

	store := database.New_store(db)

	// Settings changed from the web UI (API keys, datasets path) are persisted
	// in the config table; merge them over the config file so they take effect
	// from this run onward.
	if overrides, err := store.Config_all(context.Background()); err != nil {
		return err
	} else if len(overrides) > 0 {
		config.Apply_overrides(cfg, overrides)
	}

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

	matching_service := matching.New(store, pipe.matcher, nil, logger)

	// start_matching kicks off an automatic matching run; it is assigned after
	// the webserver exists, but the Stream_datasets hook below can be triggered
	// from the Settings page at any later point.
	var start_matching func()

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
		Datasets: pipe.status,
		Stream_datasets: func(index_db_path string) {
			pipe.stream_datasets(logger, index_db_path, matching_service, start_matching, pipe.status)
		},
		Reload: func() error {
			return pipe.reload_sources(logger, store, cfg)
		},
	})

	start_matching = func() {
		if cfg.Match_on_start && matching_configured(cfg) {
			logger.Info("starting background matching at startup")
			server.Auto_match()
		}
	}

	watch_ctx, stop_watch := context.WithCancel(context.Background())
	defer stop_watch()
	go func() {
		w := watcher.New(store, runner, watcher.Options{
			Default_enabled: cfg.Watch_enabled,
			Logger:          logger,
			Run_scan:        server.Watch_scan,
		})
		if err := w.Run(watch_ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("folder watcher stopped", "error", err)
		}
	}()

	bind := listen_address(cfg.Listen)
	http_server := &http.Server{
		Addr:              bind,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		logger.Info("serving", "listen", cfg.Listen, "bind", bind, "web_ui", true)
		if err := http_server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	// Building an imdb datasets index can take minutes and peg a core, so it
	// must not delay the web service. Load it in the background and start the
	// startup matching run only once it (or the online sources) are ready.
	if cfg.Imdb_datasets_path != "" {
		logger.Info("loading imdb datasets in the background", "path", cfg.Imdb_datasets_path)
		pipe.load_datasets(logger, cfg.Imdb_datasets_path, matching_service, start_matching, pipe.status)
	} else {
		start_matching()
	}

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
	shutdown_err := http_server.Shutdown(shutdown_ctx)
	if index := pipe.datasets.Load(); index != nil {
		_ = index.Close()
	}
	return shutdown_err
}

func cmd_scan(logger *slog.Logger, args []string) error {
	var libraries string_list
	cfg, err := load_config(args, func(fs *flag.FlagSet) {
		fs.Var(&libraries, "library", "scan only the named library (repeatable)")
	})
	if err != nil {
		return err
	}
	logger = build_logger(cfg.Log_level)
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
