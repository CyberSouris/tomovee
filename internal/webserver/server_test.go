package webserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	"github.com/cybersouris/tomovee/internal/webui"
)

type fake_metadata struct {
	movies map[int]*tmdb.Movie_details
	tv     map[int]*tmdb.Tv_details
}

func (f fake_metadata) Search_movie(context.Context, string, int) ([]tmdb.Movie_search_result, error) {
	return nil, nil
}

func (f fake_metadata) Search_tv(context.Context, string, int) ([]tmdb.Tv_search_result, error) {
	return nil, nil
}

func (f fake_metadata) Movie_details(_ context.Context, id int) (*tmdb.Movie_details, error) {
	if details, ok := f.movies[id]; ok {
		return details, nil
	}
	return &tmdb.Movie_details{}, nil
}

func (f fake_metadata) Tv_details(_ context.Context, id int) (*tmdb.Tv_details, error) {
	if details, ok := f.tv[id]; ok {
		return details, nil
	}
	return &tmdb.Tv_details{}, nil
}

func (f fake_metadata) Find_by_imdb(context.Context, string) (*tmdb.Find_result, error) {
	return &tmdb.Find_result{}, nil
}

// fake_offline supplies a canned offline candidate so the background matching
// job can auto-match without network access.
type fake_offline struct{}

func (fake_offline) Search(_ context.Context, _ string, _ int, _ scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return []matcher.Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}, nil
}

func new_test_server(t *testing.T) (*Server, *database.Store) {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	store := database.New_store(d)
	metadata := fake_metadata{
		movies: map[int]*tmdb.Movie_details{
			603: {
				Id: 603, Imdb_id: "tt0133093", Title: "The Matrix",
				Original_title: "The Matrix", Release_date: "1999-03-31",
				Overview: "A hacker learns the truth.", Vote_average: 8.2, Vote_count: 24000,
				Runtime: 136, Poster_path: "/matrix.jpg",
				Genres: []tmdb.Genre{{Id: 28, Name: "Action"}},
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: metadata, Offline: fake_offline{}, Logger: logger})
	runner := scan.New(store, scan.Options{Logger: logger})
	matching_service := matching.New(store, m, nil, logger)
	cfg := &config.Config{
		Listen:           "127.0.0.1:0",
		Database_path:    ":memory:",
		Poster_cache_dir: t.TempDir(),
		Api:              config.Api_config{Tmdb_key: "test"},
	}
	server := New(Options{
		Store: store, Config: cfg, Matcher: m, Metadata: metadata,
		Scanner: runner, Matching: matching_service, Static: webui.FS(), Logger: logger,
	})
	return server, store
}

func do_request(t *testing.T, server *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return out
}

func Test_catalog_list_and_detail(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Imdb_id: "tt0133093", Tmdb_id: 603, Poster_path: "/matrix.jpg",
		Status: "matched", Genres: []string{"Action"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	list := do_request(t, server, http.MethodGet, "/api/v1/catalog?q=matrix", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body)
	}
	list_body := decode[struct {
		Count   int            `json:"count"`
		Entries []catalog_item `json:"entries"`
	}](t, list)
	if list_body.Count != 1 || list_body.Entries[0].Poster_url != "/api/v1/posters/1" {
		t.Fatalf("unexpected list: %+v", list_body)
	}

	detail := do_request(t, server, http.MethodGet, "/api/v1/catalog/"+itoa(id), "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detail.Code, detail.Body)
	}
	detail_body := decode[detail_response](t, detail)
	if detail_body.Entry.Title != "The Matrix" {
		t.Errorf("title = %q", detail_body.Entry.Title)
	}
}

func Test_empty_lists_encode_as_arrays(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "Empty Show", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	detail := do_request(t, server, http.MethodGet, "/api/v1/catalog/"+itoa(id), "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detail.Code, detail.Body)
	}
	body := detail.Body.String()
	for _, want := range []string{`"genres":[]`, `"episodes":[]`, `"versions":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail body missing %s: %s", want, body)
		}
	}

	settings := do_request(t, server, http.MethodGet, "/api/v1/settings", "")
	if settings.Code != http.StatusOK {
		t.Fatalf("settings status = %d: %s", settings.Code, settings.Body)
	}
	settings_body := settings.Body.String()
	for _, want := range []string{`"libraries":[]`} {
		if !strings.Contains(settings_body, want) {
			t.Errorf("settings body missing %s: %s", want, settings_body)
		}
	}
}

func Test_poster_serves_local_frame(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "Mystery", Status: "needs_lookup",
		Poster_path: thumbnail.Local_marker,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	local := thumbnail.Local_path(server.cfg.Poster_cache_dir, id)
	if err := os.WriteFile(local, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	recorder := do_request(t, server, http.MethodGet, "/api/v1/posters/"+itoa(id), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if recorder.Body.String() != "jpeg-bytes" {
		t.Errorf("body = %q", recorder.Body.String())
	}
}

func Test_unmatched_and_manual_match(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	unmatched := do_request(t, server, http.MethodGet, "/api/v1/unmatched", "")
	unmatched_body := decode[struct {
		Count int `json:"count"`
	}](t, unmatched)
	if unmatched_body.Count != 1 {
		t.Fatalf("unmatched count = %d", unmatched_body.Count)
	}

	match := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match",
		`{"tmdb_id": 603, "media_type": "movie"}`)
	if match.Code != http.StatusOK {
		t.Fatalf("match status = %d: %s", match.Code, match.Body)
	}
	match_body := decode[struct {
		Entry catalog_item `json:"entry"`
	}](t, match)
	if match_body.Entry.Status != "matched" || match_body.Entry.Title != "The Matrix" {
		t.Fatalf("unexpected matched entry: %+v", match_body.Entry)
	}
	if match_body.Entry.Tmdb_id != 603 {
		t.Errorf("tmdb id = %d", match_body.Entry.Tmdb_id)
	}
}

func Test_settings_update(t *testing.T) {
	server, _ := new_test_server(t)

	update := do_request(t, server, http.MethodPut, "/api/v1/settings",
		`{"watch_enabled": true, "libraries": [{"name": "Movies", "path": "/media/movies", "enabled": true}]}`)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", update.Code, update.Body)
	}
	body := decode[settings_response](t, update)
	if !body.Watch_enabled {
		t.Errorf("watch_enabled = false")
	}
	if len(body.Libraries) != 1 || body.Libraries[0].Name != "Movies" || body.Libraries[0].Path != "/media/movies" || !body.Libraries[0].Enabled {
		t.Fatalf("libraries = %+v", body.Libraries)
	}
	if !body.Tmdb_configured {
		t.Errorf("tmdb_configured = false")
	}
}

func Test_poster_prune_removes_unreferenced(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	if _, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "Keep", Poster_path: "/keep.jpg", Status: "matched",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	dir := t.TempDir()
	for _, name := range []string{"1.jpg", "99.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	server.posters = poster_cache.New(poster_cache.Options{Dir: dir})

	response := do_request(t, server, http.MethodPost, "/api/v1/posters/prune", "")
	if response.Code != http.StatusOK {
		t.Fatalf("prune status = %d: %s", response.Code, response.Body)
	}
	body := decode[struct {
		Removed int `json:"removed"`
	}](t, response)
	if body.Removed != 1 {
		t.Errorf("removed = %d, want 1", body.Removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.jpg")); err != nil {
		t.Errorf("referenced poster removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "99.jpg")); !os.IsNotExist(err) {
		t.Errorf("unreferenced poster kept")
	}
}

func Test_scan_job_runs(t *testing.T) {
	server, _ := new_test_server(t)

	start := do_request(t, server, http.MethodPost, "/api/v1/scan", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := do_request(t, server, http.MethodGet, "/api/v1/scan/status", "")
		status_body := decode[struct {
			Job *job_response `json:"job"`
		}](t, status)
		if status_body.Job != nil && !status_body.Job.Running {
			if status_body.Job.Result == nil {
				t.Fatalf("finished job has no result")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan job did not finish in time")
}

func unmatched_entry_with_version(t *testing.T, store *database.Store) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: id, File_path: "/media/The.Matrix.1999.mkv",
		Size_bytes: 9000, Hash: "hash-1",
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}
	return id
}

func wait_for_match_job(t *testing.T, server *Server, want_matched int) *match_job_response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := do_request(t, server, http.MethodGet, "/api/v1/match/status", "")
		body := decode[struct {
			Job *match_job_response `json:"job"`
		}](t, status)
		if body.Job != nil && !body.Job.Running {
			if body.Job.Result == nil {
				t.Fatalf("finished match job has no result")
			}
			if body.Job.Result.Matched != want_matched {
				t.Fatalf("matched = %d, want %d (result %+v)",
					body.Job.Result.Matched, want_matched, body.Job.Result)
			}
			return body.Job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("match job did not finish in time")
	return nil
}

func Test_match_job_matches_offline(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id := unmatched_entry_with_version(t, store)

	start := do_request(t, server, http.MethodPost, "/api/v1/match", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body)
	}
	wait_for_match_job(t, server, 1)

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Imdb_id != "tt0133093" || entry.Title != "The Matrix" {
		t.Fatalf("entry = %+v, want matched matrix", entry)
	}
}

func Test_auto_match_runs_nonblocking(t *testing.T) {
	server, _ := new_test_server(t)
	unmatched_entry_with_version(t, server.store)

	server.Auto_match()

	status := do_request(t, server, http.MethodGet, "/api/v1/match/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status immediately after auto-match = %d", status.Code)
	}
	wait_for_match_job(t, server, 1)
}
