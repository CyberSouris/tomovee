package webserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
)

// skip_without_ffmpeg skips a test when the ffmpeg binary is not installed, so
// the container-remux integration paths degrade gracefully in minimal
// environments instead of failing.
func skip_without_ffmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg not installed; skipping integration test")
	}
}

// make_mkv writes a tiny real MKV (video + audio) into dir using ffmpeg, and
// returns its path. Used as a realistic source for the container-remux path.
func make_mkv(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24", "-t", "1",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", "1",
		"-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg build fixture: %v (%s)", err, out)
	}
	return path
}

const media_bytes = "fakemoviedatacontentforstreamingtest"

func stream_fixture(t *testing.T) (*Server, int64) {
	t.Helper()
	server, store := new_test_server(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Movie.mkv"), []byte(media_bytes), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := store.Upsert_library(context.Background(), "movies", root, false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	libraries, err := store.List_libraries(context.Background())
	if err != nil || len(libraries) != 1 {
		t.Fatalf("list libraries: %v (%d)", err, len(libraries))
	}
	entry_id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "movie", Title: "The Matrix", Release_year: 1999,
		Imdb_id: "tt0133093", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	version_id, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: entry_id, Library_id: libraries[0].Id,
		File_path: "Movie.mkv", Container: "matroska", Size_bytes: int64(len(media_bytes)),
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	return server, version_id
}

func media_request(server *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func Test_version_file_streams_with_range_support(t *testing.T) {
	server, version_id := stream_fixture(t)
	target := fmt.Sprintf("/api/v1/versions/%d/file", version_id)

	full := media_request(server, http.MethodGet, target, nil)
	if full.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", full.Code, full.Body)
	}
	if full.Body.String() != media_bytes {
		t.Errorf("body = %q, want %q", full.Body.String(), media_bytes)
	}
	if full.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("missing Accept-Ranges header")
	}

	partial := media_request(server, http.MethodGet, target, map[string]string{"Range": "bytes=4-9"})
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d: %s", partial.Code, partial.Body)
	}
	if partial.Body.String() != media_bytes[4:10] {
		t.Errorf("range body = %q, want %q", partial.Body.String(), media_bytes[4:10])
	}
	if want := fmt.Sprintf("bytes %d-%d/%d", 4, 9, len(media_bytes)); partial.Header().Get("Content-Range") != want {
		t.Errorf("Content-Range = %q, want %q", partial.Header().Get("Content-Range"), want)
	}
}

func Test_version_detail_includes_file_url(t *testing.T) {
	server, version_id := stream_fixture(t)
	detail := decode[detail_response](t, media_request(server, http.MethodGet, "/api/v1/catalog/1", nil))
	if len(detail.Versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(detail.Versions))
	}
	want := fmt.Sprintf("/api/v1/versions/%d/file", version_id)
	if detail.Versions[0].File_url != want {
		t.Errorf("file_url = %q, want %q", detail.Versions[0].File_url, want)
	}
}

func Test_version_file_unknown_or_missing(t *testing.T) {
	server, _ := stream_fixture(t)
	missing := media_request(server, http.MethodGet, "/api/v1/versions/9999/file", nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", missing.Code)
	}

	// A version row whose media file vanished from disk is also a 404.
	server2, store := new_test_server(t)
	if err := store.Upsert_library(context.Background(), "movies", t.TempDir(), false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	entry_id, _ := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "movie", Title: "Gone", Status: "matched",
	})
	libraries, _ := store.List_libraries(context.Background())
	if len(libraries) == 0 {
		t.Fatal("no library available")
	}
	version_id, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: entry_id, Library_id: libraries[0].Id,
		File_path: "missing.mkv",
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	gone := media_request(server2, http.MethodGet, fmt.Sprintf("/api/v1/versions/%d/file", version_id), nil)
	if gone.Code != http.StatusNotFound {
		t.Errorf("gone file status = %d, want 404", gone.Code)
	}
}

func Test_version_file_confined_to_library_root(t *testing.T) {
	server, store := new_test_server(t)
	root := t.TempDir()
	if err := store.Upsert_library(context.Background(), "movies", root, false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	libraries, err := store.List_libraries(context.Background())
	if err != nil || len(libraries) != 1 {
		t.Fatalf("list libraries: %v (%d)", err, len(libraries))
	}
	entry_id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "movie", Title: "Sneaky", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	for _, path := range []string{"../../etc/passwd", "/etc/passwd", "..", ""} {
		version_id, err := store.Save_version(context.Background(), database.Version{
			Catalog_entry_id: entry_id, Library_id: libraries[0].Id,
			File_path: path, Container: "_",
		})
		if err != nil {
			t.Fatalf("save version %q: %v", path, err)
		}
		resp := media_request(server, http.MethodGet, fmt.Sprintf("/api/v1/versions/%d/file", version_id), nil)
		if resp.Code != http.StatusNotFound {
			t.Errorf("file_path %q status = %d, want 404: %s", path, resp.Code, resp.Body)
		}
	}
}

func Test_browser_unplayable_container(t *testing.T) {
	for _, ext := range []string{".mkv", ".avi", ".webm", ".ts", ".flv", ""} {
		if !browser_unplayable_container("movie" + ext) {
			t.Errorf("extension %q should be considered browser-unplayable", ext)
		}
	}
	for _, ext := range []string{".mp4", ".m4v", ".mov"} {
		if browser_unplayable_container("movie" + ext) {
			t.Errorf("extension %q should be left untouched", ext)
		}
	}
}

// container_version_server returns a server whose stream config is set to
// container-remux, with a version pointing at media_file inside a library.
func container_version_server(t *testing.T, media_file string) (*Server, int64) {
	t.Helper()
	server, store := new_test_server(t)
	server.cfg.Stream.Transcode = "container"
	root := filepath.Dir(media_file)
	if err := store.Upsert_library(context.Background(), "movies", root, false); err != nil {
		t.Fatalf("register library: %v", err)
	}
	libraries, err := store.List_libraries(context.Background())
	if err != nil || len(libraries) != 1 {
		t.Fatalf("list libraries: %v (%d)", err, len(libraries))
	}
	entry_id, err := store.Upsert_catalog_entry(context.Background(), database.Catalog_entry{
		Media_type: "movie", Title: "Remuxed", Status: "matched",
	})
	if err != nil {
		t.Fatalf("upsert entry: %v", err)
	}
	info, err := os.Stat(media_file)
	if err != nil {
		t.Fatalf("stat media file: %v", err)
	}
	version_id, err := store.Save_version(context.Background(), database.Version{
		Catalog_entry_id: entry_id, Library_id: libraries[0].Id,
		File_path: filepath.Base(media_file), Container: "matroska",
		Size_bytes: info.Size(),
	})
	if err != nil {
		t.Fatalf("save version: %v", err)
	}
	return server, version_id
}

// Test_version_file_container_remux_serves_an_mp4 verifies that a non-MP4
// file served with stream.transcode=container is transparently remuxed to a
// playable MP4. The response must be an MP4 (ftyp box), not the raw MKV
// (matroska EBML signature).
func Test_version_file_container_remux_serves_an_mp4(t *testing.T) {
	skip_without_ffmpeg(t)
	media := make_mkv(t, t.TempDir())
	server, version_id := container_version_server(t, media)
	target := fmt.Sprintf("/api/v1/versions/%d/file", version_id)

	resp := media_request(server, http.MethodGet, target, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", resp.Code, resp.Body)
	}
	body := resp.Body.Bytes()
	if len(body) < 8 || !bytes.HasPrefix(body[4:8], []byte("ftyp")) {
		t.Errorf("body is not an MP4 (no ftyp box at offset 4), first bytes: %x", body[:min(16, len(body))])
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "video/mp4") {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
}

// Test_version_file_container_remux_falls_back_on_bad_input ensures a remux
// failure (unreadable media bytes) degrades to serving the original raw file
// rather than erroring the request.
func Test_version_file_container_remux_falls_back_on_bad_input(t *testing.T) {
	skip_without_ffmpeg(t)
	root := t.TempDir()
	media := filepath.Join(root, "garbage.mkv")
	if err := os.WriteFile(media, []byte("this is not a real video"), 0o644); err != nil {
		t.Fatalf("write garbage media: %v", err)
	}
	server, version_id := container_version_server(t, media)
	resp := media_request(server, http.MethodGet, fmt.Sprintf("/api/v1/versions/%d/file", version_id), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", resp.Code, resp.Body)
	}
	if resp.Body.String() != "this is not a real video" {
		t.Errorf("body = %q, want raw garbage bytes", resp.Body.String())
	}
}

// Test_version_file_mp4_not_remuxed verifies an already-browser-friendly MP4
// is served untouched even with container transcode enabled.
func Test_version_file_mp4_not_remuxed(t *testing.T) {
	skip_without_ffmpeg(t)
	root := t.TempDir()
	out := filepath.Join(root, "sample.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=24", "-t", "1",
		"-f", "lavfi", "-i", "sine=frequency=440", "-t", "1",
		"-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac",
		out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg build mp4 fixture: %v (%s)", err, output)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read mp4: %v", err)
	}
	server, version_id := container_version_server(t, out)
	resp := media_request(server, http.MethodGet, fmt.Sprintf("/api/v1/versions/%d/file", version_id), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", resp.Code, resp.Body)
	}
	if !bytes.Equal(resp.Body.Bytes(), raw) {
		t.Errorf("mp4 was modified by container transcode")
	}
}

// Test_version_file_default_no_transcode_serves_raw confirms the default
// stream.transcode (none) serves files untouched.
func Test_version_file_default_no_transcode_serves_raw(t *testing.T) {
	server, version_id := stream_fixture(t)
	resp := media_request(server, http.MethodGet, fmt.Sprintf("/api/v1/versions/%d/file", version_id), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", resp.Code, resp.Body)
	}
	if resp.Body.String() != media_bytes {
		t.Errorf("body = %q, want raw media_bytes", resp.Body.String())
	}
}
