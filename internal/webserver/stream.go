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
		s.internal_error(w, err, "load version")
		return
	}
	if version == nil {
		write_error(w, http.StatusNotFound, "version not found")
		return
	}
	path, err := s.version_file_path(r.Context(), *version)
	if err != nil {
		s.logger.Debug("version file path resolution failed", "version_id", version_id, "error", err)
		write_error(w, http.StatusNotFound, "media file not found")
		return
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		write_error(w, http.StatusNotFound, "media file not found")
		return
	}
	// Load the version's audio tracks so the codec gate below can inspect
	// them; Get_version returns only the version row, not its track tables.
	if audio, _, err := s.store.Version_tracks(r.Context(), version_id); err != nil {
		s.logger.Debug("loading version audio tracks failed; codec gate disabled", "version_id", version_id, "error", err)
	} else {
		version.Audio = audio
	}
	if s.cfg != nil {
		switch s.cfg.Stream.Transcode {
		case "live":
			if needs_transcode(path, *version) && s.serve_live_transcode(w, r, path) {
				return
			}
		case "container":
			if browser_unplayable_container(path) || browser_unplayable_audio(version.Audio) {
				if dst, err := remux_to_mp4(r.Context(), path, version.Audio); err == nil {
					defer os.Remove(dst)
					s.logger.Debug("serving container-remuxed media", "src", path, "dst", dst)
					http.ServeFile(w, r, dst)
					return
				} else {
					// ffmpeg absence or a remux failure falls back to the raw file.
					s.logger.Debug("container remux failed; serving original", "src", path, "error", err)
				}
			}
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

// remux_to_mp4 rewraps src in an MP4 container. Browser-playable streams are
// copied without re-encoding; audio in a codec browsers cannot decode is
// re-encoded to AAC so the remuxed file actually has audible sound (a blind
// -c copy remux would preserve e.g. AC3, which browsers cannot unmute). The
// returned path is the resulting temporary file; the caller owns it and must
// remove it when done.
func remux_to_mp4(ctx context.Context, src string, audio []database.Audio_track) (string, error) {
	file, err := os.CreateTemp("", "tomovee-remux-*.mp4")
	if err != nil {
		return "", err
	}
	dst := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	args := ffmpeg.KwArgs{
		"map":      "0",
		"c:v":      "copy",
		"c:s":      "copy",
		"movflags": "+faststart",
	}
	if browser_unplayable_audio(audio) {
		args["c:a"] = "aac"
	} else {
		args["c:a"] = "copy"
	}
	err = ffmpeg.Input(src).
		Output(dst, args).
		OverWriteOutput().
		Run()
	if err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// needs_transcode reports whether a version must be transformed before it can
// play in a browser, given its scanned metadata. It is used by the "live"
// streaming mode: browser-unplayable containers OR codecs (e.g. HEVC video,
// AC3/DTS audio inside an otherwise-fine MP4) get re-encoded to H.264/AAC.
func needs_transcode(path string, version database.Version) bool {
	if browser_unplayable_container(path) {
		return true
	}
	if browser_unplayable_codecs(version.Video_codec, version.Audio) {
		return true
	}
	return false
}

// browser_unplayable_codecs reports whether a scanned version carries a video
// or audio codec browsers cannot decode natively even inside an MP4 container.
func browser_unplayable_codecs(video_codec string, audio []database.Audio_track) bool {
	switch strings.ToLower(video_codec) {
	case "", "h264", "avc1", "vp8", "vp9", "av1":
		// fine (unknown/empty treated as browser-playable)
	default:
		return true
	}
	return browser_unplayable_audio(audio)
}

// browser_unplayable_audio reports whether any audio track uses a codec browsers
// cannot decode when muxed into an MP4. AC3/EAC3/DTS/TrueHD are common in MKV
// sources but silent in the browser's <video> element, so such tracks must be
// re-encoded to AAC when remuxing.
func browser_unplayable_audio(audio []database.Audio_track) bool {
	for _, track := range audio {
		switch strings.ToLower(track.Codec) {
		case "", "aac", "mp3", "mp2", "opus", "vorbis", "flac":
			// fine
		default:
			return true
		}
	}
	return false
}

// serve_live_transcode re-encodes src to H.264/AAC and streams it to the HTTP
// response as an MP4, flushing after every write so the server behaves like a
// chunked live stream. ffmpeg is killed when the request context is cancelled
// (client disconnect). It returns true when the response was fully handled, and
// false when the caller should fall back to serving the raw file instead (e.g.
// ffmpeg absent or a failure before any bytes were streamed).
func (s *Server) serve_live_transcode(w http.ResponseWriter, r *http.Request, src string) bool {
	w.Header().Set("Content-Type", "video/mp4")
	if r.Method == http.MethodHead {
		// A HEAD must not stream a transcode; just advertise the content type.
		w.WriteHeader(http.StatusOK)
		return true
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.logger.Debug("response writer cannot flush; live transcode unsupported", "src", src)
		return false
	}
	out := &flush_writer{w: w, flusher: flusher}
	in := ffmpeg.Input(src)
	in.Context = r.Context()
	err := in.Output("pipe:", ffmpeg.KwArgs{
		"c:v":      "libx264",
		"preset":   "veryfast",
		"c:a":      "aac",
		"movflags": "+frag_keyframe+empty_moov",
		"f":        "mp4",
	}).
		WithOutput(out).
		OverWriteOutput().
		Run()
	if err != nil {
		// off the bat failure (e.g. missing ffmpeg, unreadable input) -> the
		// caller serves the raw file; log at debug since a fallback follows.
		s.logger.Debug("live transcode failed; falling back to raw file", "src", src, "error", err)
		return false
	}
	return true
}

// flush_writer writes through to the response writer and flushes after every
// write, giving the chunked/streaming behavior a living <video> needs.
type flush_writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (f *flush_writer) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		f.flusher.Flush()
	}
	return n, err
}

// version_file_path resolves the cleaned, safe absolute path of a version's
// media file. Only library-scoped versions are servable; they are confined to
// their library's root. Legacy absolute-path rows (scanned before libraries
// existed) are never served.
func (s *Server) version_file_path(ctx context.Context, version database.Version) (string, error) {
	if version.Library_id <= 0 {
		return "", errors.New("media file has no library")
	}
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
