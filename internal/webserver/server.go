// Package webserver exposes the Tomovee REST API and serves the embedded
// single-page web UI.
package webserver

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/poster_cache"
	"github.com/cybersouris/tomovee/internal/scan"
)

// Options configures a Server.
type Options struct {
	Store    *database.Store
	Config   *config.Config
	Matcher  *matcher.Matcher
	Metadata matcher.Metadata_source
	Scanner  *scan.Scanner
	// Matching, when set, backs the background matching job and its endpoints.
	Matching *matching.Matching
	Posters  *poster_cache.Cache
	// Datasets reports the offline IMDb index build state, when configured.
	// Nil tracks nothing.
	Datasets *imdb_datasets.Tracker
	// Stream_datasets, when set, streams the IMDb datasets over the network
	// straight into a new index at index_db_path (decompressing on the fly,
	// never saving the originals to disk) and reports progress through the
	// Datasets tracker. cmd_serve wires this to its pipeline so the Settings
	// page's import button can build the index at runtime. When nil, POST
	// /api/v1/datasets reports the feature as unavailable.
	Stream_datasets func(index_db_path string)
	// Reload, when set, re-applies the persisted source settings (API keys)
	// to the live matching pipeline so they take effect immediately instead of
	// on the next restart. When nil, POST /api/v1/settings/reload reports the
	// feature as unavailable.
	Reload func() error
	Static fs.FS
	Logger *slog.Logger
}

// Server holds the HTTP handlers and background job state.
type Server struct {
	store    *database.Store
	cfg      *config.Config
	matcher  *matcher.Matcher
	metadata matcher.Metadata_source
	scanner  *scan.Scanner
	matching *matching.Matching
	posters  *poster_cache.Cache
	datasets *imdb_datasets.Tracker
	// stream_datasets mirrors the Options field; see there.
	stream_datasets func(index_db_path string)
	// reload mirrors the Options field; see there.
	reload  func() error
	static  fs.FS
	logger  *slog.Logger
	mux     *http.ServeMux
	jobs    *job_manager[scan.Progress, scan.Result]
	matches *job_manager[matching.Progress, matching.Result]
}

// New builds a Server and registers its routes.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:           opts.Store,
		cfg:             opts.Config,
		matcher:         opts.Matcher,
		metadata:        opts.Metadata,
		scanner:         opts.Scanner,
		matching:        opts.Matching,
		posters:         opts.Posters,
		datasets:        opts.Datasets,
		stream_datasets: opts.Stream_datasets,
		reload:          opts.Reload,
		static:          opts.Static,
		logger:          logger,
		mux:             http.NewServeMux(),
		jobs:            new_job_manager[scan.Progress, scan.Result](logger),
		matches:         new_job_manager[matching.Progress, matching.Result](logger),
	}
	s.routes()
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/catalog", s.handle_catalog_list)
	s.mux.HandleFunc("GET /api/v1/categories", s.handle_categories)
	s.mux.HandleFunc("GET /api/v1/catalog/{id}", s.handle_catalog_detail)
	s.mux.HandleFunc("POST /api/v1/catalog/{id}/match", s.handle_manual_match)
	s.mux.HandleFunc("POST /api/v1/catalog/{id}/rematch", s.handle_rematch)
	s.mux.HandleFunc("POST /api/v1/catalog/{id}/reclassify", s.handle_reclassify)
	s.mux.HandleFunc("GET /api/v1/unmatched", s.handle_unmatched)
	s.mux.HandleFunc("GET /api/v1/background", s.handle_background_status)
	s.mux.HandleFunc("GET /api/v1/search", s.handle_search)
	s.mux.HandleFunc("POST /api/v1/scan", s.handle_scan_start)
	s.mux.HandleFunc("GET /api/v1/scan/status", s.handle_scan_status)
	s.mux.HandleFunc("GET /api/v1/scan/stream", s.handle_scan_stream)
	s.mux.HandleFunc("POST /api/v1/match", s.handle_match_start)
	s.mux.HandleFunc("GET /api/v1/match/status", s.handle_match_status)
	s.mux.HandleFunc("GET /api/v1/match/stream", s.handle_match_stream)
	s.mux.HandleFunc("GET /api/v1/settings", s.handle_settings_get)
	s.mux.HandleFunc("PUT /api/v1/settings", s.handle_settings_put)
	s.mux.HandleFunc("GET /api/v1/libraries", s.handle_libraries_list)
	s.mux.HandleFunc("GET /api/v1/path-complete", s.handle_path_complete)
	s.mux.HandleFunc("DELETE /api/v1/libraries/{name}", s.handle_library_delete)
	s.mux.HandleFunc("POST /api/v1/datasets", s.handle_datasets_start)
	s.mux.HandleFunc("GET /api/v1/posters/{id}", s.handle_poster)
	s.mux.HandleFunc("POST /api/v1/posters/prune", s.handle_poster_prune)
	s.mux.HandleFunc("GET /api/v1/versions/{version_id}/file", s.handle_version_file)
	s.mux.HandleFunc("POST /api/v1/settings/reload", s.handle_sources_reload)
	s.mux.HandleFunc("/", s.handle_spa)
}

func (s *Server) handle_spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		write_error(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if data, err := fs.ReadFile(s.static, name); err == nil {
		if content_type := content_type_for(name); content_type != "" {
			w.Header().Set("Content-Type", content_type)
		}
		_, _ = w.Write(data)
		return
	}
	index, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		write_error(w, http.StatusNotFound, "web ui not available")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

func content_type_for(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".ico"):
		return "image/x-icon"
	}
	return ""
}

func write_json(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func write_error(w http.ResponseWriter, status int, message string) {
	write_json(w, status, map[string]string{"error": message})
}

func path_id(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func int_query(r *http.Request, key string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return 0
	}
	return value
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
