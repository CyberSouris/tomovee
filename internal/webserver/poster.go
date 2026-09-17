package webserver

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cybersouris/tomovee/internal/poster_cache"
	"github.com/cybersouris/tomovee/internal/tmdb"
)

// handle_poster serves a catalog entry's poster from the local cache,
// downloading it on demand and falling back to a TMDB redirect.
func (s *Server) handle_poster(w http.ResponseWriter, r *http.Request) {
	id, ok := path_id(r)
	if !ok {
		write_error(w, http.StatusBadRequest, "invalid catalog id")
		return
	}
	entry, err := s.store.Get_catalog_entry(r.Context(), id)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil || entry.Poster_path == "" {
		write_error(w, http.StatusNotFound, "no poster available")
		return
	}

	if s.posters != nil {
		local, err := s.posters.Ensure(r.Context(), id, entry.Poster_path)
		if err == nil {
			http.ServeFile(w, r, local)
			return
		}
		if !errors.Is(err, poster_cache.Err_no_poster) {
			s.logger.Warn("poster download failed", "entry", id, "error", err)
		}
	} else {
		local := filepath.Join(s.cfg.Poster_cache_dir, poster_file_name(id, entry.Poster_path))
		if info, err := os.Stat(local); err == nil && !info.IsDir() {
			http.ServeFile(w, r, local)
			return
		}
	}
	http.Redirect(w, r, tmdb.Poster_url(entry.Poster_path, "w500"), http.StatusFound)
}

func (s *Server) handle_poster_prune(w http.ResponseWriter, r *http.Request) {
	if s.posters == nil {
		write_error(w, http.StatusServiceUnavailable, "poster cache is not configured")
		return
	}
	referenced, err := s.store.List_poster_refs(r.Context())
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	removed, err := s.posters.Prune(referenced)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, map[string]int{"removed": removed})
}

func poster_file_name(id int64, poster_path string) string {
	return itoa(id) + filepath.Ext(poster_path)
}
