package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
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
	movies        map[int]*tmdb.Movie_details
	tv            map[int]*tmdb.Tv_details
	search_movies []tmdb.Movie_search_result
	search_tv     []tmdb.Tv_search_result
}

func (f fake_metadata) Search_movie(_ context.Context, _ string, _ int) ([]tmdb.Movie_search_result, error) {
	return f.search_movies, nil
}

func (f fake_metadata) Search_tv(_ context.Context, _ string, _ int) ([]tmdb.Tv_search_result, error) {
	return f.search_tv, nil
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

// fake_local supplies the search box from the local IMDb index, like the real
// Matcher_source-backed server.
type fake_local struct {
	hits []matcher.Offline_candidate
}

func (f fake_local) Search_local(_ context.Context, query string, _ int, _ scanner.Media_type, _ int) ([]matcher.Offline_candidate, error) {
	var hits []matcher.Offline_candidate
	for _, hit := range f.hits {
		if strings.Contains(strings.ToLower(hit.Title), strings.ToLower(query)) {
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

func (f fake_local) Search(_ context.Context, _ string, _ int, _ scanner.Media_type) ([]matcher.Offline_candidate, error) {
	return nil, nil
}

// fake_offline_low supplies an offline candidate with a low match score, so a
// rematch stays ambiguous and surfaces the candidate shortlist instead of
// applying a match.
type fake_offline_low struct{}

func (fake_offline_low) Search(_ context.Context, _ string, _ int, kind scanner.Media_type) ([]matcher.Offline_candidate, error) {
	if kind != scanner.Movie {
		return nil, nil
	}
	return []matcher.Offline_candidate{{
		Imdb_id: "tt9999999", Title: "Completely Different", Year: 1999, Media_type: scanner.Movie,
	}}, nil
}

// fake_offline_lookup extends fake_local so manual matching can resolve an
// exact IMDb id from the local index when TMDB is not configured.
type fake_offline_lookup struct {
	fake_local
	results map[string]*matcher.Result
}

func (f fake_offline_lookup) Lookup(_ context.Context, imdb_id string) (*matcher.Result, bool) {
	result, ok := f.results[imdb_id]
	return result, ok
}

func new_test_server(t *testing.T) (*Server, *database.Store) {
	return new_test_server_with_offline(t, fake_offline{})
}

func new_test_server_with_offline(t *testing.T, offline matcher.Offline_source) (*Server, *database.Store) {
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
	m := matcher.New(matcher.Options{Metadata: metadata, Offline: offline, Logger: logger})
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

func Test_Watch_scan_runs_through_job_manager(t *testing.T) {
	server, _ := new_test_server(t)
	ctx := context.Background()

	result, err := server.Watch_scan(ctx, nil)
	if err != nil {
		t.Fatalf("watch_scan: %v", err)
	}
	if result == nil {
		t.Fatal("watch_scan returned nil result")
	}

	snapshot := server.jobs.Snapshot()
	if snapshot == nil {
		t.Fatal("no job snapshot recorded")
	}
	if snapshot.Running {
		t.Errorf("job still running after sync scan")
	}
	if snapshot.Result == nil {
		t.Errorf("job snapshot has no result")
	}
}

func Test_Watch_scan_reports_busy(t *testing.T) {
	server, _ := new_test_server(t)
	ctx := context.Background()

	release := make(chan struct{})
	started := make(chan struct{})
	defer close(release)
	_, err := server.jobs.Start(func(_ context.Context, _ func(scan.Progress)) (*scan.Result, error) {
		close(started)
		<-release
		return &scan.Result{}, nil
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-started

	_, err = server.Watch_scan(ctx, nil)
	if !errors.Is(err, scan.Err_scan_in_progress) {
		t.Fatalf("watch_scan error = %v, want scan.Err_scan_in_progress", err)
	}
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

func Test_categories(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	for _, entry := range []database.Catalog_entry{
		{Media_type: "movie", Title: "The Matrix", Status: "matched", Genres: []string{"Action"}},
		{Media_type: "movie", Title: "Alien", Status: "matched", Genres: []string{"Action", "Science Fiction"}},
		{Media_type: "movie", Title: "Needs Review", Status: "needs_lookup"},
	} {
		if _, err := store.Upsert_catalog_entry(ctx, entry); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	response := do_request(t, server, http.MethodGet, "/api/v1/categories", "")
	if response.Code != http.StatusOK {
		t.Fatalf("categories status = %d: %s", response.Code, response.Body)
	}
	body := decode[struct {
		Genres    []category_item `json:"genres"`
		Unmatched int             `json:"unmatched"`
	}](t, response)
	if body.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1", body.Unmatched)
	}
	if len(body.Genres) != 2 {
		t.Fatalf("genres = %+v, want 2", body.Genres)
	}
	if body.Genres[0].Name != "Action" || body.Genres[0].Count != 2 ||
		body.Genres[1].Name != "Science Fiction" || body.Genres[1].Count != 1 {
		t.Errorf("genres = %+v", body.Genres)
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

func Test_unmatched_lists_candidate_entries_first(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()

	late := func(title string) int64 {
		time.Sleep(1 * time.Millisecond)
		id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
			Media_type: "movie", Title: title, Status: "needs_lookup",
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		return id
	}
	plain := late("Plain entry")
	with_candidates := late("With candidates")
	other := late("Other plain entry")
	if err := store.Replace_candidates(ctx, with_candidates, []database.Candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: "movie",
	}}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}

	unmatched := do_request(t, server, http.MethodGet, "/api/v1/unmatched", "")
	if unmatched.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", unmatched.Code, unmatched.Body)
	}
	body := decode[struct {
		Entries []catalog_item `json:"entries"`
	}](t, unmatched)
	if len(body.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(body.Entries))
	}
	if body.Entries[0].Id != with_candidates || len(body.Entries[0].Candidates) != 1 {
		t.Fatalf("first entry = %+v, want the one with candidates", body.Entries[0])
	}
	seen := map[int64]bool{}
	for _, entry := range body.Entries[1:] {
		if len(entry.Candidates) != 0 {
			t.Fatalf("candidate-bearing entry not first: %+v", entry)
		}
		seen[entry.Id] = true
	}
	if !seen[plain] || !seen[other] {
		t.Fatalf("plain entries missing from tail: %+v", body.Entries)
	}
}

func Test_manual_match_without_tmdb_key(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_lookup{
		fake_local: fake_local{hits: []matcher.Offline_candidate{
			{Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie},
		}},
		results: map[string]*matcher.Result{
			"tt0133093": {
				Matched: true, Confidence: 1, Source: "imdb-datasets",
				Media_type: scanner.Movie, Imdb_id: "tt0133093",
				Title: "The Matrix", Original_title: "The Matrix", Year: 1999,
				Rating: 8.7, Vote_count: 2500000,
			},
		},
	})
	server.metadata = nil
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Matching by a bare IMDb id must work without TMDB when the offline index
	// knows the title.
	match := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match",
		`{"imdb_id": "tt0133093", "media_type": "movie"}`)
	if match.Code != http.StatusOK {
		t.Fatalf("imdb match status = %d: %s", match.Code, match.Body)
	}
	match_body := decode[struct {
		Entry catalog_item `json:"entry"`
	}](t, match)
	if match_body.Entry.Status != "matched" || match_body.Entry.Title != "The Matrix" ||
		match_body.Entry.Imdb_id != "tt0133093" || match_body.Entry.Tmdb_id != 0 {
		t.Fatalf("unexpected matched entry: %+v", match_body.Entry)
	}

	// An unknown id must surface a clear error instead of a false match.
	other_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "other", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	missing := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(other_id)+"/match",
		`{"imdb_id": "tt9999999", "media_type": "movie"}`)
	if missing.Code == http.StatusOK {
		t.Fatalf("unknown imdb id accepted: %s", missing.Body)
	}
}

func Test_manual_match_tmdb_id_falls_back_to_persisted_offline_candidate(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_lookup{
		fake_local: fake_local{hits: []matcher.Offline_candidate{
			{Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie},
		}},
		results: map[string]*matcher.Result{
			"tt0133093": {
				Matched: true, Confidence: 1, Source: "imdb-datasets",
				Media_type: scanner.Movie, Imdb_id: "tt0133093",
				Title: "The Matrix", Original_title: "The Matrix", Year: 1999,
			},
		},
	})
	server.metadata = nil
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "matrix", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A candidate persisted from an earlier rematch carries only a tmdb id that
	// maps to an offline-resolvable imdb id.
	if err := store.Replace_candidates(ctx, id, []database.Candidate{{
		Tmdb_id: 603, Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: "movie",
	}}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}

	match := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match",
		`{"tmdb_id": 603, "media_type": "movie"}`)
	if match.Code != http.StatusOK {
		t.Fatalf("match status = %d: %s", match.Code, match.Body)
	}
	match_body := decode[struct {
		Entry catalog_item `json:"entry"`
	}](t, match)
	if match_body.Entry.Status != "matched" || match_body.Entry.Title != "The Matrix" ||
		match_body.Entry.Imdb_id != "tt0133093" {
		t.Fatalf("unexpected matched entry: %+v", match_body.Entry)
	}

	// A tmdb id with no persisted candidate still fails cleanly.
	match_unknown := do_request(t, server, http.MethodPost, "/api/v1/catalog/"+itoa(id)+"/match",
		`{"tmdb_id": 999, "media_type": "movie"}`)
	if match_unknown.Code == http.StatusOK {
		t.Fatalf("unknown tmdb id accepted: %s", match_unknown.Body)
	}
}

func Test_search_local_merge(t *testing.T) {
	server, _ := new_test_server(t)
	server.matcher = matcher.New(matcher.Options{
		Offline: fake_local{hits: []matcher.Offline_candidate{
			{Imdb_id: "tt0295432", Title: "The Matrix Revisited", Year: 2001, Media_type: scanner.Movie},
			{Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie},
		}},
	})

	server.metadata = fake_metadata{
		search_movies: []tmdb.Movie_search_result{
			{Id: 603, Title: "The Matrix", Release_date: "1999-03-31", Overview: "A hacker.", Poster_path: "/matrix.jpg", Vote_average: 8.2},
		},
	}

	t.Run("merged", func(t *testing.T) {
		found := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix&media_type=movie", "")
		if found.Code != http.StatusOK {
			t.Fatalf("search status = %d: %s", found.Code, found.Body)
		}
		got := decode[struct {
			Results []search_item `json:"results"`
		}](t, found)
		if len(got.Results) != 2 {
			t.Fatalf("results = %d, want 2 (TMDB + 1 local, one local deduped): %+v", len(got.Results), got.Results)
		}

		first := got.Results[0]
		if first.Tmdb_id != 603 || first.Imdb_id != "" || first.Title != "The Matrix" || first.Year != 1999 {
			t.Fatalf("unexpected first result: %+v", first)
		}
		revisited := got.Results[1]
		if revisited.Tmdb_id != 0 || revisited.Imdb_id != "tt0295432" || revisited.Title != "The Matrix Revisited" || revisited.Year != 2001 {
			t.Fatalf("unexpected merged local result: %+v", revisited)
		}
	})

	t.Run("local only", func(t *testing.T) {
		server.metadata = nil
		local_only := do_request(t, server, http.MethodGet, "/api/v1/search?q=revisited&media_type=movie", "")
		if local_only.Code != http.StatusOK {
			t.Fatalf("metadata-less search status = %d, want 200: %s", local_only.Code, local_only.Body)
		}
		local_body := decode[struct {
			Results []search_item `json:"results"`
		}](t, local_only)
		if len(local_body.Results) != 1 || local_body.Results[0].Imdb_id != "tt0295432" {
			t.Fatalf("unexpected local-only results: %+v", local_body.Results)
		}
	})
}

func Test_search_autocomplete(t *testing.T) {
	server, _ := new_test_server(t)

	movie := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix&media_type=movie", "")
	body := decode[struct {
		Results []search_item `json:"results"`
	}](t, movie)
	if movie.Code != http.StatusOK || len(body.Results) != 0 {
		t.Fatalf("movie search = %d: %s", movie.Code, movie.Body)
	}

	server.metadata = fake_metadata{
		search_movies: []tmdb.Movie_search_result{
			{Id: 603, Title: "The Matrix", Release_date: "1999-03-31", Overview: "A hacker.", Poster_path: "/matrix.jpg", Vote_average: 8.2},
			{Id: 624860, Title: "The Matrix Resurrections", Release_date: "2021-12-22", Overview: "Third sequel.", Vote_average: 6.5},
			{Id: 605, Title: "The Matrix Reloaded", Release_date: "2003-05-15", Overview: "Second.", Vote_average: 7.2},
		},
		search_tv: []tmdb.Tv_search_result{
			{Id: 160, Name: "Lost", First_air_date: "2004-09-22", Overview: "Island.", Vote_average: 8.5},
		},
	}

	found := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix&media_type=movie", "")
	found_body := decode[struct {
		Results []search_item `json:"results"`
	}](t, found)
	if found.Code != http.StatusOK {
		t.Fatalf("search status = %d: %s", found.Code, found.Body)
	}
	if len(found_body.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(found_body.Results))
	}
	first := found_body.Results[0]
	if first.Tmdb_id != 603 || first.Title != "The Matrix" || first.Year != 1999 ||
		first.Media_type != "movie" || first.Poster_url != "https://image.tmdb.org/t/p/w92/matrix.jpg" || first.Vote_average != 8.2 {
		t.Fatalf("unexpected first result: %+v", first)
	}

	tv := do_request(t, server, http.MethodGet, "/api/v1/search?q=lost&media_type=series&year=2004", "")
	tv_body := decode[struct {
		Results []search_item `json:"results"`
	}](t, tv)
	if len(tv_body.Results) != 1 || tv_body.Results[0].Tmdb_id != 160 || tv_body.Results[0].Year != 2004 {
		t.Fatalf("unexpected tv results: %+v", tv_body.Results)
	}

	empty := do_request(t, server, http.MethodGet, "/api/v1/search?media_type=movie", "")
	if empty.Code != http.StatusBadRequest {
		t.Errorf("empty query status = %d, want 400", empty.Code)
	}

	server.metadata = nil
	unconfigured := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix", "")
	if unconfigured.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured status = %d, want 503", unconfigured.Code)
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

// matched_entry_with_version stores an already-matched catalog entry with a
// version, the starting point of a rematch-all run.
func matched_entry_with_version(t *testing.T, store *database.Store) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Imdb_id: "tt0000000", Status: "matched",
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

// wait_for_match_result polls until the match job reports want_matched entries,
// tolerating an intermediate finished snapshot from a queued run's predecessor.
func wait_for_match_result(t *testing.T, server *Server, want_matched int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := do_request(t, server, http.MethodGet, "/api/v1/match/status", "")
		body := decode[struct {
			Job *match_job_response `json:"job"`
		}](t, status)
		if body.Job != nil && body.Job.Result != nil && body.Job.Result.Matched == want_matched {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("match job never reported %d matched entries", want_matched)
}

func Test_rematch_all_re_runs_matched_entries(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id := matched_entry_with_version(t, store)

	start := do_request(t, server, http.MethodPost, "/api/v1/match/rematch", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body)
	}
	wait_for_match_result(t, server, 1)

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Imdb_id != "tt0133093" {
		t.Fatalf("entry = %+v, want re-matched matrix (imdb tt0133093)", entry)
	}
}

func Test_rematch_all_queues_behind_running_job(t *testing.T) {
	server, _ := new_test_server(t)
	id := unmatched_entry_with_version(t, server.store)

	block := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = server.matches.Start(func(job_ctx context.Context, _ func(matching.Progress)) (*matching.Result, error) {
			close(started)
			<-block
			return &matching.Result{Total: 1}, nil
		})
	}()
	<-started
	defer server.matches.Cancel()

	start := do_request(t, server, http.MethodPost, "/api/v1/match/rematch", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("rematch all while job running = %d, want 202 (queued): %s", start.Code, start.Body)
	}

	close(block)
	wait_for_match_result(t, server, 1)

	entry, err := server.store.Get_catalog_entry(context.Background(), id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" {
		t.Fatalf("entry status = %s, want matched after queued rematch all", entry.Status)
	}
}

// query_capture answers the offline search only for an exact wanted query and
// records what the matcher asked for, so a test can prove the search was
// driven by the folder name rather than the episode file name.
type query_capture struct {
	want string
	got  string
	year int
}

func (q *query_capture) Search(_ context.Context, query string, year int, _ scanner.Media_type) ([]matcher.Offline_candidate, error) {
	q.got = query
	q.year = year
	if !strings.EqualFold(query, q.want) {
		return nil, nil
	}
	return []matcher.Offline_candidate{{
		Imdb_id: "tt0133093", Title: "The Matrix", Year: 1999, Media_type: scanner.Movie,
	}}, nil
}

func Test_match_job_searches_series_by_folder(t *testing.T) {
	capture := &query_capture{want: "The Matrix"}
	server, store := new_test_server_with_offline(t, capture)
	ctx := context.Background()

	id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "Whatever", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, File_path: "The.Matrix.1999/Season 1/The.Matrix.S01E01.mkv",
		Size_bytes: 9000, Hash: "hash-1",
	}); err != nil {
		t.Fatalf("save version: %v", err)
	}

	start := do_request(t, server, http.MethodPost, "/api/v1/match", "")
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d: %s", start.Code, start.Body)
	}
	wait_for_match_job(t, server, 1)

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" || entry.Imdb_id != "tt0133093" {
		t.Fatalf("entry = %+v, want matched matrix", entry)
	}
	if !strings.EqualFold(capture.got, "The Matrix") {
		t.Fatalf("matcher searched %q, want the series folder name 'The Matrix'", capture.got)
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

func Test_matching_unavailable_message(t *testing.T) {
	server, _ := new_test_server(t)

	server.cfg.Imdb_datasets_path = "/data/imdb"
	server.datasets = imdb_datasets.New_tracker()
	server.datasets.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_stale})
	msg := server.matching_unavailable_message()
	if !strings.Contains(msg, "local IMDb index is still being built") {
		t.Fatalf("datasets-but-not-ready message = %q", msg)
	}

	server.cfg.Imdb_datasets_path = ""
	server.cfg.Api.Tmdb_key = ""
	msg = server.matching_unavailable_message()
	if !strings.Contains(msg, "matching is not configured") {
		t.Fatalf("nothing-configured message = %q", msg)
	}

	server.cfg.Api.Opensubtitles_api_key = "key"
	msg = server.matching_unavailable_message()
	if !strings.Contains(msg, "configured but unavailable") {
		t.Fatalf("api-not-ready message = %q", msg)
	}
}

func Test_match_start_reports_unavailable(t *testing.T) {
	server, _ := new_test_server(t)
	server.matcher = matcher.New(matcher.Options{})

	start := do_request(t, server, http.MethodPost, "/api/v1/match", "")
	if start.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status = %d, want 503: %s", start.Code, start.Body)
	}
}

func Test_background_status_includes_datasets(t *testing.T) {
	server, _ := new_test_server(t)
	server.datasets = imdb_datasets.New_tracker()
	server.datasets.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_stale, Total: 100})
	server.datasets.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_import, Dataset: "title.basics", Bytes: 50, Total: 100})

	response := do_request(t, server, http.MethodGet, "/api/v1/background", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	body := decode[background_status](t, response)
	if body.Datasets == nil {
		t.Fatal("datasets status is nil")
	}
	if string(body.Datasets.State) != string(imdb_datasets.State_building) || body.Datasets.Percent != 50 {
		t.Fatalf("datasets = %+v, want building at 50%%", body.Datasets)
	}
}

func Test_matching_unavailable_message_includes_percent(t *testing.T) {
	server, _ := new_test_server(t)
	server.cfg.Imdb_datasets_path = "/data/imdb"
	server.datasets = imdb_datasets.New_tracker()
	server.datasets.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_stale, Total: 100})
	server.datasets.Observe(imdb_datasets.Build_progress{Step: imdb_datasets.Build_import, Bytes: 25, Total: 100})

	msg := server.matching_unavailable_message()
	if !strings.Contains(msg, "25%") {
		t.Fatalf("message = %q, want percent", msg)
	}
}

func Test_rematch_ambiguous_persists_candidates(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_low{})
	ctx := context.Background()
	id := unmatched_entry_with_version(t, store)

	response := do_request(t, server, http.MethodPost, fmt.Sprintf("/api/v1/catalog/%d/rematch", id), "")
	if response.Code != http.StatusOK {
		t.Fatalf("rematch status = %d: %s", response.Code, response.Body)
	}
	body := decode[struct {
		Applied    bool             `json:"applied"`
		Candidates []candidate_item `json:"candidates"`
	}](t, response)
	if body.Applied {
		t.Fatal("rematch applied despite low-confidence candidates")
	}
	if len(body.Candidates) != 1 || body.Candidates[0].Imdb_id != "tt9999999" {
		t.Fatalf("candidates = %+v", body.Candidates)
	}

	persisted, err := store.List_candidates(ctx, id)
	if err != nil {
		t.Fatalf("list persisted candidates: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Title != "Completely Different" {
		t.Fatalf("persisted candidates = %+v", persisted)
	}

	entry, err := store.Get_catalog_entry(ctx, id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "needs_lookup" {
		t.Fatalf("entry status = %q, want needs_lookup", entry.Status)
	}
}

func Test_rematch_exposes_candidates_in_unmatched_and_detail(t *testing.T) {
	server, store := new_test_server_with_offline(t, fake_offline_low{})
	id := unmatched_entry_with_version(t, store)

	rematch := do_request(t, server, http.MethodPost, fmt.Sprintf("/api/v1/catalog/%d/rematch", id), "")
	if rematch.Code != http.StatusOK {
		t.Fatalf("rematch status = %d: %s", rematch.Code, rematch.Body)
	}

	unmatched := do_request(t, server, http.MethodGet, "/api/v1/unmatched", "")
	un := decode[struct {
		Entries []catalog_item `json:"entries"`
	}](t, unmatched)
	for _, entry := range un.Entries {
		if entry.Id == id {
			if len(entry.Candidates) != 1 || entry.Candidates[0].Imdb_id != "tt9999999" {
				t.Fatalf("unmatched candidates = %+v", entry.Candidates)
			}
		}
	}

	detail := do_request(t, server, http.MethodGet, fmt.Sprintf("/api/v1/catalog/%d", id), "")
	det := decode[detail_response](t, detail)
	if len(det.Entry.Candidates) != 1 || det.Entry.Candidates[0].Imdb_id != "tt9999999" {
		t.Fatalf("detail candidates = %+v", det.Entry.Candidates)
	}

	status := do_request(t, server, http.MethodGet, "/api/v1/match/status", "")
	snap := decode[struct {
		Job *match_job_response `json:"job"`
	}](t, status)
	if snap.Job == nil || snap.Job.Running {
		t.Fatalf("no finished rematch job in snapshot: %+v", snap.Job)
	}
	if snap.Job.Result == nil || snap.Job.Result.Unmatched != 1 || snap.Job.Result.Candidates != 1 {
		t.Fatalf("job result = %+v, want unmatched=1 candidates=1", snap.Job.Result)
	}
}

func Test_rematch_applied_path_clears_candidates(t *testing.T) {
	server, store := new_test_server(t)
	ctx := context.Background()
	id := unmatched_entry_with_version(t, store)
	if err := store.Replace_candidates(ctx, id, []database.Candidate{{Tmdb_id: 42, Title: "Stale", Media_type: "movie"}}); err != nil {
		t.Fatalf("seed candidates: %v", err)
	}

	response := do_request(t, server, http.MethodPost, fmt.Sprintf("/api/v1/catalog/%d/rematch", id), "")
	if response.Code != http.StatusOK {
		t.Fatalf("rematch status = %d: %s", response.Code, response.Body)
	}
	body := decode[struct {
		Applied bool `json:"applied"`
	}](t, response)
	if !body.Applied {
		t.Fatal("rematch did not apply the confident offline match")
	}
	left, err := store.List_candidates(ctx, id)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("candidates survived an applied rematch: %+v", left)
	}
}

func Test_rematch_queues_behind_running_job(t *testing.T) {
	server, _ := new_test_server(t)
	id := unmatched_entry_with_version(t, server.store)

	block := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = server.matches.Start(func(job_ctx context.Context, _ func(matching.Progress)) (*matching.Result, error) {
			close(started)
			<-block
			return &matching.Result{Total: 1}, nil
		})
	}()
	<-started
	defer server.matches.Cancel()

	response_ch := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response_ch <- do_request(t, server, http.MethodPost, fmt.Sprintf("/api/v1/catalog/%d/rematch", id), "")
	}()

	select {
	case <-response_ch:
		t.Fatal("rematch answered while a matching job was running instead of queueing")
	case <-time.After(100 * time.Millisecond):
	}

	close(block)
	select {
	case response := <-response_ch:
		if response.Code != http.StatusOK {
			t.Fatalf("queued rematch = %d, want 200: %s", response.Code, response.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued rematch did not finish after the running job stopped")
	}

	entry, err := server.store.Get_catalog_entry(context.Background(), id)
	if err != nil || entry == nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Status != "matched" {
		t.Fatalf("entry status = %s, want matched after queued rematch", entry.Status)
	}
}
