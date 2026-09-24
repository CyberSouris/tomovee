package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/metacache"
)

func write_config(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func Test_string_list(t *testing.T) {
	var list string_list
	if got := list.String(); got != "" {
		t.Errorf("String() on empty = %q", got)
	}
	if err := list.Set("Movies"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := list.Set("Series"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := list.String(); got != "Movies,Series" {
		t.Errorf("String() = %q, want %q", got, "Movies,Series")
	}
}

func capture_stderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = writer
	fn()
	_ = writer.Close()
	os.Stderr = old
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(out)
}

func Test_build_logger(t *testing.T) {
	cases := []struct {
		name     string
		level    string
		emit     func(*slog.Logger)
		want_log bool
	}{
		{"debug at debug level", "debug", func(l *slog.Logger) { l.Debug("probe") }, true},
		{"info at info level", "info", func(l *slog.Logger) { l.Info("probe") }, true},
		{"warn at warn level", "warn", func(l *slog.Logger) { l.Warn("probe") }, true},
		{"error at error level", "error", func(l *slog.Logger) { l.Error("probe") }, true},
		{"info suppressed at error level", "error", func(l *slog.Logger) { l.Info("probe") }, false},
		{"info suppressed at warn level", "warn", func(l *slog.Logger) { l.Info("probe") }, false},
		{"degenerate case tolerated", "  ERROR ", func(l *slog.Logger) { l.Error("probe") }, true},
		{"unknown level falls back to info", "verbose", func(l *slog.Logger) { l.Info("probe") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capture_stderr(t, func() { tc.emit(build_logger(tc.level)) })
			got_log := strings.Contains(got, "probe")
			if got_log != tc.want_log {
				t.Errorf("logged=%v, want %v (output %q)", got_log, tc.want_log, got)
			}
		})
	}
}

func Test_build_matcher_sources(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	plain := &config.Config{}
	subtitles, metadata := build_matcher_sources(store, plain, logger)
	if subtitles != nil || metadata != nil {
		t.Errorf("no keys: subtitles=%v metadata=%v, want both nil", subtitles, metadata)
	}

	only_tmdb := &config.Config{Api: config.Api_config{Tmdb_key: "key"}}
	subtitles, metadata = build_matcher_sources(nil, only_tmdb, logger)
	if subtitles != nil {
		t.Errorf("subtitles configured with no opensubtitles key")
	}
	if metadata == nil {
		t.Fatal("tmdb metadata nil with a key")
	}
	if _, ok := metadata.(*metacache.Cache); ok {
		t.Errorf("metadata wrapped in cache without a store")
	}

	both := &config.Config{Api: config.Api_config{
		Tmdb_key:              "tmdb",
		Opensubtitles_api_key: "os",
	}}
	subtitles, metadata = build_matcher_sources(store, both, logger)
	if subtitles == nil {
		t.Error("opensubtitles source nil with a key")
	}
	if _, ok := metadata.(*metacache.Cache); !ok {
		t.Errorf("metadata = %T, want *metacache.Cache", metadata)
	}
}

func Test_build_pipeline(t *testing.T) {
	ctx := context.Background()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := store.Ensure_library(ctx, "Movies", "/media/movies", false); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	if err := store.Upsert_library(ctx, "Movies", "/media/movies", true); err != nil {
		t.Fatalf("enable library: %v", err)
	}

	cfg := &config.Config{
		Libraries:     map[string]string{"Movies": "/media/movies"},
		Watch_enabled: true,
	}
	p, err := build_pipeline(logger, cfg, store)
	if err != nil {
		t.Fatalf("build_pipeline: %v", err)
	}
	if p.matcher == nil {
		t.Error("matcher nil")
	}
	if p.metadata != nil {
		t.Errorf("metadata = %v, want nil without keys", p.metadata)
	}
	if p.status == nil {
		t.Error("datasets status nil")
	}
	if err := p.reload_sources(logger, store, cfg); err != nil {
		t.Fatalf("reload_sources without overrides: %v", err)
	}
	if cfg.Api.Tmdb_key != "" {
		t.Errorf("tmdb key unexpectedly set: %q", cfg.Api.Tmdb_key)
	}
}

func Test_reload_sources_applies_overrides(t *testing.T) {
	ctx := context.Background()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{}
	p, err := build_pipeline(logger, cfg, store)
	if err != nil {
		t.Fatalf("build_pipeline: %v", err)
	}
	if err := store.Config_set(ctx, config.Override_tmdb_key, "saved-key"); err != nil {
		t.Fatalf("config_set: %v", err)
	}
	if err := p.reload_sources(logger, store, cfg); err != nil {
		t.Fatalf("reload_sources: %v", err)
	}
	if cfg.Api.Tmdb_key != "saved-key" {
		t.Errorf("tmdb key = %q, want %q", cfg.Api.Tmdb_key, "saved-key")
	}
	if p.metadata == nil {
		t.Error("metadata nil after override reload")
	}
}

func Test_load_config(t *testing.T) {
	path := write_config(t, `
watch_enabled: true
listen: "127.0.0.1:9090"
log_level: error
`)
	cfg, err := load_config([]string{"--config", path})
	if err != nil {
		t.Fatalf("load_config: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9090" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.Log_level != "error" {
		t.Errorf("log_level = %q", cfg.Log_level)
	}

	cfg, err = load_config([]string{"--config", path, "--log-level", "debug"})
	if err != nil {
		t.Fatalf("load_config with override: %v", err)
	}
	if cfg.Log_level != "debug" {
		t.Errorf("log_level override = %q, want debug", cfg.Log_level)
	}
}

func Test_load_config_errors(t *testing.T) {
	if _, err := load_config([]string{"--bogus"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if _, err := load_config([]string{"extra"}); err == nil {
		t.Error("positional argument accepted")
	}
	bad := write_config(t, "\tnot: [valid yaml")
	if _, err := load_config([]string{"--config", bad}); err == nil {
		t.Error("invalid yaml accepted")
	}
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := load_config([]string{"--config", missing}); err == nil {
		t.Error("missing config with no libraries accepted")
	}
}

func Test_load_config_extra_flags(t *testing.T) {
	path := write_config(t, "watch_enabled: true\n")
	var libraries string_list
	cfg, err := load_config([]string{"--config", path, "--library", "Movies", "--library", "TV"},
		func(fs *flag.FlagSet) { fs.Var(&libraries, "library", "scan a library") })
	if err != nil {
		t.Fatalf("load_config: %v", err)
	}
	if cfg == nil {
		t.Fatal("config nil")
	}
	if got := libraries.String(); got != "Movies,TV" {
		t.Errorf("extra flag parsed to %q, want %q", got, "Movies,TV")
	}
}

func Test_register_libraries(t *testing.T) {
	ctx := context.Background()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)

	cfg := &config.Config{Libraries: map[string]string{
		"Movies": "/media/movies",
		"Series": "/media/series",
	}}
	if err := register_libraries(ctx, store, cfg, true); err != nil {
		t.Fatalf("register_libraries: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(libraries) != 2 {
		t.Fatalf("libraries = %d, want 2", len(libraries))
	}
	for _, library := range libraries {
		if !library.Enabled {
			t.Errorf("library %q not enabled", library.Name)
		}
	}

	cfg.Watch_enabled = true
	cfg.Libraries = map[string]string{"New": "/media/new"}
	if err := register_libraries(ctx, store, cfg, false); err != nil {
		t.Fatalf("second register: %v", err)
	}
	libraries, err = store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(libraries) != 3 {
		t.Errorf("libraries = %d, want 3", len(libraries))
	}
}

func Test_register_libraries_error(t *testing.T) {
	ctx := context.Background()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := database.New_store(d)
	_ = d.Close()
	cfg := &config.Config{Libraries: map[string]string{"Movies": "/media"}}
	if err := register_libraries(ctx, store, cfg, true); err == nil {
		t.Error("registering into a closed store succeeded")
	} else if !strings.Contains(err.Error(), "Movies") {
		t.Errorf("error %q does not name the failing library", err)
	}
}

func Test_open_database(t *testing.T) {
	cfg := &config.Config{Database_path: ":memory:"}
	db, err := open_database(cfg)
	if err != nil {
		t.Fatalf("open_database: %v", err)
	}
	defer db.Close()
	applied, err := db.Migrations_applied()
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if len(applied) == 0 {
		t.Error("no migrations applied")
	}
}

func Test_usage(t *testing.T) {
	out := capture_stderr(t, usage)
	if !strings.Contains(out, "Usage:") {
		t.Error("usage missing Usage banner")
	}
	if !strings.Contains(out, config.Default_config_path()) {
		t.Errorf("usage missing default config path %q", config.Default_config_path())
	}
}

func Test_listen_address(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"localhost:8080", "127.0.0.1:8080"},
		{"LOCALHOST:8080", "127.0.0.1:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"::1:8080", "::1:8080"},
		{"[::1]:8080", "[::1]:8080"},
		{"192.168.1.5:80", "192.168.1.5:80"},
		{"not-an-address", "not-an-address"},
	}
	for _, tc := range cases {
		if got := listen_address(tc.in); got != tc.want {
			t.Errorf("listen_address(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func Test_matching_configured(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nothing", &config.Config{}, false},
		{"tmdb", &config.Config{Api: config.Api_config{Tmdb_key: "k"}}, true},
		{"opensubtitles", &config.Config{Api: config.Api_config{Opensubtitles_api_key: "k"}}, true},
		{"datasets", &config.Config{Imdb_datasets_path: "/path"}, true},
	}
	for _, tc := range cases {
		if got := matching_configured(tc.cfg); got != tc.want {
			t.Errorf("%s: matching_configured = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func Test_cmd_serve_config_error(t *testing.T) {
	if err := cmd_serve([]string{"--bogus"}); err == nil {
		t.Error("cmd_serve accepted an unknown flag")
	}
}

func Test_cmd_serve_poster_dir_error(t *testing.T) {
	poster_file := filepath.Join(t.TempDir(), "posters")
	if err := os.WriteFile(poster_file, []byte("x"), 0o644); err != nil {
		t.Fatalf("create poster file: %v", err)
	}
	path := write_config(t, `
watch_enabled: true
poster_cache_dir: `+poster_file+`
`)
	if err := cmd_serve([]string{"--config", path}); err == nil {
		t.Error("cmd_serve accepted a poster cache dir that is a file")
	}
}

func Test_cmd_scan_empty_library(t *testing.T) {
	dir := t.TempDir()
	path := write_config(t, `
watch_enabled: true
libraries:
  Movies: "`+dir+`"
scan:
  min_file_size_mb: 1
`)
	if err := cmd_scan([]string{"--config", path}); err != nil {
		t.Fatalf("cmd_scan: %v", err)
	}
}
