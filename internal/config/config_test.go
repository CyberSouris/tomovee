package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write_temp_config(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func Test_Defaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("expected default listen 127.0.0.1:8080, got %q", cfg.Listen)
	}
	if cfg.Api.Opensubtitles_user_agent == "" {
		t.Error("expected a default OpenSubtitles user agent")
	}
	if cfg.Database_path == "" || cfg.Poster_cache_dir == "" {
		t.Error("expected default database and poster paths")
	}
}

func Test_load_full_config(t *testing.T) {
	path := write_temp_config(t, `
database_path: /tmp/library.db
poster_cache_dir: /tmp/posters
listen: 0.0.0.0:9090
libraries:
  movies: /mnt/media/movies
  shows: /mnt/media/shows
api:
  tmdb_key: abc123
  opensubtitles_api_key: os-key
  opensubtitles_username: user
  opensubtitles_password: pass
watch_enabled: true
scan:
  min_file_size_mb: 120
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:9090" {
		t.Errorf("listen = %q, want 0.0.0.0:9090", cfg.Listen)
	}
	if cfg.Api.Tmdb_key != "abc123" {
		t.Errorf("tmdb_key = %q, want abc123", cfg.Api.Tmdb_key)
	}
	if cfg.Api.Opensubtitles_api_key != "os-key" {
		t.Errorf("opensubtitles_api_key = %q, want os-key", cfg.Api.Opensubtitles_api_key)
	}
	if len(cfg.Libraries) != 2 || cfg.Libraries["movies"] != "/mnt/media/movies" {
		t.Fatalf("libraries = %v, want movies and shows", cfg.Libraries)
	}
	if cfg.Scan.Min_file_size_mb != 120 {
		t.Errorf("min_file_size_mb = %d, want 120", cfg.Scan.Min_file_size_mb)
	}
	if cfg.Database_path != "/tmp/library.db" {
		t.Errorf("database_path = %q, want /tmp/library.db", cfg.Database_path)
	}
}

func Test_load_missing_file_returns_defaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("load missing file: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("expected defaults for missing file, got listen %q", cfg.Listen)
	}
}

func Test_load_invalid_yaml_is_error(t *testing.T) {
	path := write_temp_config(t, "database_path: [unclosed")
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid YAML, got none")
	}
}

func Test_normalize_expands_tilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home dir: %v", err)
	}
	cfg := Defaults()
	cfg.Database_path = "~/library.db"
	cfg.Libraries = map[string]string{"movies": "~/movies", "other": "/absolute/path"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Database_path != filepath.Join(home, "library.db") {
		t.Errorf("database_path = %q, want %q", cfg.Database_path, filepath.Join(home, "library.db"))
	}
	if cfg.Libraries["movies"] != filepath.Join(home, "movies") {
		t.Errorf("library movies = %q, want %q", cfg.Libraries["movies"], filepath.Join(home, "movies"))
	}
	if cfg.Libraries["other"] != "/absolute/path" {
		t.Errorf("library other = %q, want /absolute/path", cfg.Libraries["other"])
	}
}

func Test_normalize_rejects_empty_library(t *testing.T) {
	cfg := Defaults()
	cfg.Libraries = map[string]string{"empty": "   ", "good": "/media"}
	err := cfg.Normalize()
	if err == nil {
		t.Fatal("expected error for empty library path")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention the bad library, got: %v", err)
	}
}

func Test_apply_overrides_merges_settings_over_config(t *testing.T) {
	cfg := Defaults()
	cfg.Api.Tmdb_key = "file-key"
	cfg.Api.Opensubtitles_api_key = "file-os"
	cfg.Api.Opensubtitles_username = "file-user"
	cfg.Api.Opensubtitles_password = "file-pass"
	cfg.Imdb_datasets_path = "/file/datasets"

	Apply_overrides(cfg, map[string]string{
		Override_tmdb_key:               "db-key",
		Override_opensubtitles_username: "db-user",
		Override_imdb_datasets_path:     "/db/datasets",
		"unrelated.setting":             "ignored",
	})
	if cfg.Api.Tmdb_key != "db-key" {
		t.Errorf("tmdb_key = %q, want db-key", cfg.Api.Tmdb_key)
	}
	if cfg.Api.Opensubtitles_username != "db-user" {
		t.Errorf("opensubtitles_username = %q, want db-user", cfg.Api.Opensubtitles_username)
	}
	if cfg.Api.Opensubtitles_api_key != "file-os" {
		t.Errorf("opensubtitles_api_key = %q, want file-os (unset override)", cfg.Api.Opensubtitles_api_key)
	}
	if cfg.Api.Opensubtitles_password != "file-pass" {
		t.Errorf("opensubtitles_password = %q, want file-pass", cfg.Api.Opensubtitles_password)
	}
	if cfg.Imdb_datasets_path != "/db/datasets" {
		t.Errorf("imdb_datasets_path = %q, want /db/datasets", cfg.Imdb_datasets_path)
	}
}

func Test_apply_overrides_can_clear_a_setting(t *testing.T) {
	cfg := Defaults()
	cfg.Api.Tmdb_key = "file-key"
	Apply_overrides(cfg, map[string]string{Override_tmdb_key: ""})
	if cfg.Api.Tmdb_key != "" {
		t.Errorf("tmdb_key = %q, want empty after clear", cfg.Api.Tmdb_key)
	}
}

func Test_validate_log_level(t *testing.T) {
	cfg := Defaults()
	cfg.Libraries = map[string]string{"movies": "/media/movies"}
	for _, level := range []string{"debug", "info", "warn", "error", "DEBUG", "Info"} {
		cfg.Log_level = level
		if err := cfg.Validate(); err != nil {
			t.Errorf("log_level %q should validate, got: %v", level, err)
		}
	}
	for _, level := range []string{"verbose", "loud"} {
		cfg.Log_level = level
		if err := cfg.Validate(); err == nil {
			t.Errorf("log_level %q should be rejected", level)
		}
	}
	cfg.Log_level = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty log_level is valid (falls back to info), got: %v", err)
	}
	cfg.Log_level = "info"
	if err := cfg.Validate(); err != nil {
		t.Errorf("default log_level should validate, got: %v", err)
	}
}

func Test_load_log_level(t *testing.T) {
	path := write_temp_config(t, `
database_path: /tmp/library.db
poster_cache_dir: /tmp/posters
listen: 127.0.0.1:8080
libraries:
  movies: /mnt/media/movies
log_level: debug
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Log_level != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.Log_level)
	}
}

func Test_validate_requires_library_or_watch(t *testing.T) {
	cfg := Defaults()
	cfg.Libraries = nil
	cfg.Watch_enabled = false
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when no libraries and no watch")
	}
	if !strings.Contains(err.Error(), "library") {
		t.Errorf("error should mention library, got: %v", err)
	}

	cfg.Libraries = map[string]string{"movies": "/media/movies"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("no error expected with a library, got: %v", err)
	}

	cfg.Libraries = nil
	cfg.Watch_enabled = true
	if err := cfg.Validate(); err != nil {
		t.Errorf("no error expected with watch enabled, got: %v", err)
	}
}

func Test_default_config_path_prefers_xdg_config_home(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	if got := Default_config_path(); got != filepath.Join("/xdg/config", "tomovee", "config.yaml") {
		t.Errorf("Default_config_path() = %q, want the XDG location", got)
	}
	// An empty value is no value at all, so the home directory is used.
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := Default_config_path(); got != filepath.Join(home, ".config", "tomovee", "config.yaml") {
		t.Errorf("Default_config_path() = %q, want the home location", got)
	}
	// Without a home directory to speak of, the working directory is the only
	// place left.
	t.Setenv("HOME", "")
	if got := Default_config_path(); got != "config.yaml" {
		t.Errorf("Default_config_path() = %q, want the bare file name", got)
	}
}

func Test_user_data_dir_prefers_xdg_data_home(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got := user_data_dir(); got != filepath.Join("/xdg/data", "tomovee") {
		t.Errorf("user_data_dir() = %q, want the XDG location", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := user_data_dir(); got != filepath.Join(home, ".local", "share", "tomovee") {
		t.Errorf("user_data_dir() = %q, want the home location", got)
	}
	t.Setenv("HOME", "")
	if got := user_data_dir(); got != "tomovee" {
		t.Errorf("user_data_dir() = %q, want the bare directory name", got)
	}
}
