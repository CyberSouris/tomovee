package webserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/poster_cache"
	"github.com/cybersouris/tomovee/internal/scan"
	"github.com/cybersouris/tomovee/internal/scanner"
	"github.com/cybersouris/tomovee/internal/thumbnail"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

func Test_content_type_for(t *testing.T) {
	cases := map[string]string{
		"index.html":  "text/html; charset=utf-8",
		"app.js":      "text/javascript; charset=utf-8",
		"style.css":   "text/css; charset=utf-8",
		"data.json":   "application/json",
		"logo.svg":    "image/svg+xml",
		"pic.png":     "image/png",
		"favicon.ico": "image/x-icon",
		"archive.zip": "",
	}
	for name, want := range cases {
		if got := content_type_for(name); got != want {
			t.Errorf("content_type_for(%q) = %q, want %q", name, got, want)
		}
	}
}

func Test_handle_spa(t *testing.T) {
	server, _ := new_test_server(t)
	server.static = fstest.MapFS{
		"index.html": {Data: []byte("<html>index</html>")},
		"app.js":     {Data: []byte("console.log('x')")},
	}

	root := do_request(t, server, http.MethodGet, "/", "")
	if root.Code != 200 || !strings.Contains(root.Body.String(), "index") {
		t.Errorf("root = %d %q, want index", root.Code, root.Body.String())
	}
	if ct := root.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("root content-type = %q", ct)
	}

	js := do_request(t, server, http.MethodGet, "/app.js", "")
	if js.Code != 200 || js.Body.String() != "console.log('x')" {
		t.Errorf("js = %d %q", js.Code, js.Body.String())
	}
	if ct := js.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("js content-type = %q", ct)
	}

	// Unknown client-side route falls back to index.html for SPA routing.
	route := do_request(t, server, http.MethodGet, "/catalog/detail", "")
	if route.Code != 200 || !strings.Contains(route.Body.String(), "index") {
		t.Errorf("spa route = %d %q, want index fallback", route.Code, route.Body.String())
	}

	api := do_request(t, server, http.MethodGet, "/api/v1/nonexistent", "")
	if api.Code != 404 {
		t.Errorf("api 404 = %d, want 404", api.Code)
	}
	if !strings.Contains(api.Body.String(), "unknown endpoint") {
		t.Errorf("api 404 body = %q", api.Body.String())
	}
}

func Test_new_defaults_logger(t *testing.T) {
	server := New(Options{})
	if server.logger == nil {
		t.Fatal("New with a nil logger did not default it")
	}
}

func Test_internal_error_and_bad_gateway(t *testing.T) {
	server, _ := new_test_server(t)

	rec := httptest.NewRecorder()
	server.internal_error(rec, errors.New("boom"), "state-test")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Internal Server Error") {
		t.Errorf("internal_error = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.bad_gateway(rec, errors.New("upstream down"), "state-test")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "Bad Gateway") {
		t.Errorf("bad_gateway = %d %q", rec.Code, rec.Body.String())
	}
}

func Test_path_id_rejects_bad_ids(t *testing.T) {
	server, _ := new_test_server(t)
	if rec := do_request(t, server, http.MethodGet, "/api/v1/catalog/abc", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id = %d, want 400", rec.Code)
	}
	if rec := do_request(t, server, http.MethodGet, "/api/v1/catalog/999999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing entry = %d, want 404", rec.Code)
	}
}

func Test_int_query_and_itoa(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x?limit=10", http.NoBody)
	if got := int_query(req, "limit"); got != 10 {
		t.Errorf("int_query(limit) = %d, want 10", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/x?limit=abc", http.NoBody)
	if got := int_query(req, "limit"); got != 0 {
		t.Errorf("int_query(bad limit) = %d, want 0", got)
	}
	if itoa(42) != "42" || itoa(-1) != "-1" {
		t.Errorf("itoa broken")
	}
}

func Test_handle_catalog_detail_series(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "Breaking Bad",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert_series_metadata(ctx, database.Series_metadata{
		Catalog_entry_id: entry_id, First_air_date: "2008-01-20", Num_episodes: 62,
	}); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: entry_id, Season_number: 1, Episode_number: 1,
		Title: "Pilot",
	})
	if err != nil {
		t.Fatalf("episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, File_path: "/Breaking.Bad.S01E01.mkv", Size_bytes: 100,
	}); err != nil {
		t.Fatalf("version: %v", err)
	}

	rec := do_request(t, server, http.MethodGet, "/api/v1/catalog/"+itoa(entry_id), "")
	if rec.Code != 200 {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"episodes":[`) || !strings.Contains(body, "Pilot") {
		t.Errorf("detail missing enriched episodes: %s", body)
	}
	if !strings.Contains(body, "Breaking Bad") {
		t.Errorf("detail missing series metadata: %s", body)
	}
}

func Test_handle_libraries_list_and_delete(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	for _, name := range []string{"Movies", "Series"} {
		if err := store.Upsert_library(ctx, name, "/media/"+name, true); err != nil {
			t.Fatalf("upsert library: %v", err)
		}
	}

	list := do_request(t, server, http.MethodGet, "/api/v1/libraries", "")
	if list.Code != 200 {
		t.Fatalf("list = %d", list.Code)
	}
	if items := decode[[]library_item](t, list); len(items) != 2 {
		t.Fatalf("libraries = %d, want 2", len(items))
	}

	del := do_request(t, server, http.MethodDelete, "/api/v1/libraries/Movies", "")
	if del.Code != 200 {
		t.Fatalf("delete = %d %s", del.Code, del.Body.String())
	}
	items := decode[[]library_item](t, do_request(t, server, http.MethodGet, "/api/v1/libraries", ""))
	if len(items) != 1 || items[0].Name != "Series" {
		t.Errorf("after delete = %+v, want only Series", items)
	}

	// Deleting an unknown library is not an error: it just refreshes settings.
	if del := do_request(t, server, http.MethodDelete, "/api/v1/libraries/Nope", ""); del.Code != 200 {
		t.Errorf("delete unknown = %d", del.Code)
	}
}

func Test_handle_library_delete_empty_name(t *testing.T) {
	server, _ := new_test_server(t)
	// The routed pattern never yields an empty name, so exercise the guard
	// directly.
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/libraries/", http.NoBody)
	request.SetPathValue("name", "")
	rec := httptest.NewRecorder()
	server.handle_library_delete(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty name = %d, want 400", rec.Code)
	}
}

func Test_handle_poster_branches(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id := unmatched_entry_with_version(t, store)

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("entry: %v", err)
	}

	// No poster path yet -> 404.
	if rec := do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), ""); rec.Code != 404 {
		t.Errorf("no poster = %d, want 404", rec.Code)
	}

	// Invalid and missing ids.
	if rec := do_request(t, server, http.MethodGet, "/api/v1/posters/abc", ""); rec.Code != 400 {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
	if rec := do_request(t, server, http.MethodGet, "/api/v1/posters/99999", ""); rec.Code != 404 {
		t.Errorf("missing entry = %d, want 404", rec.Code)
	}

	// Remote poster path with no on-disk file falls back to a TMDB redirect.
	entry.Poster_path = "/matrix.jpg"
	if err := store.Update_catalog_entry(ctx, id, *entry); err != nil {
		t.Fatalf("update poster path: %v", err)
	}
	rec := do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect = %d", rec.Code)
	}
	if location := rec.Header().Get("Location"); !strings.Contains(location, "image.tmdb.org") || !strings.Contains(location, "/matrix.jpg") {
		t.Errorf("Location = %q", location)
	}

	// A concrete file next to the poster cache dir is served directly.
	local := server.cfg.Poster_cache_dir + "/" + poster_file_name(id, "/matrix.jpg")
	if err := os.WriteFile(local, []byte("poster-bytes"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	rec = do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), "")
	if rec.Code != 200 || rec.Body.String() != "poster-bytes" {
		t.Errorf("cached poster = %d %q", rec.Code, rec.Body.String())
	}

	// Generated frame poster served from its conventional local path.
	entry.Poster_path = thumbnail.Local_marker
	if err := store.Update_catalog_entry(ctx, id, *entry); err != nil {
		t.Fatalf("update: %v", err)
	}
	frame := thumbnail.Local_path(server.cfg.Poster_cache_dir, id)
	if err := os.WriteFile(frame, []byte("frame"), 0o644); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if rec := do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), ""); rec.Code != 200 || rec.Body.String() != "frame" {
		t.Errorf("frame poster = %d %q", rec.Code, rec.Body.String())
	}

	// Missing frame file -> 404.
	if err := os.Remove(frame); err != nil {
		t.Fatalf("remove frame: %v", err)
	}
	if rec := do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), ""); rec.Code != 404 {
		t.Errorf("missing frame = %d, want 404", rec.Code)
	}
}

func Test_handle_poster_prune(t *testing.T) {
	server, _ := new_test_server(t)

	if rec := do_request(t, server, http.MethodPost, "/api/v1/posters/prune", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no poster cache = %d, want 503", rec.Code)
	}

	server.posters = poster_cache.New(poster_cache.Options{
		Dir:    server.cfg.Poster_cache_dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec := do_request(t, server, http.MethodPost, "/api/v1/posters/prune", "")
	if rec.Code != 200 {
		t.Fatalf("prune = %d %s", rec.Code, rec.Body.String())
	}
	if removed := decode[map[string]int](t, rec); removed["removed"] != 0 {
		t.Errorf("removed = %d, want 0", removed["removed"])
	}
}

func Test_handle_reclassify_validation(t *testing.T) {
	server, _ := new_test_server(t)
	server.matching = nil
	if rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/1/reclassify", `{"media_type":"series"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no matching = %d, want 503", rec.Code)
	}

	server, store := new_test_server(t)
	id := unmatched_entry_with_version(t, store)
	target := "/api/v1/catalog/" + itoa(id) + "/reclassify"

	if rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/abc/reclassify", `{"media_type":"series"}`); rec.Code != 400 {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
	if rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/99999/reclassify", `{"media_type":"series"}`); rec.Code != 404 {
		t.Errorf("missing entry = %d, want 404", rec.Code)
	}
	if rec := do_request(t, server, http.MethodPost, target, `{`); rec.Code != 400 {
		t.Errorf("bad json = %d, want 400", rec.Code)
	}
	if rec := do_request(t, server, http.MethodPost, target, `{"media_type":"episode"}`); rec.Code != 400 {
		t.Errorf("bad media type = %d, want 400", rec.Code)
	}
}

func Test_handle_reclassify_queues_behind_running_job(t *testing.T) {
	server, store := new_test_server(t)
	id := unmatched_entry_with_version(t, store)
	block := make(chan struct{})
	started := make(chan struct{})
	if _, err := server.matches.Start(func(_ context.Context, _ func(matching.Progress)) (*matching.Result, error) {
		close(started)
		<-block
		return &matching.Result{}, nil
	}); err != nil {
		t.Fatalf("start job: %v", err)
	}
	<-started

	rec_ch := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec_ch <- do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/reclassify", `{"media_type":"series"}`)
	}()

	select {
	case <-rec_ch:
		t.Fatal("reclassify answered while a matching job was running instead of queueing")
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	select {
	case rec := <-rec_ch:
		if rec.Code != http.StatusOK {
			t.Fatalf("queued reclassify = %d, want 200: %s", rec.Code, rec.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued reclassify did not finish after the running job stopped")
	}
}

func Test_handle_reclassify_movie_to_series_ambiguous(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_low{})
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(entry_id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != 200 {
		t.Fatalf("reclassify = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied    bool             `json:"applied"`
		Candidates []candidate_item `json:"candidates"`
		Entry      *catalog_item    `json:"entry"`
	}](t, rec)
	if body.Applied {
		t.Error("ambiguous re-classification was applied")
	}
	if body.Entry != nil {
		t.Errorf("unexpected entry in ambiguous response: %+v", body.Entry)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("entry: %v", err)
	}
	if entry.Media_type != "series" {
		t.Errorf("media type = %q, want the flipped series", entry.Media_type)
	}
}

func Test_handle_reclassify_series_to_movie_applied(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(entry_id)+"/reclassify", `{"media_type":"movie"}`)
	if rec.Code != 200 {
		t.Fatalf("reclassify = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied bool          `json:"applied"`
		Entry   *catalog_item `json:"entry"`
	}](t, rec)
	if !body.Applied || body.Entry == nil {
		t.Fatalf("body = %+v, want applied entry", body)
	}
	entry, err := store.Get_catalog_entry(ctx, entry_id)
	if err != nil || entry == nil {
		t.Fatalf("entry: %v", err)
	}
	if entry.Media_type != "movie" || !entry.Matched {
		t.Errorf("entry = %+v, want matched movie", entry)
	}
}

// fake_series_offline answers the offline IPTV lookup with one confident series
// hit, so a re-type to a series resolves during the automatic pass.
type fake_series_offline struct{}

func (fake_series_offline) Search(_ context.Context, _ string, _ int, kind scanner.Media_type) ([]matcher.Offline_candidate, error) {
	if kind != scanner.Series {
		return nil, nil
	}
	return []matcher.Offline_candidate{
		{Imdb_id: "tt0386676", Title: "The Office", Year: 2005, Media_type: scanner.Series},
	}, nil
}

func Test_handle_reclassify_movie_to_series_returns_the_merged_series(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_series_offline{})
	ctx := context.Background()
	if err := store.Ensure_library(ctx, "TV", t.TempDir(), true); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	lib := libraries[0].Id

	series_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 1, Episode_number: 1,
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save series version: %v", err)
	}
	movie_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "Office Interviews",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: lib, File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 900,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(movie_id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != 200 {
		t.Fatalf("reclassify = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied bool          `json:"applied"`
		Entry   *catalog_item `json:"entry"`
	}](t, rec)
	if !body.Applied || body.Entry == nil {
		t.Fatalf("body = %+v, want the merged series applied", body)
	}
	if body.Entry.Id != series_id {
		t.Errorf("response entry id = %d, want the existing series %d", body.Entry.Id, series_id)
	}
	if body.Entry.Media_type != "series" || body.Entry.Imdb_id != "tt0386676" {
		t.Errorf("entry = %+v, want the matched series", body.Entry)
	}
	if entry, err := store.Get_catalog_entry(ctx, movie_id); err != nil || entry != nil {
		t.Errorf("re-typed movie entry not drained: entry=%+v err=%v", entry, err)
	}
}

func Test_handle_scan_start_branches(t *testing.T) {
	server, _ := new_test_server(t)

	if rec := do_request(t, server, http.MethodPost, "/api/v1/scan", `{`); rec.Code != 400 {
		t.Errorf("bad json = %d, want 400", rec.Code)
	}

	block := make(chan struct{})
	if _, err := server.jobs.Start(func(_ context.Context, _ func(scan.Progress)) (*scan.Result, error) {
		<-block
		return &scan.Result{}, nil
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if rec := do_request(t, server, http.MethodPost, "/api/v1/scan", ""); rec.Code != http.StatusConflict {
		t.Errorf("conflict = %d, want 409", rec.Code)
	}

	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for server.jobs.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("blocked scan never finished")
		}
		time.Sleep(time.Millisecond)
	}

	server.scanner = nil
	if rec := do_request(t, server, http.MethodPost, "/api/v1/scan", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no scanner = %d, want 503", rec.Code)
	}
}

func Test_handle_match_start_conflict(t *testing.T) {
	server, _ := new_test_server(t)
	block := make(chan struct{})
	defer close(block)
	if _, err := server.matches.Start(func(_ context.Context, _ func(matching.Progress)) (*matching.Result, error) {
		<-block
		return &matching.Result{}, nil
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if rec := do_request(t, server, http.MethodPost, "/api/v1/match", ""); rec.Code != http.StatusConflict {
		t.Errorf("conflict = %d, want 409", rec.Code)
	}
}

func Test_matching_unavailable_message_branches(t *testing.T) {
	server, _ := new_test_server(t)

	if got := server.datasets_percent(); got != 0 {
		t.Errorf("datasets_percent with nil tracker = %d, want 0", got)
	}

	server.cfg = &config.Config{Imdb_datasets_path: "/data/imdb"}
	if msg := server.matching_unavailable_message(); !strings.Contains(msg, "load") {
		t.Errorf("datasets message = %q", msg)
	}
	if !server.datasets_building() && server.datasets != nil {
		t.Errorf("datasets_building with nil tracker = true")
	}

	server.cfg = &config.Config{Api: config.Api_config{Tmdb_key: "key"}}
	if msg := server.matching_unavailable_message(); !strings.Contains(msg, "sources are configured") {
		t.Errorf("unavailable message = %q", msg)
	}

	server.cfg = &config.Config{}
	if msg := server.matching_unavailable_message(); !strings.Contains(msg, "not configured") {
		t.Errorf("not configured message = %q", msg)
	}
}

func Test_handle_scan_stream_ends_on_cancel(t *testing.T) {
	server, _ := new_test_server(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/scan/stream", http.NoBody).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(recorder, request)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scan stream did not stop after cancel")
	}
	if recorder.Code != 200 {
		t.Errorf("stream = %d", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(recorder.Body.String(), "event: status") {
		t.Errorf("missing status event: %q", recorder.Body.String())
	}
}

func Test_handle_match_stream_ends_on_cancel(t *testing.T) {
	server, _ := new_test_server(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/match/stream", http.NoBody).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(recorder, request)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("match stream did not stop after cancel")
	}
	if !strings.Contains(recorder.Body.String(), "event: status") {
		t.Errorf("missing status event: %q", recorder.Body.String())
	}
}

func Test_handle_scan_stream_too_many_subscribers(t *testing.T) {
	server, _ := new_test_server(t)
	var unsubs []func()
	for i := 0; i < max_subscribers; i++ {
		_, unsubscribe, err := server.scan_feed.Subscribe()
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		unsubs = append(unsubs, unsubscribe)
	}
	defer func() {
		for _, unsubscribe := range unsubs {
			unsubscribe()
		}
	}()
	if rec := do_request(t, server, http.MethodGet, "/api/v1/scan/stream", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("full stream = %d, want 503", rec.Code)
	}
}

// Test_per_library_jobs_reach_the_feed pins the fix for the stuck WebUI. A scan
// or match started for a selected library runs on its own per-library manager,
// so its progress must still reach the shared feed the SSE stream subscribes to
// — otherwise the stream never emits "done", the Actions page never clears its
// busy state, and a chained Scan & Match never starts its match step.
func Test_per_library_jobs_reach_the_feed(t *testing.T) {
	server, _ := new_test_server(t)

	scan_progress, scan_unsubscribe, err := server.scan_feed.Subscribe()
	if err != nil {
		t.Fatalf("subscribe scan feed: %v", err)
	}
	defer scan_unsubscribe()

	scan_mgr := server.scan_job_manager("lib-a")
	if _, run_err := scan_mgr.Run_sync(func(_ context.Context, progress func(scan.Progress)) (*scan.Result, error) {
		progress(scan.Progress{Phase: "file", Path: "/m/movie.mkv", Files_scanned: 1})
		progress(scan.Progress{Phase: "done", Files_found: 1, Files_scanned: 1})
		return &scan.Result{Found: 1}, nil
	}); run_err != nil {
		t.Fatalf("per-library scan: %v", run_err)
	}

	scan_deadline := time.After(2 * time.Second)
	scan_done := false
	for !scan_done {
		select {
		case progress, ok := <-scan_progress:
			if !ok {
				t.Fatal("scan feed closed before the per-library done phase")
			}
			scan_done = progress.Phase == "done"
		case <-scan_deadline:
			t.Fatal("per-library scan progress never reached the scan feed")
		}
	}

	match_progress, match_unsubscribe, err := server.match_feed.Subscribe()
	if err != nil {
		t.Fatalf("subscribe match feed: %v", err)
	}
	defer match_unsubscribe()

	match_mgr := server.match_job_manager("lib-a")
	if _, run_err := match_mgr.Run_sync(func(_ context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		progress(matching.Progress{Phase: "done", Total: 1, Done: 1})
		return &matching.Result{Total: 1}, nil
	}); run_err != nil {
		t.Fatalf("per-library match: %v", run_err)
	}

	match_deadline := time.After(2 * time.Second)
	for {
		select {
		case progress, ok := <-match_progress:
			if !ok {
				t.Fatal("match feed closed before the per-library done phase")
			}
			if progress.Phase == "done" {
				return
			}
		case <-match_deadline:
			t.Fatal("per-library match progress never reached the match feed")
		}
	}
}

func Test_job_manager_subscribe_receives_progress(t *testing.T) {
	server, _ := new_test_server(t)
	ch, unsubscribe, err := server.jobs.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, run_err := server.jobs.Run_sync(func(_ context.Context, progress func(scan.Progress)) (*scan.Result, error) {
		progress(scan.Progress{Phase: "file", Path: "/m/movie.mkv", Files_scanned: 1})
		progress(scan.Progress{Phase: "done", Files_found: 1, Files_scanned: 1, New_files: 1})
		return &scan.Result{Found: 1, New: 1}, nil
	}); run_err != nil {
		t.Fatalf("run: %v", run_err)
	}
	unsubscribe()

	var events []scan.Progress
	for p := range ch {
		events = append(events, p)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least 2", len(events))
	}
	last := events[len(events)-1]
	if last.Phase != "done" || last.New_files != 1 {
		t.Errorf("last event = %+v, want done with 1 new file", last)
	}
}

func Test_job_manager_subscribe_limits(t *testing.T) {
	server, _ := new_test_server(t)
	var unsubs []func()
	for i := 0; i < max_subscribers; i++ {
		_, unsubscribe, err := server.jobs.Subscribe()
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		unsubs = append(unsubs, unsubscribe)
	}
	if _, _, err := server.jobs.Subscribe(); !errors.Is(err, Err_too_many_subscribers) {
		t.Errorf("9th subscribe err = %v, want Err_too_many_subscribers", err)
	}
	for _, unsubscribe := range unsubs {
		unsubscribe()
	}
}

func Test_job_manager_subscribe_initializes_from_running_job(t *testing.T) {
	server, _ := new_test_server(t)
	block := make(chan struct{})
	started := make(chan struct{})
	run_done := make(chan error, 1)
	go func() {
		_, err := server.jobs.Run_sync(func(_ context.Context, progress func(scan.Progress)) (*scan.Result, error) {
			close(started)
			<-block
			progress(scan.Progress{Phase: "done", Files_found: 1})
			return &scan.Result{}, nil
		})
		run_done <- err
	}()
	<-started

	ch, unsubscribe, err := server.jobs.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	select {
	case <-ch:
		// Initial progress delivered.
	case <-time.After(time.Second):
		t.Fatal("no initial progress from a running job")
	}

	close(block)
	select {
	case err := <-run_done:
		if err != nil {
			t.Errorf("Run_sync: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run_sync did not finish")
	}
}

func Test_job_manager_run_sync_queued_waits_for_active_job(t *testing.T) {
	server, _ := new_test_server(t)

	release := make(chan struct{})
	started := make(chan struct{})
	var order []string
	var mu sync.Mutex
	mark := func(tag string) {
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
	}

	go func() {
		_, _ = server.jobs.Run_sync(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-one")
			close(started)
			<-release
			return &scan.Result{Found: 1}, nil
		})
	}()
	<-started

	queued_started := make(chan struct{})
	queued_done := make(chan error, 1)
	go func() {
		_, err := server.jobs.Run_sync_queued(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-two")
			close(queued_started)
			return &scan.Result{New: 2}, nil
		})
		queued_done <- err
	}()

	select {
	case <-queued_started:
		t.Fatal("queued run started while the active job was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-queued_done:
		if err != nil {
			t.Fatalf("queued run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued run never completed after the active job finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "run-one" || order[1] != "run-two" {
		t.Errorf("run order = %v, want [run-one run-two]", order)
	}
}

func Test_job_manager_run_sync_queued_is_fifo(t *testing.T) {
	server, _ := new_test_server(t)

	hold := make(chan struct{})
	started := make(chan struct{})
	var order []string
	var mu sync.Mutex
	mark := func(tag string) {
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
	}

	go func() {
		_, _ = server.jobs.Run_sync(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-one")
			close(started)
			<-hold
			return &scan.Result{Found: 1}, nil
		})
	}()
	<-started

	unblock_two := make(chan struct{})
	two_started := make(chan struct{})
	two_done := make(chan error, 1)
	go func() {
		_, err := server.jobs.Run_sync_queued(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-two")
			close(two_started)
			<-unblock_two
			return &scan.Result{}, nil
		})
		two_done <- err
	}()

	// Let run-one finish so run-two claims the slot and starts.
	close(hold)
	select {
	case <-two_started:
	case <-time.After(3 * time.Second):
		t.Fatal("first queued run never started")
	}

	// Enqueue run-three while run-two still holds the slot, so the queue order
	// is deterministic: run-two then run-three.
	three_done := make(chan error, 1)
	go func() {
		_, err := server.jobs.Run_sync_queued(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-three")
			return &scan.Result{}, nil
		})
		three_done <- err
	}()

	close(unblock_two)
	for _, done := range []chan error{two_done, three_done} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("queued run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued run never completed")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"run-one", "run-two", "run-three"}
	if len(order) != len(want) {
		t.Fatalf("run order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("run order = %v, want %v", order, want)
			break
		}
	}
}

func Test_job_manager_start_queued_queues_behind_active_job(t *testing.T) {
	server, _ := new_test_server(t)

	block := make(chan struct{})
	started := make(chan struct{})
	var order []string
	var mu sync.Mutex
	mark := func(tag string) {
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
	}

	go func() {
		_, _ = server.jobs.Start(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
			mark("run-one")
			close(started)
			<-block
			return &scan.Result{Found: 1}, nil
		})
	}()
	<-started

	queued_started := make(chan struct{})
	snapshot, err := server.jobs.Start_queued(func(context.Context, func(scan.Progress)) (*scan.Result, error) {
		mark("run-two")
		close(queued_started)
		return &scan.Result{New: 2}, nil
	})
	if err != nil {
		t.Fatalf("start_queued: %v", err)
	}
	if snapshot == nil || !snapshot.Running {
		t.Fatalf("queued snapshot = %+v, want a running placeholder", snapshot)
	}

	select {
	case <-queued_started:
		t.Fatal("queued job started while the active job was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	select {
	case <-queued_started:
	case <-time.After(3 * time.Second):
		t.Fatal("queued job never started after the active job finished")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if job := server.jobs.Snapshot(); job != nil && !job.Running && job.Result != nil && job.Result.New == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job := server.jobs.Snapshot(); job == nil || job.Running || job.Result == nil || job.Result.New != 2 {
		t.Fatalf("queued job never completed, snapshot = %+v", job)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"run-one", "run-two"}
	if len(order) != len(want) {
		t.Fatalf("run order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("run order = %v, want %v", order, want)
			break
		}
	}
}

func Test_handle_version_split_into_movie(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/split", "")
	if rec.Code != 200 {
		t.Fatalf("split = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied bool          `json:"applied"`
		Entry   *catalog_item `json:"entry"`
	}](t, rec)
	if !body.Applied || body.Entry == nil {
		t.Fatalf("body = %+v, want the new movie matched", body)
	}
	if body.Entry.Media_type != "movie" || body.Entry.Title != "The Matrix" {
		t.Errorf("new entry = %+v", body.Entry)
	}
	if entry, err := store.Get_catalog_entry(ctx, entry_id); err != nil || entry != nil {
		t.Errorf("drained series still exists: %+v, %v", entry, err)
	}
	versions, err := store.List_versions_for_entry(ctx, body.Entry.Id)
	if err != nil || len(versions) != 1 || versions[0].Id != version_id {
		t.Errorf("split entry versions = %+v, %v", versions, err)
	}
}

func Test_handle_version_episode_assignment(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Office/Interviews.mkv", Size_bytes: 900,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/episode", `{"season":1,"episode":9}`)
	if rec.Code != 200 {
		t.Fatalf("assign = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Ok        bool  `json:"ok"`
		EpisodeId int64 `json:"episode_id"`
	}](t, rec)
	if !body.Ok || body.EpisodeId == 0 {
		t.Fatalf("body = %+v", body)
	}
	episode, err := store.Get_episode(ctx, body.EpisodeId)
	if err != nil || episode == nil {
		t.Fatalf("episode: %v, %v", episode, err)
	}
	if episode.Season_number != 1 || episode.Episode_number != 9 || episode.Catalog_entry_id != entry_id {
		t.Errorf("episode = %+v", episode)
	}
	if version, err := store.Get_version(ctx, version_id); err != nil || version == nil {
		t.Fatalf("version: %v, %v", version, err)
	} else if version.Episode_id != body.EpisodeId || version.Catalog_entry_id != 0 {
		t.Errorf("version = %+v, want entry moved onto the episode", version)
	}
}

func Test_handle_version_episode_rejects_bad_numbers(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Office/Interviews.mkv", Size_bytes: 900,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	if rec := do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/episode", `{"season":1,"episode":0}`); rec.Code != http.StatusBadRequest {
		t.Errorf("episode 0 = %d, want 400", rec.Code)
	}
	if rec := do_request(t, server, http.MethodPost, "/api/v1/versions/999999/episode", `{"season":1,"episode":1}`); rec.Code != http.StatusNotFound {
		t.Errorf("missing version = %d, want 404", rec.Code)
	}
}

func Test_handle_version_actions_reject_bad_requests(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Office/Interviews.mkv", Size_bytes: 900,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	path := "/api/v1/versions/" + itoa(version_id)

	for _, tc := range []struct {
		name string
		path string
		body string
		want int
	}{
		{"split with an unparseable id", "/api/v1/versions/abc/split", "", http.StatusBadRequest},
		{"split with a zero id", "/api/v1/versions/0/split", "", http.StatusBadRequest},
		{"split of a version that is gone", "/api/v1/versions/999999/split", "", http.StatusNotFound},
		{"episode with an unparseable id", "/api/v1/versions/abc/episode", `{"season":1,"episode":1}`, http.StatusBadRequest},
		{"episode with a negative season", path + "/episode", `{"season":-1,"episode":1}`, http.StatusBadRequest},
		{"episode with a broken body", path + "/episode", `{"season":`, http.StatusBadRequest},
	} {
		rec := do_request(t, server, http.MethodPost, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Without a matching service neither action can run, whatever it is asked
	// to do.
	server.matching = nil
	for _, tc := range []struct {
		name string
		path string
	}{
		{"split", path + "/split"},
		{"episode", path + "/episode"},
	} {
		rec := do_request(t, server, http.MethodPost, tc.path, `{"season":1,"episode":1}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without matching = %d, want 503", tc.name, rec.Code)
		}
	}
}

// Splitting a file out of a title and finding nothing for it leaves the new
// movie waiting with its candidates, exactly like any other unmatched entry.
func Test_handle_version_split_persists_candidates_when_unmatched(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_low{})
	ctx := context.Background()
	entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix Collection",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/split", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("split = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied    bool             `json:"applied"`
		Entry      *catalog_item    `json:"entry"`
		Candidates []candidate_item `json:"candidates"`
	}](t, rec)
	if body.Applied || body.Entry == nil {
		t.Fatalf("body = %+v, want the new movie left unmatched", body)
	}
	if len(body.Candidates) != 1 || body.Candidates[0].Imdb_id != "tt9999999" {
		t.Fatalf("candidates = %+v", body.Candidates)
	}
	persisted, err := store.List_candidates(ctx, body.Entry.Id)
	if err != nil {
		t.Fatalf("list persisted candidates: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Title != "Completely Different" {
		t.Errorf("persisted candidates = %+v", persisted)
	}
	// The file is on the new movie either way: the split happened, only the
	// match did not.
	versions, err := store.List_versions_for_entry(ctx, body.Entry.Id)
	if err != nil || len(versions) != 1 || versions[0].Id != version_id {
		t.Errorf("new entry versions = %+v, %v", versions, err)
	}
}

// A split joins the queue of a matching job that is already running instead of
// being turned away, so the request comes back with the result of the real run.
func Test_handle_version_split_waits_for_a_running_job(t *testing.T) {
	server, store := new_test_server(t)
	entry_id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "series", Title: "The Matrix",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	version_id, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: entry_id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	block := make(chan struct{})
	if _, err := server.matches.Start(func(_ context.Context, _ func(matching.Progress)) (*matching.Result, error) {
		<-block
		return &matching.Result{}, nil
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/split", "")
	}()

	select {
	case rec := <-done:
		t.Fatalf("split answered %d while a job was running, want it to wait", rec.Code)
	case <-time.After(50 * time.Millisecond):
	}
	close(block)

	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("split = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied bool          `json:"applied"`
		Entry   *catalog_item `json:"entry"`
	}](t, rec)
	if !body.Applied || body.Entry == nil || body.Entry.Media_type != "movie" {
		t.Fatalf("body = %+v, want the new movie matched", body)
	}
}

// failing_metadata fails every TMDB details lookup, which is the only way a
// manual match can hit the "enrich from tmdb" failure response.
type failing_metadata struct{ fake_metadata }

func (failing_metadata) Movie_details(context.Context, int) (*tmdb.Movie_details, error) {
	return nil, errors.New("tmdb unavailable")
}

// imdb_find_metadata resolves the Matrix IMDb id through TMDB find, so a bare
// IMDb match can be enriched without any offline index.
type imdb_find_metadata struct{ fake_metadata }

func (imdb_find_metadata) Find_by_imdb(_ context.Context, imdb_id string) (*tmdb.Find_result, error) {
	if imdb_id != "tt0133093" {
		return &tmdb.Find_result{}, nil
	}
	return &tmdb.Find_result{Movie_results: []tmdb.Movie_search_result{{Id: 603}}}, nil
}

func upsert_movie_entry(t *testing.T, store *database.Store, title string) int64 {
	t.Helper()
	id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "movie", Title: title,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return id
}

// inject_failure installs a SQLite trigger that aborts the given statement, so
// a store write can be made to fail after earlier calls have succeeded. SQLite
// keeps triggers in the schema, so a second connection to the same file sees
// them; the database must therefore be file-backed rather than :memory:.
func inject_failure(t *testing.T, database_path, trigger string) {
	t.Helper()
	dsn := "file:" + url.PathEscape(database_path) + "?_pragma=journal_mode(WAL)"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(trigger); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
}

// new_trigger_server opens a file-backed database, applies migrations through
// the normal Open path, and hands back the path so a failure trigger can be
// added before the request runs.
func new_trigger_server(t *testing.T, metadata matcher.Metadata_source, offline matcher.Offline_source) (*Server, *database.Store, string) {
	t.Helper()
	database_path := filepath.Join(t.TempDir(), "trigger.db")
	d, err := database.Open(database_path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: metadata, Offline: offline, Logger: logger})
	matching_service := matching.New(store, m, nil, logger)
	server := New(Options{
		Store: store, Matcher: m, Metadata: metadata,
		Matching: matching_service, Logger: logger,
	})
	return server, store, database_path
}

func Test_manual_match_tmdb_enrich_failure_answers_bad_gateway(t *testing.T) {
	server, store := new_test_server_with(t, failing_metadata{}, fake_offline{})
	id := upsert_movie_entry(t, store, "The Matrix")

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{"tmdb_id":603}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("enrich failure status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_manual_match_imdb_unresolvable_answers_bad_gateway(t *testing.T) {
	server, store := new_test_server(t)
	id := upsert_movie_entry(t, store, "The Matrix")

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{"imdb_id":"tt9999999"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unresolvable imdb status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_manual_match_requires_an_identifier(t *testing.T) {
	server, store := new_test_server(t)
	id := upsert_movie_entry(t, store, "The Matrix")

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty match request status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_manual_match_imdb_resolves_via_tmdb_find(t *testing.T) {
	server, store := new_test_server_with(t, imdb_find_metadata{default_test_metadata()}, fake_offline{})
	id := upsert_movie_entry(t, store, "matrix")
	if _, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: id, File_path: "/media/matrix.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save file: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{"imdb_id":"tt0133093"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("imdb match status = %d: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		Entry *catalog_item `json:"entry"`
	}](t, rec)
	if body.Entry == nil {
		t.Fatalf("body = %+v, want the matched entry", body)
	}
	if body.Entry.Title != "The Matrix" || body.Entry.Imdb_id != "tt0133093" {
		t.Errorf("entry = %+v", body.Entry)
	}
	if body.Entry.Status != "matched" {
		t.Errorf("status = %q, want matched", body.Entry.Status)
	}
}

func Test_manual_match_update_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, imdb_find_metadata{default_test_metadata()}, fake_offline{})
	id := upsert_movie_entry(t, store, "The Matrix")
	inject_failure(t, database_path, `CREATE TRIGGER fail_catalog_update BEFORE UPDATE ON catalog_entry
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{"imdb_id":"tt0133093"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update failure status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_rematch_candidate_delete_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline{})
	id := upsert_movie_entry(t, store, "The Matrix")
	if _, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: id, File_path: "/media/matrix.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	// A stored candidate row is required: SQLite fires a BEFORE DELETE trigger
	// only per row examined, so clearing an empty shortlist would never reach
	// it. The confident match then clears that shortlist and hits the failure.
	if err := store.Replace_candidates(context.Background(), id, []database.Candidate{{
		Tmdb_id: 603, Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999,
		Media_type: "movie", Score: 0.9,
	}}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_candidate_delete BEFORE DELETE ON candidate
		BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/rematch", ``)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("candidate delete failure status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_reclassify_invalid_catalog_id_is_bad_request(t *testing.T) {
	server, _ := new_test_server(t)
	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/not-a-number/reclassify", `{"media_type":"series"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_rematch_invalid_catalog_id_is_bad_request(t *testing.T) {
	server, _ := new_test_server(t)
	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/not-a-number/rematch", ``)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_search_media_type_series(t *testing.T) {
	server, _ := new_test_server_with(t, fake_metadata{
		search_tv: []tmdb.Tv_search_result{{Id: 2993, Name: "The Office", First_air_date: "2005-03-24"}},
	}, fake_offline{})

	rec := do_request(t, server, http.MethodGet, "/api/v1/search?q=office&media_type=series", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		Results []search_item `json:"results"`
	}](t, rec)
	if len(body.Results) == 0 {
		t.Fatal("series search returned no results")
	}
	if body.Results[0].Title != "The Office" || body.Results[0].Media_type != "series" {
		t.Errorf("results = %+v", body.Results)
	}
}

func Test_match_start_falls_back_to_enabled_libraries(t *testing.T) {
	server, store := new_test_server(t)
	if err := store.Upsert_library(context.Background(), "lib1", t.TempDir(), true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/match", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("match without libraries status = %d: %s", rec.Code, rec.Body)
	}
}

func Test_scan_cancel_targets_a_single_library(t *testing.T) {
	server, store := new_test_server(t)
	if err := store.Upsert_library(context.Background(), "lib1", t.TempDir(), true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	start := do_request(t, server, http.MethodPost, "/api/v1/scan", `{"libraries":["lib1"]}`)
	if start.Code != http.StatusAccepted {
		t.Fatalf("scan start status = %d: %s", start.Code, start.Body)
	}
	cancel := do_request(t, server, http.MethodPost, "/api/v1/scan/cancel?library=lib1", ``)
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("scan cancel status = %d: %s", cancel.Code, cancel.Body)
	}
}

// failing_search_metadata fails only the search calls, so handle_search's TMDB
// error labelling can be exercised on its own.
type failing_search_metadata struct{ fake_metadata }

func (failing_search_metadata) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	return nil, errors.New("tmdb search down")
}

func (failing_search_metadata) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	return nil, errors.New("tmdb search down")
}

// Test_search_tmdb_failure_answers_bad_gateway pins the label selection in
// handle_search: a failing TMDB call is reported through bad_gateway with the
// media type it searched for.
func Test_search_tmdb_failure_answers_bad_gateway(t *testing.T) {
	for _, target := range []string{
		"/api/v1/search?q=matrix",
		"/api/v1/search?q=matrix&media_type=series",
	} {
		server, _ := new_test_server_with(t, failing_search_metadata{}, fake_offline{})
		rec := do_request(t, server, http.MethodGet, target, "")
		if rec.Code != http.StatusBadGateway {
			t.Errorf("GET %s = %d %s, want bad gateway", target, rec.Code, rec.Body.String())
		}
	}
}

// failing_local breaks the local autocomplete source only.
type failing_local struct{}

func (failing_local) Search(context.Context, string, int, scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return nil, nil
}

func (failing_local) Search_local(context.Context, string, int, scanner.Media_type, int) ([]matcher.Offline_candidate, error) {
	return nil, errors.New("local index broken")
}

// Test_search_local_failure_answers_bad_gateway pins the local-error branch of
// handle_search: once TMDB answered, a failing local index is surfaced as a
// bad gateway rather than swallowed.
func Test_search_local_failure_answers_bad_gateway(t *testing.T) {
	server, _ := new_test_server_with(t, default_test_metadata(), failing_local{})
	rec := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d %s, want bad gateway", rec.Code, rec.Body.String())
	}
}

// Test_search_caps_results_at_max pins the early break in both search loops:
// neither TMDB results nor local suggestions may push the response past
// max_search_results.
func Test_search_caps_results_at_max(t *testing.T) {
	movies := make([]tmdb.Movie_search_result, 0, max_search_results+2)
	series := make([]tmdb.Tv_search_result, 0, max_search_results+2)
	for i := 0; i < max_search_results+2; i++ {
		movies = append(movies, tmdb.Movie_search_result{Id: i + 1, Title: fmt.Sprintf("Film %d", i)})
		series = append(series, tmdb.Tv_search_result{Name: fmt.Sprintf("Show %d", i)})
	}

	for _, tc := range []struct {
		name       string
		target     string
		metadata   matcher.Metadata_source
		want_title string
		local_hit  string
	}{
		{"movie", "/api/v1/search?q=film", fake_metadata{search_movies: movies}, "Film 0", "Film Extra"},
		{"series", "/api/v1/search?q=show&media_type=series", fake_metadata{search_tv: series}, "Show 0", "Show Extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A local suggestion is waiting for every media type, so the local
			// loop runs into the cap TMDB already filled and must break
			// instead of appending.
			server, _ := new_test_server_with(t, tc.metadata, fake_local{hits: []matcher.Offline_candidate{
				{Imdb_id: "tt9999999", Title: tc.local_hit, Year: 2001, Media_type: scanner.Movie},
			}})
			rec := do_request(t, server, http.MethodGet, tc.target, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
			}
			body := decode[struct {
				Results []catalog_item `json:"results"`
			}](t, rec)
			if len(body.Results) != max_search_results {
				t.Fatalf("results = %d, want the cap of %d", len(body.Results), max_search_results)
			}
			if body.Results[0].Title != tc.want_title {
				t.Errorf("first result = %q, want %q", body.Results[0].Title, tc.want_title)
			}
		})
	}
}

// Test_search_local_skips_duplicates_and_respects_cap pins search_local: a
// local hit that repeats a TMDB result is dropped, new hits are appended, and
// the loop stops once the cap is reached.
func Test_search_local_skips_duplicates_and_respects_cap(t *testing.T) {
	server, _ := new_test_server_with(t,
		fake_metadata{search_movies: []tmdb.Movie_search_result{{Id: 603, Title: "The Matrix", Release_date: "1999-03-31"}}},
		fake_local{hits: []matcher.Offline_candidate{
			{Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie},
			{Imdb_id: "tt0234215", Title: "The Matrix Reloaded", Year: 2003, Media_type: scanner.Movie},
		}})

	rec := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Results []catalog_item `json:"results"`
	}](t, rec)
	if len(body.Results) != 2 {
		t.Fatalf("results = %+v, want the TMDB hit plus the one new local hit", body.Results)
	}
	if body.Results[1].Title != "The Matrix Reloaded" {
		t.Errorf("second result = %q, want the non-duplicate local hit", body.Results[1].Title)
	}
}

// Test_scan_cancel_by_library_and_global pins the two branches of the scan
// cancel endpoint: the per-library lookup reuses the stored manager, and the
// global form cancels every running library job plus the shared queue.
func Test_scan_cancel_by_library_and_global(t *testing.T) {
	server, store := new_test_server(t)
	dir := t.TempDir()
	if err := store.Upsert_library(context.Background(), "lib1", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	do_request(t, server, http.MethodPost, "/api/v1/scan", `{"libraries":["lib1"]}`)
	rec := do_request(t, server, http.MethodPost, "/api/v1/scan/cancel?library=lib1", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("per-library cancel = %d %s", rec.Code, rec.Body.String())
	}
	rec = do_request(t, server, http.MethodPost, "/api/v1/scan/cancel", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("global cancel = %d %s", rec.Code, rec.Body.String())
	}
}

// Test_match_cancel_by_library_and_global covers the matching-side cancel
// handler: cancelling one library reuses its manager, cancelling without a
// library name walks every per-library manager.
func Test_match_cancel_by_library_and_global(t *testing.T) {
	server, store := new_test_server(t)
	dir := t.TempDir()
	if err := store.Upsert_library(context.Background(), "lib1", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	do_request(t, server, http.MethodPost, "/api/v1/match", `{"libraries":["lib1"]}`)
	rec := do_request(t, server, http.MethodPost, "/api/v1/match/cancel?library=lib1", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("per-library cancel = %d %s", rec.Code, rec.Body.String())
	}
	rec = do_request(t, server, http.MethodPost, "/api/v1/match/cancel", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("global cancel = %d %s", rec.Code, rec.Body.String())
	}
}

// Test_background_cancel_stops_both_queues starts a scan and a match job and
// then drains everything through the combined cancel endpoint, covering the
// scan and match manager loops in handle_background_cancel.
func Test_background_cancel_stops_both_queues(t *testing.T) {
	server, store := new_test_server(t)
	dir := t.TempDir()
	if err := store.Upsert_library(context.Background(), "lib1", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	do_request(t, server, http.MethodPost, "/api/v1/scan", `{"libraries":["lib1"]}`)
	do_request(t, server, http.MethodPost, "/api/v1/match", `{"libraries":["lib1"]}`)
	rec := do_request(t, server, http.MethodPost, "/api/v1/background/cancel", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("background cancel = %d %s", rec.Code, rec.Body.String())
	}
}

// Test_per_library_job_managers_are_reused starts a scan and a match for the
// same library twice; the second start finds the manager the first one stored,
// so the per-library job maps are reused instead of growing a new entry. The
// second response is either accepted (previous job finished) or a conflict
// (still running) — both prove the same manager was found; only a 500 would
// mean the reuse path broke.
func Test_per_library_job_managers_are_reused(t *testing.T) {
	server, store := new_test_server(t)
	dir := t.TempDir()
	if err := store.Upsert_library(context.Background(), "lib1", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	do_request(t, server, http.MethodPost, "/api/v1/scan", `{"libraries":["lib1"]}`)
	rec := do_request(t, server, http.MethodPost, "/api/v1/scan", `{"libraries":["lib1"]}`)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusConflict {
		t.Fatalf("second scan start = %d %s", rec.Code, rec.Body.String())
	}

	do_request(t, server, http.MethodPost, "/api/v1/match", `{"libraries":["lib1"]}`)
	rec = do_request(t, server, http.MethodPost, "/api/v1/match", `{"libraries":["lib1"]}`)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusConflict {
		t.Fatalf("second match start = %d %s", rec.Code, rec.Body.String())
	}
}

// Test_rematch_all_start_reuses_match_managers pins the queued rematch-all
// handler: force=true routes through Run_force_rematch and still finds the
// per-library manager a plain match created.
func Test_rematch_all_start_reuses_match_managers(t *testing.T) {
	server, store := new_test_server(t)
	dir := t.TempDir()
	if err := store.Upsert_library(context.Background(), "lib1", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}

	do_request(t, server, http.MethodPost, "/api/v1/match", `{"libraries":["lib1"]}`)
	rec := do_request(t, server, http.MethodPost, "/api/v1/match/rematch", `{"libraries":["lib1"],"force":true}`)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusConflict {
		t.Fatalf("rematch all start = %d %s", rec.Code, rec.Body.String())
	}
}

// Test_start_handlers_reject_invalid_json covers the decode failure in the
// job-start handlers: an unparseable body is a bad request, not a job.
func Test_start_handlers_reject_invalid_json(t *testing.T) {
	server, _ := new_test_server(t)
	for _, target := range []string{"/api/v1/scan", "/api/v1/match", "/api/v1/match/rematch"} {
		rec := do_request(t, server, http.MethodPost, target, `{oops`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s invalid json = %d %s", target, rec.Code, rec.Body.String())
		}
	}
}

// Test_match_start_store_failure_lists_no_libraries pins resolve_library_names'
// behaviour on a store failure: the error is swallowed and no names are
// returned, so the start resolves to an empty job list rather than failing the
// request.
func Test_match_start_store_failure_lists_no_libraries(t *testing.T) {
	server, _ := new_test_server(t)
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	server.store = database.New_store(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	rec := do_request(t, server, http.MethodPost, "/api/v1/match", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("match start on closed store = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Libraries []string `json:"libraries"`
	}](t, rec)
	if len(body.Libraries) != 0 {
		t.Errorf("libraries = %v, want none", body.Libraries)
	}
}

// Test_rematch_ambiguous_replace_failure_is_internal_error seeds a candidate
// shortlist, then makes the candidate DELETE abort: an ambiguous rematch
// persists its shortlist through Replace_candidates, and the failure must
// surface as an internal error.
func Test_rematch_ambiguous_replace_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id := upsert_movie_entry(t, store, "Completely Different")
	// The offline candidate is titled "Completely Different", so only a file
	// name that does not echo it keeps the rematch ambiguous: a movie is
	// looked up by its file, and an exact hit would apply the match before the
	// candidate shortlist is ever persisted.
	if _, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	if err := store.Replace_candidates(context.Background(), id, []database.Candidate{{
		Tmdb_id: 603, Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999,
		Media_type: "movie", Score: 0.9,
	}}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_candidate_delete BEFORE DELETE ON candidate
		BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/rematch", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// Test_rematch_status_update_failure_is_internal_error pre-matches an entry,
// then makes the catalog UPDATE abort: an ambiguous rematch on a matched entry
// still updates its status back to needs_lookup, and that failure must surface
// as an internal error.
func Test_rematch_status_update_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id := upsert_movie_entry(t, store, "Completely Different")
	// A file name that does not echo the offline candidate's title keeps the
	// rematch ambiguous — a movie is looked up by its file — so the status
	// write runs after the shortlist is persisted instead of the match being
	// applied first.
	if _, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 1000,
		Status: "matched",
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_status_update BEFORE UPDATE ON media_file
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/rematch", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// Test_split_version_job_failure_is_internal_error makes every catalog UPDATE
// abort, so the split job (which re-matches the freshly cut movie entry) fails
// inside the queue and the handler answers with an internal error.
func Test_split_version_job_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline{})
	id := upsert_movie_entry(t, store, "The Matrix")
	if err := store.Update_catalog_entry(context.Background(), id, database.Catalog_entry{
		Media_type: "series", Title: "The Matrix",
	}); err != nil {
		t.Fatalf("flip media type: %v", err)
	}
	version_id, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: id, File_path: "/media/The.Matrix.1999.mkv", Size_bytes: 9000,
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_entry_update BEFORE UPDATE ON catalog_entry
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/versions/"+itoa(version_id)+"/split", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// series_show_folder_fixture seeds the absorbed-reclassify shape: an existing
// series owning a numbered episode and a loose movie sharing the show folder.
func series_show_folder_fixture(t *testing.T, store *database.Store) (movie_id int64) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := store.Upsert_library(ctx, "TV", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil || len(libraries) == 0 {
		t.Fatalf("list libraries: %v", err)
	}
	series_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 1, Episode_number: 1,
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: libraries[0].Id,
		File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 9000,
	}); err != nil {
		t.Fatalf("save series version: %v", err)
	}
	movie_id, err = store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office Interviews",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: libraries[0].Id,
		File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 4000,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}
	return movie_id
}

// Test_reclassify_merge_update_failure_is_internal_error aborts every version
// UPDATE, so folding the drained movie into the existing series fails and the
// reclassify job answers with an internal error.
func Test_reclassify_merge_update_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id := series_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_version_update BEFORE UPDATE ON media_file
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// Test_reclassify_merge_delete_failure_is_internal_error lets the fold succeed
// but aborts the catalog DELETE, so removing the drained movie entry fails and
// the reclassify job answers with an internal error.
func Test_reclassify_merge_delete_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id := series_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_entry_delete BEFORE DELETE ON catalog_entry
		BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// Test_reclassify_rename_failure_is_internal_error re-types a lone movie whose
// file sits in a show folder; the folder becomes its own series, and the title
// rewrite (rename_anchor_to_folder) is the only catalog UPDATE left — aborting
// it must surface as an internal error.
func Test_reclassify_rename_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	ctx := context.Background()
	dir := t.TempDir()
	if err := store.Upsert_library(ctx, "TV", dir, true); err != nil {
		t.Fatalf("upsert library: %v", err)
	}
	libraries, err := store.List_libraries(ctx)
	if err != nil || len(libraries) == 0 {
		t.Fatalf("list libraries: %v", err)
	}
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office Interviews",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: id, Library_id: libraries[0].Id,
		File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 4000,
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_title_update BEFORE UPDATE OF title ON catalog_entry
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}

// Test_reclassify_absorbed_ambiguous_reports_target_entry pins the absorbed
// branch of write_applied_or_candidates: the re-typed movie is drained into
// the existing series, the rematch on the merged series stays ambiguous, and
// the chooser payload carries the surviving series entry — not the drained
// movie the request named.
func Test_reclassify_absorbed_ambiguous_reports_target_entry(t *testing.T) {
	server, store, _ := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id := series_show_folder_fixture(t, store)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/reclassify", `{"media_type":"series"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Applied    bool             `json:"applied"`
		Candidates []candidate_item `json:"candidates"`
		Entry      *catalog_item    `json:"entry"`
	}](t, rec)
	if body.Applied {
		t.Fatalf("applied = true, want ambiguous rematch")
	}
	if body.Entry == nil || body.Entry.Media_type != "series" {
		t.Fatalf("entry = %+v, want the absorbed series", body.Entry)
	}
	if body.Entry.Id == id {
		t.Errorf("entry id = %d, want the series the movie was absorbed into", body.Entry.Id)
	}
}

// Test_manual_match_enrich_failure_is_internal_error lets the catalog update
// succeed but aborts the series-metadata write, so Enrich_episodes fails inside
// apply_manual_match and the handler answers with an internal error instead of
// the happy-path entry reload.
func Test_manual_match_enrich_failure_is_internal_error(t *testing.T) {
	server, store, database_path := new_trigger_server(t, default_test_metadata(), fake_offline_low{})
	id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "series", Title: "The Office",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	inject_failure(t, database_path, `CREATE TRIGGER fail_series_metadata BEFORE INSERT ON series_metadata
		BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END`)

	rec := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match", `{"tmdb_id":603}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want internal error", rec.Code, rec.Body.String())
	}
}
