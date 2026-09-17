package webserver

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/cybersouris/tomovee/internal/tmdb"
)

// handle_poster serves a catalog entry's poster from the local cache, falling
// back to redirecting to TMDB when it has not been cached yet.
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
	local := filepath.Join(s.cfg.Poster_cache_dir, poster_file_name(id, entry.Poster_path))
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		http.ServeFile(w, r, local)
		return
	}
	http.Redirect(w, r, tmdb.Poster_url(entry.Poster_path, "w500"), http.StatusFound)
}

func poster_file_name(id int64, poster_path string) string {
	return itoa(id) + filepath.Ext(poster_path)
}
