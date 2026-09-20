package imdb_datasets

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cybersouris/tomovee/internal/scanner"
)

func gz_bytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(data)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// stream_test_server serves the small test datasets as gzipped exports with the
// same layout as https://datasets.imdbws.com.
func stream_test_server(t *testing.T) *httptest.Server {
	t.Helper()
	datasets := map[string][]byte{
		"title.basics.tsv.gz": gz_bytes(t, test_tsv),
		"title.akas.tsv.gz": gz_bytes(t,
			"titleId\tordering\ttitle\tregion\tlanguage\ttypes\tattributes\tisOriginalTitle\n"+
				"tt0133093\t1\tMatrix\tNA\t\\N\ttitle\t\tfalse\n"),
		"title.episode.tsv.gz": gz_bytes(t,
			"tconst\tparentTconst\tseasonNumber\tepisodeNumber\n"+
				"tt1586952\ttt0903747\t1\t1\n"),
	}
	mux := http.NewServeMux()
	for name, data := range datasets {
		name, data := name, data
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(data)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func Test_open_stream_builds_index_from_http(t *testing.T) {
	dir := t.TempDir()
	index_db_path := Index_db_path(dir)
	server := stream_test_server(t)

	index, err := Open_stream(context.Background(), index_db_path, server.URL, nil, nil)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer index.Close()
	if index.Count() != 7 {
		t.Errorf("count = %d, want 7", index.Count())
	}
	if got := index.Search("Matrix", 0, scanner.Movie); len(got) != 1 || got[0].Id != "tt0133093" {
		t.Errorf("aka search = %+v", got)
	}
	// The originals are streamed straight into the index and never saved.
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".gz" || entry.Name() == "title.basics" {
			t.Errorf("originals saved to disk: %s", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "title.basics.tsv.gz")); !os.IsNotExist(err) {
		t.Errorf("unexpected downloaded export on disk: %v", err)
	}
	// No partial build file may be left behind.
	if _, err := os.Stat(index_db_path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("stale temporary index on disk: %v", err)
	}
}

func Test_open_stream_reports_progress(t *testing.T) {
	dir := t.TempDir()
	server := stream_test_server(t)
	var events []Build_progress

	index, err := Open_stream(context.Background(), Index_db_path(dir), server.URL,
		nil, func(p Build_progress) { events = append(events, p) })
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	index.Close()

	steps := map[Build_step]int{}
	var stale_total int64
	for _, p := range events {
		steps[p.Step]++
		if p.Step == Build_stale {
			stale_total = p.Total
		}
	}
	if steps[Build_stale] != 1 || steps[Build_fts] != 1 || steps[Build_ready] != 1 {
		t.Errorf("event mix = %+v, want one of each of stale/fts/ready", steps)
	}
	if steps[Build_import] == 0 {
		t.Error("no import events emitted")
	}
	if stale_total <= 0 {
		t.Errorf("stale total = %d, want > 0", stale_total)
	}
	if events[len(events)-1].Step != Build_ready {
		t.Errorf("last event = %+v, want ready", events[len(events)-1])
	}
}

func Test_open_path_reuses_streamed_index(t *testing.T) {
	dir := t.TempDir()
	server := stream_test_server(t)

	index, err := Open_stream(context.Background(), Index_db_path(dir), server.URL, nil, nil)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	index.Close()

	// The data directory holds only the index (no exports); first open after a
	// rebuild against a directory with streamed originals must still produce an
	// index by reusing it instead of failing with "does not look like exports".
	index, err = Open(dir, nil)
	if err != nil {
		t.Fatalf("reopen streamed directory: %v", err)
	}
	defer index.Close()
	if index.Count() != 7 {
		t.Errorf("count = %d, want 7", index.Count())
	}
	if got := index.Search("Matrix", 0, scanner.Movie); len(got) != 1 || got[0].Id != "tt0133093" {
		t.Errorf("aka search after reuse = %+v", got)
	}
}
