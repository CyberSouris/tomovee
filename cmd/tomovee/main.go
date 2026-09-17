// Command tomovee is the movie and TV database daemon and one-shot scanner.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
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
  tomovee serve [--config PATH]   run the daemon (REST API + web UI)
  tomovee scan  [--config PATH]   run a one-shot scan and exit
  tomovee version                 print the version
  tomovee help                    show this help

  --config PATH   config file (default: %s)
`, version, config.Default_config_path())
}

func load_config(args []string) (*config.Config, error) {
	fs := flag.NewFlagSet("tomovee", flag.ContinueOnError)
	config_path := fs.String("config", "", "path to the YAML config file")
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

func cmd_serve(logger *slog.Logger, args []string) error {
	cfg, err := load_config(args)
	if err != nil {
		return err
	}
	if _, err := open_database(cfg); err != nil {
		return err
	}
	logger.Warn("serve mode is not implemented yet; nothing to do", "listen", cfg.Listen)
	return nil
}

func cmd_scan(logger *slog.Logger, args []string) error {
	cfg, err := load_config(args)
	if err != nil {
		return err
	}
	if _, err := open_database(cfg); err != nil {
		return err
	}
	logger.Warn("scan mode is not implemented yet; nothing to do", "dirs", cfg.Scan_directories)
	return nil
}
