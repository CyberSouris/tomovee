package webserver

import (
	"encoding/json"
	"net/http"
)

type watch_folder_item struct {
	Path      string `json:"path"`
	Enabled   bool   `json:"enabled"`
	Last_scan string `json:"last_scan,omitempty"`
}

type settings_response struct {
	Listen                   string              `json:"listen"`
	Database_path            string              `json:"database_path"`
	Poster_cache_dir         string              `json:"poster_cache_dir"`
	Scan_directories         []string            `json:"scan_directories"`
	Imdb_datasets_path       string              `json:"imdb_datasets_path,omitempty"`
	Watch_enabled            bool                `json:"watch_enabled"`
	Tmdb_configured          bool                `json:"tmdb_configured"`
	Opensubtitles_configured bool                `json:"opensubtitles_configured"`
	Watch_folders            []watch_folder_item `json:"watch_folders"`
}

type settings_update struct {
	Watch_enabled *bool `json:"watch_enabled"`
	Folders       []struct {
		Path    string `json:"path"`
		Enabled bool   `json:"enabled"`
	} `json:"folders"`
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
	for _, folder := range update.Folders {
		if folder.Path == "" {
			continue
		}
		if err := s.store.Set_watch_folder(r.Context(), folder.Path, folder.Enabled); err != nil {
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

func (s *Server) settings(r *http.Request) (settings_response, error) {
	scan_directories := s.cfg.Scan_directories
	if scan_directories == nil {
		scan_directories = []string{}
	}
	response := settings_response{
		Listen:                   s.cfg.Listen,
		Database_path:            s.cfg.Database_path,
		Poster_cache_dir:         s.cfg.Poster_cache_dir,
		Scan_directories:         scan_directories,
		Imdb_datasets_path:       s.cfg.Imdb_datasets_path,
		Watch_enabled:            s.cfg.Watch_enabled,
		Tmdb_configured:          s.cfg.Api.Tmdb_key != "",
		Opensubtitles_configured: s.cfg.Api.Opensubtitles_api_key != "",
		Watch_folders:            []watch_folder_item{},
	}
	if value, ok, err := s.store.Config_get(r.Context(), "watch_enabled"); err != nil {
		return response, err
	} else if ok {
		response.Watch_enabled = value == "true"
	}
	folders, err := s.store.List_watch_folders(r.Context())
	if err != nil {
		return response, err
	}
	for _, folder := range folders {
		response.Watch_folders = append(response.Watch_folders, watch_folder_item{
			Path: folder.Path, Enabled: folder.Enabled, Last_scan: folder.Last_scan,
		})
	}
	return response, nil
}
