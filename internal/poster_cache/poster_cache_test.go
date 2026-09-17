package poster_cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func new_test_cache(t *testing.T, base_url string) (*Cache, string) {
	t.Helper()
	dir := t.TempDir()
	cache := New(Options{Dir: dir, Base_url: base_url})
	return cache, dir
}

func Test_ensure_downloads_and_caches(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/w500/poster.jpg" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer server.Close()

	cache, dir := new_test_cache(t, server.URL)
	ctx := context.Background()

	path, err := cache.Ensure(ctx, 42, "/poster.jpg")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if filepath.Base(path) != "42.jpg" {
		t.Errorf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Errorf("data = %q", data)
	}

	if _, err := cache.Ensure(ctx, 42, "/poster.jpg"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1", hits)
	}

	removed, err := cache.Prune(map[int64]bool{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "42.jpg")); !os.IsNotExist(err) {
		t.Errorf("poster was not removed")
	}
}

func Test_ensure_without_poster(t *testing.T) {
	cache, _ := new_test_cache(t, "http://unused")
	if _, err := cache.Ensure(context.Background(), 1, ""); err != Err_no_poster {
		t.Fatalf("err = %v, want Err_no_poster", err)
	}
}

func Test_prune_keeps_referenced(t *testing.T) {
	cache, dir := new_test_cache(t, "http://unused")
	for _, name := range []string{"1.jpg", "2.jpg", "notes.txt", "poster-123.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	removed, err := cache.Prune(map[int64]bool{1: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	for _, name := range []string{"1.jpg", "notes.txt", "poster-123.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should have been kept: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "2.jpg")); !os.IsNotExist(err) {
		t.Errorf("2.jpg should have been removed")
	}
}

func Test_ensure_rejects_non_200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cache, _ := new_test_cache(t, server.URL)
	if _, err := cache.Ensure(context.Background(), 1, "/missing.jpg"); err == nil {
		t.Fatal("expected error for non-200 response")
	}
}
