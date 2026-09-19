package webserver

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/database"
)

// handle_version_file serves a version's media file so a browser can stream it
// in a <video> element or download it. http.ServeFile handles Range requests
// (seeking), HEAD, and If-Modified-Since. The file is resolved against its
// library root, so a request can never escape the configured libraries.
func (s *Server) handle_version_file(w http.ResponseWriter, r *http.Request) {
	version_id, err := strconv.ParseInt(r.PathValue("version_id"), 10, 64)
	if err != nil || version_id <= 0 {
		write_error(w, http.StatusBadRequest, "invalid version id")
		return
	}
	version, err := s.store.Get_version(r.Context(), version_id)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if version == nil {
		write_error(w, http.StatusNotFound, "version not found")
		return
	}
	path, err := s.version_file_path(r.Context(), *version)
	if err != nil {
		write_error(w, http.StatusNotFound, err.Error())
		return
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		write_error(w, http.StatusNotFound, "media file not found")
		return
	}
	http.ServeFile(w, r, path)
}

// version_file_path resolves the cleaned, safe absolute path of a version's
// media file. Library-scoped versions are confined to their library's root;
// legacy absolute paths (rows scanned before libraries existed) are served
// directly.
func (s *Server) version_file_path(ctx context.Context, version database.Version) (string, error) {
	if version.Library_id > 0 {
		library, err := s.store.Get_library(ctx, version.Library_id)
		if err != nil {
			return "", err
		}
		if library == nil {
			return "", errors.New("media library not found")
		}
		path, ok := path_within_root(library.Path, version.File_path)
		if !ok {
			return "", errors.New("media file outside library root")
		}
		return path, nil
	}
	if !filepath.IsAbs(version.File_path) {
		return "", errors.New("media file has no library")
	}
	return filepath.Clean(version.File_path), nil
}

// path_within_root returns the cleaned join of root and relative when the
// result stays inside root, otherwise false.
func path_within_root(root, relative string) (string, bool) {
	path := filepath.Clean(filepath.Join(root, relative))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return path, true
}
