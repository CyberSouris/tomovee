package imdb_datasets

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// gz serves a dataset body as a gzip-compressed payload with an explicit
// Content-Length so HEAD sizing works against the test server.
func gz(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("gzip payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func serve_datasets(t *testing.T, body string, with_basics bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, stem := range dataset_stems {
		if !with_basics && stem == "title.basics" {
			continue
		}
		payload := gz(t, body)
		mux.HandleFunc("/"+stem+".tsv.gz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		})
	}
	return httptest.NewServer(mux)
}

func Test_download_fetches_all_datasets_and_reports_progress(t *testing.T) {
	srv := serve_datasets(t, "const\ttype\ttitle\n", true)
	defer srv.Close()

	dir := t.TempDir()
	var events []Build_progress
	paths, err := download_from(context.Background(), srv.URL, dir, func(p Build_progress) { events = append(events, p) })
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(paths) != len(dataset_stems) {
		t.Fatalf("downloaded %d files, want %d", len(paths), len(dataset_stems))
	}
	for _, stem := range dataset_stems {
		if _, err := os.Stat(filepath.Join(dir, stem+".tsv.gz")); err != nil {
			t.Errorf("missing %s.tsv.gz: %v", stem, err)
		}
	}
	if len(events) == 0 {
		t.Fatal("no progress events reported")
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Errorf("expected a final Done event, got step=%q done=false", last.Step)
	}
	if last.Total <= 0 {
		t.Errorf("expected a positive total, got %d", last.Total)
	}
	if last.Bytes != last.Total {
		t.Errorf("cumulative bytes = %d, want total %d", last.Bytes, last.Total)
	}
}

func Test_download_missing_dataset_fails_without_partial_files(t *testing.T) {
	srv := serve_datasets(t, "const\ttype\n", false)
	defer srv.Close()

	dir := t.TempDir()
	if _, err := download_from(context.Background(), srv.URL, dir, nil); err == nil {
		t.Fatal("expected error when title.basics is missing")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("partial file %s left behind after failed download", entry.Name())
		}
	}
}

func Test_tracker_download_progress(t *testing.T) {
	tr := New_tracker()
	tr.Observe(Build_progress{Step: Build_download, Dataset: "title.basics.tsv.gz", Bytes: 100, Total: 1000})
	status := tr.Snapshot()
	if status.State != State_building {
		t.Errorf("state = %q, want building", status.State)
	}
	if status.Step != Build_download {
		t.Errorf("step = %q, want download", status.Step)
	}
	if status.Percent != 10 {
		t.Errorf("percent = %d, want 10", status.Percent)
	}
	if status.Dataset != "title.basics.tsv.gz" {
		t.Errorf("dataset = %q, want title.basics.tsv.gz", status.Dataset)
	}
	tr.Observe(Build_progress{Step: Build_download, Bytes: 1000, Total: 1000, Done: true})
	if percent := tr.Snapshot().Percent; percent != 100 {
		t.Errorf("final percent = %d, want 100", percent)
	}
}
