package webserver

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/poster_cache"
	"github.com/cybersouris/tomovee/internal/scan"
)

// new_closed_db_server returns a server whose database has already been closed.
// Every store call a handler makes then takes its error path, which is the
// cheapest way to reach the internal_error branches that a healthy store never
// produces.
func new_closed_db_server(t *testing.T) *Server {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store := database.New_store(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Logger: logger})
	runner := scan.New(store, scan.Options{Logger: logger})
	server := New(Options{
		Store:    store,
		Config:   &config.Config{Database_path: ":memory:", Listen: "127.0.0.1:0"},
		Matcher:  m,
		Scanner:  runner,
		Matching: matching.New(store, m, nil, logger),
		Posters:  poster_cache.New(poster_cache.Options{Dir: t.TempDir(), Logger: logger}),
		// Wired so the handler gets past its own nil check and reaches the
		// store; the hook itself must never run in this test.
		Stream_datasets: func(string) { t.Error("stream_datasets called in a dead-database test") },
		Logger:          logger,
	})
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	return server
}

// Test_handlers_report_a_dead_database pins that a failing store surfaces as a
// 500 with a JSON error instead of a panic, a hang or a half-written body.
func Test_handlers_report_a_dead_database(t *testing.T) {
	server := new_closed_db_server(t)
	cases := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"catalog list", http.MethodGet, "/api/v1/catalog", ""},
		{"catalog detail", http.MethodGet, "/api/v1/catalog/1", ""},
		{"categories", http.MethodGet, "/api/v1/categories", ""},
		{"unmatched", http.MethodGet, "/api/v1/unmatched", ""},
		{"manual match", http.MethodPost, "/api/v1/catalog/1/match", `{"imdb_id":"tt0133093"}`},
		{"rematch entry", http.MethodPost, "/api/v1/catalog/1/rematch", ""},
		{"reclassify", http.MethodPost, "/api/v1/catalog/1/reclassify", `{"media_type":"series"}`},
		{"version split", http.MethodPost, "/api/v1/versions/1/split", ""},
		{"version episode", http.MethodPost, "/api/v1/versions/1/episode", `{"season":1,"episode":1}`},
		{"settings get", http.MethodGet, "/api/v1/settings", ""},
		{"settings put", http.MethodPut, "/api/v1/settings", `{"tmdb_key":"k"}`},
		{"settings put watch", http.MethodPut, "/api/v1/settings", `{"watch_enabled":true}`},
		{"settings put library", http.MethodPut, "/api/v1/settings", `{"libraries":[{"name":"X","path":"/x"}]}`},
		{"libraries list", http.MethodGet, "/api/v1/libraries", ""},
		{"library delete", http.MethodDelete, "/api/v1/libraries/Movies", ""},
		{"poster prune", http.MethodPost, "/api/v1/posters/prune", ""},
		{"datasets start", http.MethodPost, "/api/v1/datasets", `{"path":"/data"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := do_request(t, server, tc.method, tc.target, tc.body)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", recorder.Code, recorder.Body.String())
			}
			body := decode[struct {
				Error string `json:"error"`
			}](t, recorder)
			if body.Error == "" {
				t.Error("500 carried no error message")
			}
		})
	}
}

// Test_handlers_without_their_optional_services covers the 503 branches taken
// when the server was built without a matcher, a matching service or a reload
// hook, all of which are legal wiring combinations.
func Test_handlers_without_their_optional_services(t *testing.T) {
	t.Run("no matcher", func(t *testing.T) {
		server, _ := new_test_server(t)
		server.matcher = nil
		// Search needs one of the two sources; with the TMDB stub cleared too
		// it has neither and must say so instead of returning nothing.
		server.metadata = nil
		search := do_request(t, server, http.MethodGet, "/api/v1/search?q=matrix", "")
		if search.Code != http.StatusServiceUnavailable {
			t.Errorf("search status = %d, want 503: %s", search.Code, search.Body.String())
		}
		match := do_request(t, server, http.MethodPost, "/api/v1/catalog/1/match", `{"imdb_id":"tt0133093"}`)
		if match.Code != http.StatusServiceUnavailable {
			t.Errorf("match status = %d, want 503: %s", match.Code, match.Body.String())
		}
	})

	t.Run("no matching service", func(t *testing.T) {
		server, _ := new_test_server(t)
		server.matching = nil
		for _, target := range []string{"/api/v1/versions/1/split", "/api/v1/catalog/1/rematch"} {
			recorder := do_request(t, server, http.MethodPost, target, "")
			if recorder.Code != http.StatusServiceUnavailable {
				t.Errorf("%s status = %d, want 503: %s", target, recorder.Code, recorder.Body.String())
			}
		}
		recorder := do_request(t, server, http.MethodPost, "/api/v1/match", "")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("match status = %d, want 503: %s", recorder.Code, recorder.Body.String())
		}
		recorder = do_request(t, server, http.MethodPost, "/api/v1/match/rematch", "")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("rematch all status = %d, want 503: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("no reload hook", func(t *testing.T) {
		server, _ := new_test_server(t)
		recorder := do_request(t, server, http.MethodPost, "/api/v1/settings/reload", "")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("reload status = %d, want 503: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("no datasets stream", func(t *testing.T) {
		server, _ := new_test_server(t)
		recorder := do_request(t, server, http.MethodPost, "/api/v1/datasets", "")
		if recorder.Code != http.StatusNotFound {
			t.Errorf("datasets status = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("no datasets tracker", func(t *testing.T) {
		// Settings must still answer, just without the datasets block.
		server, _ := new_test_server(t)
		server.datasets = nil
		recorder := do_request(t, server, http.MethodGet, "/api/v1/settings", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
		}
		if body := recorder.Body.String(); contains(body, `"datasets"`) {
			t.Errorf("settings still report a datasets status: %s", body)
		}
	})
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// Test_streams_refuse_past_the_subscriber_limit pins the SSE fan-out cap. The
// limit is enforced on the job manager, so filling it directly is enough to make
// the next stream request fail without opening any real connections.
func Test_streams_refuse_past_the_subscriber_limit(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		subs    func(t *testing.T, server *Server)
		message string
	}{
		{
			name:   "scan stream",
			target: "/api/v1/scan/stream",
			subs: func(t *testing.T, server *Server) {
				for i := 0; i < max_subscribers; i++ {
					_, unsubscribe, err := server.jobs.Subscribe()
					if err != nil {
						t.Fatalf("subscribe %d: %v", i, err)
					}
					t.Cleanup(unsubscribe)
				}
			},
			message: "too many scan stream subscribers",
		},
		{
			name:   "match stream",
			target: "/api/v1/match/stream",
			subs: func(t *testing.T, server *Server) {
				for i := 0; i < max_subscribers; i++ {
					_, unsubscribe, err := server.matches.Subscribe()
					if err != nil {
						t.Fatalf("subscribe %d: %v", i, err)
					}
					t.Cleanup(unsubscribe)
				}
			},
			message: "too many match stream subscribers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := new_test_server(t)
			tc.subs(t, server)
			recorder := do_request(t, server, http.MethodGet, tc.target, "")
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
			}
			body := decode[struct {
				Error string `json:"error"`
			}](t, recorder)
			if body.Error != tc.message {
				t.Errorf("error = %q, want %q", body.Error, tc.message)
			}
		})
	}
}
