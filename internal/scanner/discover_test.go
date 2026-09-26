package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func write_file(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func build_tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write_file(t, filepath.Join(root, "Movie.2020.1080p.mkv"), 2048)
	write_file(t, filepath.Join(root, "Show.S01E02.720p.mkv"), 2048)
	write_file(t, filepath.Join(root, "sample.mkv"), 2048)
	write_file(t, filepath.Join(root, "tiny.mkv"), 10)
	write_file(t, filepath.Join(root, ".hidden.mkv"), 2048)
	write_file(t, filepath.Join(root, ".hidden_dir", "inside.mkv"), 2048)
	write_file(t, filepath.Join(root, "sub", "Nested.Movie.2019.mkv"), 2048)
	write_file(t, filepath.Join(root, "sub", "deep", "Deeper.2018.mkv"), 2048)
	write_file(t, filepath.Join(root, "notes.txt"), 2048)
	return root
}

func names_of(files []Found_file) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func has_name(files []Found_file, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func Test_discover_recursive_default(t *testing.T) {
	root := build_tree(t)
	files, err := Discover(context.Background(), Discover_options{Root: root})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	got := names_of(files)
	want := []string{"Deeper.2018.mkv", "Movie.2020.1080p.mkv", "Nested.Movie.2019.mkv", "Show.S01E02.720p.mkv", "tiny.mkv"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}

	for _, f := range files {
		switch f.Name {
		case "Show.S01E02.720p.mkv":
			if f.Media_type != Series || f.Episode == nil || f.Episode.Season != 1 || f.Episode.Episode != 2 {
				t.Errorf("series classification wrong: %+v", f)
			}
		case "Movie.2020.1080p.mkv", "Nested.Movie.2019.mkv", "Deeper.2018.mkv", "tiny.mkv":
			if f.Media_type != Movie {
				t.Errorf("%s should be a movie, got %v", f.Name, f.Media_type)
			}
		}
	}
}

func Test_discover_sets_series_root(t *testing.T) {
	root := t.TempDir()
	write_file(t, filepath.Join(root, "Show.Name", "Season 1", "Show.S01E01.mkv"), 2048)
	write_file(t, filepath.Join(root, "Flat.Show.S01E02.mkv"), 2048)

	files, err := Discover(context.Background(), Discover_options{Root: root})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	with_root := 0
	for _, f := range files {
		if f.Name == "Show.S01E01.mkv" {
			with_root++
			want := filepath.Join(root, "Show.Name")
			if f.Series_root != want {
				t.Errorf("Series_root = %q, want %q", f.Series_root, want)
			}
		}
		if f.Name == "Flat.Show.S01E02.mkv" && f.Series_root != "" {
			t.Errorf("flat episode at library root should have no series root, got %q", f.Series_root)
		}
	}
	if with_root != 1 {
		t.Fatalf("nested episode not discovered, files = %v", names_of(files))
	}
}

func Test_discover_skips_hidden_and_junk(t *testing.T) {
	root := build_tree(t)
	files, err := Discover(context.Background(), Discover_options{Root: root})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, name := range []string{"sample.mkv", "notes.txt", ".hidden.mkv", "inside.mkv"} {
		if has_name(files, name) {
			t.Errorf("discovery should have skipped %q", name)
		}
	}
}

func Test_discover_exclude_patterns(t *testing.T) {
	root := build_tree(t)
	files, err := Discover(context.Background(), Discover_options{
		Root:             root,
		Exclude_patterns: []string{"*.txt", "sub/deep/*", "Show*"},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, name := range []string{"notes.txt", "Deeper.2018.mkv", "Show.S01E02.720p.mkv"} {
		if has_name(files, name) {
			t.Errorf("%q should have been excluded", name)
		}
	}
	if !has_name(files, "Nested.Movie.2019.mkv") {
		t.Error("excluding sub/deep/* should leave the rest of sub/ alone")
	}
}

func Test_is_excluded_matches_the_whole_relative_path(t *testing.T) {
	patterns := []string{"*.txt", "sub/deep/*"}
	for path, want := range map[string]bool{
		"notes.txt":      true,
		"sub/deep/a.mkv": true,
		"sub/other.mkv":  false,
		"movie.mkv":      false,
		"a.txt.bak":      false,
	} {
		if got := is_excluded(path, patterns); got != want {
			t.Errorf("is_excluded(%q) = %v, want %v", path, got, want)
		}
	}
	// No patterns means nothing is excluded.
	if is_excluded("anything.mkv", nil) {
		t.Error("a path must not be excluded without patterns")
	}
}

func Test_discover_size_filter(t *testing.T) {
	root := build_tree(t)
	files, err := Discover(context.Background(), Discover_options{Root: root, Min_size_bytes: 1000})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if has_name(files, "tiny.mkv") {
		t.Error("tiny.mkv should be filtered by size")
	}
	if !has_name(files, "Movie.2020.1080p.mkv") {
		t.Error("large movie should pass size filter")
	}
}

func Test_discover_flat(t *testing.T) {
	root := build_tree(t)
	files, err := Discover(context.Background(), Discover_options{Root: root, Flat: true})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if has_name(files, "Nested.Movie.2019.mkv") || has_name(files, "Deeper.2018.mkv") {
		t.Errorf("flat discovery should not descend: %v", names_of(files))
	}
	if !has_name(files, "Movie.2020.1080p.mkv") {
		t.Errorf("expected root-level movie, got %v", names_of(files))
	}
}

func Test_discover_missing_root_is_error(t *testing.T) {
	if _, err := Discover(context.Background(), Discover_options{Root: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func Test_discover_empty_root_is_error(t *testing.T) {
	if _, err := Discover(context.Background(), Discover_options{}); err == nil {
		t.Fatal("expected error for empty root")
	}
}
