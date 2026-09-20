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
	ffmpeg "github.com/krau/ffmpeg-go"
)

func init() {
	// ffmpeg-go logs every compiled command to stderr by default; the streaming
	// endpoint calls it per request, so silence that noise once at startup.
	ffmpeg.LogCompiledCommand = false
}

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
	if s.cfg != nil && s.cfg.Stream.Transcode == "container" && browser_unplayable_container(path) {
		if dst, err := remux_to_mp4(r.Context(), path); err == nil {
			defer os.Remove(dst)
			s.logger.Debug("serving container-remuxed media", "src", path, "dst", dst)
			http.ServeFile(w, r, dst)
			return
		} else {
			// ffmpeg absence or a remux failure falls back to the raw file.
			s.logger.Debug("container remux failed; serving original", "src", path, "error", err)
		}
	}
	http.ServeFile(w, r, path)
}

// browser_unplayable_container reports whether path is in a container browsers
// cannot demux natively, so stream.transcode=container should remux it first.
// MP4 and its close relatives (M4V/MOV) are already browser-friendly.
func browser_unplayable_container(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
		return false
	default:
		return true
	}
}

// remux_to_mp4 rewraps src in an MP4 container, copying the streams without
// re-encoding, and returns the path of the resulting temporary file. The caller
// owns the temp file and must remove it when done.
func remux_to_mp4(ctx context.Context, src string) (string, error) {
	file, err := os.CreateTemp("", "tomovee-remux-*.mp4")
	if err != nil {
		return "", err
	}
	dst := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	err = ffmpeg.Input(src).
		Output(dst, ffmpeg.KwArgs{
			"map":      "0",
			"c":        "copy",
			"movflags": "+faststart",
		}).
		OverWriteOutput().
		Run()
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
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
