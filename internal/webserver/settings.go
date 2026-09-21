package webserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cybersouris/tomovee/internal/config"
	"github.com/cybersouris/tomovee/internal/imdb_datasets"
)

type library_item struct {
	Id        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Enabled   bool   `json:"enabled"`
	Last_scan string `json:"last_scan,omitempty"`
}

type settings_response struct {
	Listen             string `json:"listen"`
	Database_path      string `json:"database_path"`
	Poster_cache_dir   string `json:"poster_cache_dir"`
	Imdb_datasets_path string `json:"imdb_datasets_path,omitempty"`
	Watch_enabled      bool   `json:"watch_enabled"`
	// Tmdb_key and the OpenSubtitles fields are the effective values (config
	// file merged with any persisted override) so the Settings form can prefill
	// and edit them.
	Tmdb_key                 string                `json:"tmdb_key,omitempty"`
	Opensubtitles_api_key    string                `json:"opensubtitles_api_key,omitempty"`
	Opensubtitles_username   string                `json:"opensubtitles_username,omitempty"`
	Opensubtitles_password   string                `json:"opensubtitles_password,omitempty"`
	Tmdb_configured          bool                  `json:"tmdb_configured"`
	Opensubtitles_configured bool                  `json:"opensubtitles_configured"`
	Matching_ready           bool                  `json:"matching_ready"`
	Datasets                 *datasets_status_json `json:"datasets,omitempty"`
	Libraries                []library_item        `json:"libraries"`
}

// datasets_status_json is the offline index build status sent to the UI.
type datasets_status_json struct {
	State    string `json:"state"`
	Percent  int    `json:"percent"`
	Dataset  string `json:"dataset,omitempty"`
	Step     string `json:"step,omitempty"`
	Message  string `json:"message,omitempty"`
	Has_path bool   `json:"has_path"`
}

type settings_update struct {
	Watch_enabled          *bool   `json:"watch_enabled"`
	Tmdb_key               *string `json:"tmdb_key"`
	Opensubtitles_api_key  *string `json:"opensubtitles_api_key"`
	Opensubtitles_username *string `json:"opensubtitles_username"`
	Opensubtitles_password *string `json:"opensubtitles_password"`
	Imdb_datasets_path     *string `json:"imdb_datasets_path"`
	Libraries              []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Enabled bool   `json:"enabled"`
	} `json:"libraries"`
}

// handle_library_delete removes the named library from the settings list
// along with the version rows that lived under its root. Catalog entries are
// global, so an entry is dropped only when the removed library was its last
// holder. It returns the refreshed settings so the Settings page re-renders
// the shortened library list immediately.
func (s *Server) handle_library_delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		write_error(w, http.StatusBadRequest, "invalid library name")
		return
	}
	if err := s.store.Delete_library(r.Context(), name); err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := s.settings(r)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, response)
}

func (s *Server) handle_settings_get(w http.ResponseWriter, r *http.Request) {
	response, err := s.settings(r)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, response)
}

func (s *Server) handle_settings_put(w http.ResponseWriter, r *http.Request) {
	var update settings_update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		write_error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if update.Watch_enabled != nil {
		value := "false"
		if *update.Watch_enabled {
			value = "true"
		}
		if err := s.store.Config_set(r.Context(), "watch_enabled", value); err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Persist the editable source settings as overrides and mirror them onto
	// the live config so the status endpoints reflect the saved values.
	overrides := []struct {
		key   string
		value *string
		dst   *string
	}{
		{config.Override_tmdb_key, update.Tmdb_key, &s.cfg.Api.Tmdb_key},
		{config.Override_opensubtitles_api_key, update.Opensubtitles_api_key, &s.cfg.Api.Opensubtitles_api_key},
		{config.Override_opensubtitles_username, update.Opensubtitles_username, &s.cfg.Api.Opensubtitles_username},
		{config.Override_opensubtitles_password, update.Opensubtitles_password, &s.cfg.Api.Opensubtitles_password},
		{config.Override_imdb_datasets_path, update.Imdb_datasets_path, &s.cfg.Imdb_datasets_path},
	}
	for _, item := range overrides {
		if item.value == nil {
			continue
		}
		if err := s.store.Config_set(r.Context(), item.key, *item.value); err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
		*item.dst = *item.value
	}
	for _, library := range update.Libraries {
		if library.Name == "" {
			continue
		}
		if err := s.store.Upsert_library(r.Context(), library.Name, library.Path, library.Enabled); err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	response, err := s.settings(r)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, response)
}

// handle_sources_reload re-applies the persisted source settings (TMDB and
// OpenSubtitles keys saved on the Settings page) to the live matching pipeline,
// so they take effect immediately without restarting the server, then returns
// the refreshed settings.
func (s *Server) handle_sources_reload(w http.ResponseWriter, r *http.Request) {
	if s.reload == nil {
		write_error(w, http.StatusServiceUnavailable, "source reload is not configured")
		return
	}
	if err := s.reload(); err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := s.settings(r)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, response)
}

func (s *Server) settings(r *http.Request) (settings_response, error) {
	tmdb_key, err := s.settings_value(r.Context(), config.Override_tmdb_key, s.cfg.Api.Tmdb_key)
	if err != nil {
		return settings_response{}, err
	}
	os_key, err := s.settings_value(r.Context(), config.Override_opensubtitles_api_key, s.cfg.Api.Opensubtitles_api_key)
	if err != nil {
		return settings_response{}, err
	}
	os_user, err := s.settings_value(r.Context(), config.Override_opensubtitles_username, s.cfg.Api.Opensubtitles_username)
	if err != nil {
		return settings_response{}, err
	}
	os_pass, err := s.settings_value(r.Context(), config.Override_opensubtitles_password, s.cfg.Api.Opensubtitles_password)
	if err != nil {
		return settings_response{}, err
	}
	datasets_path, err := s.settings_value(r.Context(), config.Override_imdb_datasets_path, s.cfg.Imdb_datasets_path)
	if err != nil {
		return settings_response{}, err
	}
	response := settings_response{
		Listen:                   s.cfg.Listen,
		Database_path:            s.cfg.Database_path,
		Poster_cache_dir:         s.cfg.Poster_cache_dir,
		Imdb_datasets_path:       datasets_path,
		Watch_enabled:            s.cfg.Watch_enabled,
		Tmdb_key:                 tmdb_key,
		Opensubtitles_api_key:    os_key,
		Opensubtitles_username:   os_user,
		Opensubtitles_password:   os_pass,
		Tmdb_configured:          tmdb_key != "",
		Opensubtitles_configured: os_key != "",
		Matching_ready:           s.matching != nil && s.matcher != nil && s.matcher.Has_sources(),
		Libraries:                []library_item{},
	}
	if s.datasets != nil {
		status := s.datasets.Snapshot()
		response.Datasets = &datasets_status_json{
			State:    string(status.State),
			Percent:  status.Percent,
			Dataset:  status.Dataset,
			Step:     string(status.Step),
			Message:  status.Message,
			Has_path: status.Has_path,
		}
	}
	if value, ok, err := s.store.Config_get(r.Context(), "watch_enabled"); err != nil {
		return response, err
	} else if ok {
		response.Watch_enabled = value == "true"
	}
	libraries, err := s.store.List_libraries(r.Context())
	if err != nil {
		return response, err
	}
	for _, library := range libraries {
		response.Libraries = append(response.Libraries, library_item{
			Id: library.Id, Name: library.Name, Path: library.Path,
			Enabled: library.Enabled, Last_scan: library.Last_scan,
		})
	}
	return response, nil
}

// settings_value returns the persisted override for key when present, falling
// back to the config file value.
func (s *Server) settings_value(ctx context.Context, key, fallback string) (string, error) {
	if value, ok, err := s.store.Config_get(ctx, key); err != nil {
		return "", err
	} else if ok {
		return value, nil
	}
	return fallback, nil
}

// datasets_start_request optionally names the directory to download into.
type datasets_start_request struct {
	Path string `json:"path"`
}

// handle_datasets_start downloads the IMDb datasets and imports them straight
// into a new index, streaming each export and decompressing it on the fly, all
// in the background with progress reported through the datasets tracker. The
// originals are never written to disk. The chosen data directory is persisted
// as the imdb_datasets_path override so the resulting index is reused across
// restarts. The request returns as soon as the import begins.
func (s *Server) handle_datasets_start(w http.ResponseWriter, r *http.Request) {
	if s.stream_datasets == nil {
		write_error(w, http.StatusNotFound, "imdb datasets are not configured")
		return
	}
	if s.datasets != nil && s.datasets.Snapshot().State == imdb_datasets.State_building {
		write_error(w, http.StatusConflict, "an imdb datasets import is already running")
		return
	}
	var body datasets_start_request
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		write_error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	dir := strings.TrimSpace(body.Path)
	if dir == "" {
		if value, ok, err := s.store.Config_get(r.Context(), config.Override_imdb_datasets_path); err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		} else if ok && value != "" {
			dir = value
		} else {
			dir = s.cfg.Imdb_datasets_path
		}
	}
	if dir == "" {
		dir = filepath.Join(filepath.Dir(s.cfg.Database_path), "imdb_datasets")
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if err := s.store.Config_set(r.Context(), config.Override_imdb_datasets_path, dir); err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cfg.Imdb_datasets_path = dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	stream := s.stream_datasets
	index_db_path := imdb_datasets.Index_db_path(dir)
	go func() {
		stream(index_db_path)
	}()
	write_json(w, http.StatusAccepted, map[string]any{"status": "started", "path": dir})
}
