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
scan_directories:
  - /mnt/media/movies
  - /mnt/media/shows
api:
  tmdb_key: abc123
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
	if len(cfg.Scan_directories) != 2 {
		t.Fatalf("scan_directories = %v, want 2 entries", cfg.Scan_directories)
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
	cfg.Scan_directories = []string{"~/movies", "/absolute/path"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Database_path != filepath.Join(home, "library.db") {
		t.Errorf("database_path = %q, want %q", cfg.Database_path, filepath.Join(home, "library.db"))
	}
	if cfg.Scan_directories[0] != filepath.Join(home, "movies") {
		t.Errorf("scan dir 0 = %q, want %q", cfg.Scan_directories[0], filepath.Join(home, "movies"))
	}
}

func Test_validate_requires_scan_dir_or_watch(t *testing.T) {
	cfg := Defaults()
	cfg.Scan_directories = nil
	cfg.Watch_enabled = false
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when no scan dirs and no watch")
	}
	if !strings.Contains(err.Error(), "scan_directories") {
		t.Errorf("error should mention scan_directories, got: %v", err)
	}

	cfg.Watch_enabled = true
	if err := cfg.Validate(); err != nil {
		t.Errorf("no error expected with watch enabled, got: %v", err)
	}
}
