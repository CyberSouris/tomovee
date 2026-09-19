package webserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
)

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
