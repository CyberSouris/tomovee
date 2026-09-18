package database

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func Test_open_in_memory_applies_migrations(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	want := []int{1, 2, 3}
	if got, err := d.Migrations_applied(); err != nil {
		t.Fatalf("migrations applied: %v", err)
	} else if !reflect.DeepEqual(got, want) {
		t.Errorf("migrations applied = %v, want %v", got, want)
	}
}

func Test_open_creates_missing_parent_directory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "tomovee.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Migrations_applied(); err != nil {
		t.Fatalf("migrations applied: %v", err)
	}
}

func Test_open_creates_all_tables(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	want := []string{
		"schema_migrations", "config", "catalog_entry", "genre",
		"catalog_entry_genre", "series_metadata", "episode", "version",
		"audio_track", "subtitle_track", "lookup_cache", "library",
	}
	rows, err := d.Sql().Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing table %q", name)
		}
	}
}

func Test_open_is_idempotent_across_reopen(t *testing.T) {
	dir := t.TempDir()
	db_path := filepath.Join(dir, "lib.db")

	d1, err := Open(db_path)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	_ = d1.Close()

	d2, err := Open(db_path)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer d2.Close()

	want := []int{1, 2, 3}
	if got, err := d2.Migrations_applied(); err != nil {
		t.Fatalf("migrations applied: %v", err)
	} else if !reflect.DeepEqual(got, want) {
		t.Errorf("migrations applied after reopen = %v, want %v", got, want)
	}
}

func Test_ping(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Ping(context.Background()); err != nil {
		t.Errorf("ping: %v", err)
	}
}
