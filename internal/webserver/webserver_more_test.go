package webserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x?limit=10", nil)
	if got := int_query(req, "limit"); got != 10 {
		t.Errorf("int_query(limit) = %d, want 10", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/x?limit=abc", nil)
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
		Media_type: "series", Title: "Breaking Bad", Status: "matched",
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
		Title: "Pilot", Status: "matched",
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
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/libraries/", nil)
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
	if _, err := server.matches.Start(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
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
		Media_type: "movie", Title: "The Matrix", Release_year: 1999, Status: "needs_lookup",
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
		Media_type: "series", Title: "The Matrix", Status: "needs_lookup",
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
	if entry.Media_type != "movie" || entry.Status != "matched" {
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
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
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
		Media_type: "movie", Title: "Office Interviews", Status: "needs_lookup",
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
	if _, err := server.jobs.Start(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
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
	if _, err := server.matches.Start(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/scan/stream", nil).WithContext(ctx)
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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/match/stream", nil).WithContext(ctx)
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
		_, unsubscribe, err := server.jobs.Subscribe()
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

func Test_job_manager_subscribe_receives_progress(t *testing.T) {
	server, _ := new_test_server(t)
	ch, unsubscribe, err := server.jobs.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, run_err := server.jobs.Run_sync(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
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
		_, err := server.jobs.Run_sync(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
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
