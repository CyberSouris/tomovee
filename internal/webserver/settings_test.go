package webserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/webui"
)

func discard_logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mem_store(t *testing.T) *database.Store {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return database.New_store(d)
}

func Test_settings_override_persisted_and_read_back(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()

	put := do_request(t, server, http.MethodPut, "/api/v1/settings",
		`{"tmdb_key":"tmdb-2","opensubtitles_api_key":"os-2","opensubtitles_username":"user2","opensubtitles_password":"pass2","imdb_datasets_path":"/data/imdb"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", put.Code, put.Body)
	}
	body := decode[settings_response](t, put)
	if body.Tmdb_key != "tmdb-2" || body.Opensubtitles_api_key != "os-2" || body.Opensubtitles_username != "user2" || body.Opensubtitles_password != "pass2" {
		t.Fatalf("settings not echoed back: %+v", body)
	}
	if body.Imdb_datasets_path != "/data/imdb" || !body.Tmdb_configured || !body.Opensubtitles_configured {
		t.Fatalf("expected configured sources: %+v", body)
	}

	// Values persist in the config table and survive a fresh read.
	get := do_request(t, server, http.MethodGet, "/api/v1/settings", "")
	got := decode[settings_response](t, get)
	if got.Tmdb_key != "tmdb-2" || got.Imdb_datasets_path != "/data/imdb" {
		t.Fatalf("settings GET did not reflect overrides: %+v", got)
	}
	for key, want := range map[string]string{
		config.Override_tmdb_key:               "tmdb-2",
		config.Override_opensubtitles_api_key:  "os-2",
		config.Override_opensubtitles_username: "user2",
		config.Override_opensubtitles_password: "pass2",
		config.Override_imdb_datasets_path:     "/data/imdb",
	} {
		value, ok, err := store.Config_get(ctx, key)
		if err != nil || !ok || value != want {
			t.Errorf("config %s = %q, ok=%v, err=%v; want %q", key, value, ok, err, want)
		}
	}

	// Clearing a key to "" removes the credential and marks it unconfigured.
	clear := do_request(t, server, http.MethodPut, "/api/v1/settings",
		`{"tmdb_key":""}`)
	cleared := decode[settings_response](t, clear)
	if cleared.Tmdb_key != "" || cleared.Tmdb_configured {
		t.Fatalf("expected tmdb cleared/unconfigured: %+v", cleared)
	}
}

func Test_settings_reload_applies_sources(t *testing.T) {
	server, _ := new_test_server(t)
	called := false
	server.reload = func() error {
		called = true
		return nil
	}
	post := do_request(t, server, http.MethodPost, "/api/v1/settings/reload", "{}")
	if post.Code != http.StatusOK {
		t.Fatalf("reload status = %d: %s", post.Code, post.Body)
	}
	if !called {
		t.Fatal("reload hook was not called")
	}
	body := decode[settings_response](t, post)
	if body.Tmdb_key != "test" || !body.Tmdb_configured {
		t.Fatalf("expected refreshed settings: %+v", body)
	}
}

func Test_settings_reload_unconfigured_returns_503(t *testing.T) {
	server, _ := new_test_server(t)
	post := do_request(t, server, http.MethodPost, "/api/v1/settings/reload", "{}")
	if post.Code != http.StatusServiceUnavailable {
		t.Fatalf("reload status = %d, want 503", post.Code)
	}
}

func Test_settings_overrides_merge_over_config_file_value(t *testing.T) {
	server, _ := new_test_server(t)
	get := do_request(t, server, http.MethodGet, "/api/v1/settings", "")
	body := decode[settings_response](t, get)
	if body.Tmdb_key != "test" || !body.Tmdb_configured {
		t.Fatalf("expected config-file tmdb key without override: %+v", body)
	}
	if body.Opensubtitles_username != "" {
		t.Fatalf("expected empty opensubtitles username by default: %+v", body)
	}
}

func Test_settings_upsert_library_from_web(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()

	put := do_request(t, server, http.MethodPut, "/api/v1/settings",
		`{"libraries":[{"name":"movies","path":"/srv/movies","enabled":true}]}`)
	if put.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", put.Code, put.Body)
	}
	body := decode[settings_response](t, put)
	if len(body.Libraries) != 1 || body.Libraries[0].Name != "movies" ||
		body.Libraries[0].Path != "/srv/movies" || !body.Libraries[0].Enabled {
		t.Fatalf("library not echoed back: %+v", body.Libraries)
	}

	libraries, err := store.List_libraries(ctx)
	if err != nil || len(libraries) != 1 || libraries[0].Path != "/srv/movies" || !libraries[0].Enabled {
		t.Fatalf("library not persisted: %v (%+v)", err, libraries)
	}

	// Same name again just updates path and enabled state in place.
	put2 := do_request(t, server, http.MethodPut, "/api/v1/settings",
		`{"libraries":[{"name":"movies","path":"/srv/movies2","enabled":false}]}`)
	if put2.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", put2.Code, put2.Body)
	}
	libraries, err = store.List_libraries(ctx)
	if err != nil || len(libraries) != 1 || libraries[0].Path != "/srv/movies2" || libraries[0].Enabled {
		t.Fatalf("library not updated: %v (%+v)", err, libraries)
	}
}

func Test_datasets_stream_and_import_roundtrip(t *testing.T) {
	store := mem_store(t)
	tracker := imdb_datasets.New_tracker()
	streamed := make(chan string, 1)
	cfg := &config.Config{Database_path: ":memory:", Poster_cache_dir: t.TempDir()}
	data_dir := filepath.Join(t.TempDir(), "imdb")
	server := New(Options{
		Store: store, Config: cfg, Static: webui.FS(), Logger: discard_logger(),
		Datasets: tracker,
		Stream_datasets: func(index_db_path string) {
			tracker.Set_has_path(true)
			tracker.Observe(imdb_datasets.Build_progress{
				Step: imdb_datasets.Build_import, Dataset: "title.basics",
				Bytes: 250, Total: 1000,
			})
			streamed <- index_db_path
		},
	})

	resp := do_request(t, server, http.MethodPost, "/api/v1/datasets", `{"path":"`+data_dir+`"}`)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", resp.Code, resp.Body)
	}
	select {
	case index_db_path := <-streamed:
		want := filepath.Join(data_dir, imdb_datasets.Index_db_name)
		if index_db_path != want {
			t.Errorf("streamed index path = %q, want %q", index_db_path, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream hook was not invoked")
	}
	status := tracker.Snapshot()
	if status.Step != imdb_datasets.Build_import || status.Bytes != 250 {
		t.Errorf("tracker step/bytes = %q/%d, want import/250", status.Step, status.Bytes)
	}
	if !status.Has_path {
		t.Error("expected has_path after starting an import")
	}
	value, ok, _ := store.Config_get(context.Background(), config.Override_imdb_datasets_path)
	if !ok || value != data_dir {
		t.Errorf("persisted datasets path = %q, ok=%v; want %q", value, ok, data_dir)
	}
}

func Test_datasets_default_directory_under_database_path(t *testing.T) {
	store := mem_store(t)
	streamed := make(chan string, 1)
	cfg := &config.Config{Database_path: "/tmp/data/tomovee.db", Poster_cache_dir: t.TempDir()}
	server := New(Options{
		Store: store, Config: cfg, Static: webui.FS(), Logger: discard_logger(),
		Datasets:        imdb_datasets.New_tracker(),
		Stream_datasets: func(index_db_path string) { streamed <- index_db_path },
	})

	resp := do_request(t, server, http.MethodPost, "/api/v1/datasets", `{}`)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", resp.Code, resp.Body)
	}
	want := filepath.Join("/tmp/data", "imdb_datasets", imdb_datasets.Index_db_name)
	select {
	case index_db_path := <-streamed:
		if index_db_path != want {
			t.Errorf("streamed index path = %q, want %q", index_db_path, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream hook was not invoked")
	}
}

func Test_datasets_endpoint_unavailable_without_pipeline_hook(t *testing.T) {
	server, _ := new_test_server(t)
	resp := do_request(t, server, http.MethodPost, "/api/v1/datasets", `{}`)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
}

func Test_datasets_endpoint_conflicts_with_running_build(t *testing.T) {
	server, _ := new_test_server(t)
	server.stream_datasets = func(string) {}
	tracker := imdb_datasets.New_tracker()
	tracker.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_stale, Total: 100})
	server.datasets = tracker

	resp := do_request(t, server, http.MethodPost, "/api/v1/datasets", `{}`)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.Code, resp.Body)
	}
}
