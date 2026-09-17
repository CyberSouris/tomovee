// Package config loads, validates, and normalizes Tomovee's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level application configuration. It is the merged result
// of built-in defaults, an optional YAML file, and any applied overrides.
type Config struct {
	Database_path    string      `yaml:"database_path"`
	Poster_cache_dir string      `yaml:"poster_cache_dir"`
	Listen           string      `yaml:"listen"`
	Scan_directories []string    `yaml:"scan_directories"`
	Api              Api_config  `yaml:"api"`
	Watch_enabled    bool        `yaml:"watch_enabled"`
	Scan             Scan_config `yaml:"scan"`
	// Imdb_datasets_path optionally points at an IMDb title.basics.tsv(.gz)
	// export used for offline, network-free enrichment when APIs are down.
	Imdb_datasets_path string `yaml:"imdb_datasets_path"`
}

// Api_config holds credentials and settings for the external matching APIs.
type Api_config struct {
	Tmdb_key                 string `yaml:"tmdb_key"`
	Opensubtitles_api_key    string `yaml:"opensubtitles_api_key"`
	Opensubtitles_username   string `yaml:"opensubtitles_username"`
	Opensubtitles_password   string `yaml:"opensubtitles_password"`
	Opensubtitles_user_agent string `yaml:"opensubtitles_user_agent"`
}

// Scan_config tunes the scanning pipeline.
type Scan_config struct {
	Quality_subdir_min_size int `yaml:"quality_subdir_min_size"`
	Min_file_size_mb        int `yaml:"min_file_size_mb"`
}

// Defaults returns a configuration populated with built-in defaults.
func Defaults() *Config {
	base_dir := user_data_dir()
	return &Config{
		Database_path:    filepath.Join(base_dir, "tomovee.db"),
		Poster_cache_dir: filepath.Join(base_dir, "posters"),
		Listen:           "127.0.0.1:8080",
		Api: Api_config{
			Opensubtitles_user_agent: "Tomovee/0.1 by Cyber Souris",
		},
		Scan: Scan_config{
			Quality_subdir_min_size: 2,
			Min_file_size_mb:        50,
		},
	}
}

// Default_config_path returns the default configuration file location used
// when no --config flag is given: $XDG_CONFIG_HOME/tomovee/config.yaml, else
// $HOME/.config/tomovee/config.yaml, else ./config.yaml.
func Default_config_path() string {
	if dir, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok && dir != "" {
		return filepath.Join(dir, "tomovee", "config.yaml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "tomovee", "config.yaml")
	}
	return "config.yaml"
}

// Load reads and parses the YAML config file at path, merging it over
// defaults. The config file is optional: a missing file is not an error; it
// produces a config with only defaults and nil. An unreadable file or one with
// invalid YAML is an error.
//
// Load validates the result; callers must also call the returned config's
// Validate if they want field-level checks after further modification.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Normalize(); err != nil {
		return nil, fmt.Errorf("normalize config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

// Normalize expands "~" and makes paths absolute where possible. It should be
// called after any programmatic modification before using the config.
func (c *Config) Normalize() error {
	var errs []string
	c.Database_path = normalize_path(c.Database_path)
	c.Poster_cache_dir = normalize_path(c.Poster_cache_dir)
	if c.Imdb_datasets_path != "" {
		c.Imdb_datasets_path = normalize_path(c.Imdb_datasets_path)
	}
	dirs := make([]string, 0, len(c.Scan_directories))
	for i, dir := range c.Scan_directories {
		norm := normalize_path(dir)
		if norm == "" {
			errs = append(errs, fmt.Sprintf("scan_directories[%d] is empty", i))
			continue
		}
		dirs = append(dirs, norm)
	}
	c.Scan_directories = dirs
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Validate checks field-level constraints and returns descriptive errors.
func (c *Config) Validate() error {
	var errs []string
	if c.Database_path == "" {
		errs = append(errs, "database_path must not be empty")
	}
	if c.Poster_cache_dir == "" {
		errs = append(errs, "poster_cache_dir must not be empty")
	}
	if c.Listen == "" {
		errs = append(errs, "listen must not be empty")
	}
	if len(c.Scan_directories) == 0 && !c.Watch_enabled {
		errs = append(errs, "at least one scan_directories entry is required (or enable folder watching)")
	}
	if c.Scan.Min_file_size_mb < 0 {
		errs = append(errs, "scan.min_file_size_mb must not be negative")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func normalize_path(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func user_data_dir() string {
	if dir, ok := os.LookupEnv("XDG_DATA_HOME"); ok && dir != "" {
		return filepath.Join(dir, "tomovee")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "tomovee")
	}
	return "tomovee"
}
