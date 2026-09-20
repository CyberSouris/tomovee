package imdb_datasets

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"
)

// Datasets_base_url is where the IMDb non-commercial exports are published.
// Using them requires showing the attribution used in the Settings UI:
// "Information courtesy of IMDb (https://www.imdb.com). Used with permission."
const Datasets_base_url = "https://datasets.imdbws.com"

// dataset_stems are the exports streamed and imported, in build order.
// title.akas is included so alternate titles contribute to matching.
var dataset_stems = []string{"title.basics", "title.akas", "title.episode", "title.ratings"}

// Index_db_path returns where the index database for the datasets in dir is
// kept next to the data directory.
func Index_db_path(dir string) string {
	return filepath.Join(dir, Index_db_name)
}

// Open_stream downloads the IMDb datasets from base and imports them straight
// into a new index at index_db_path, decompressing each export as it streams,
// without ever writing the original export files to disk. This is possible
// because every export is converted into the index's own structures anyway.
// Exports the server does not publish (a 404) are skipped, mirroring how
// optional on-disk exports are treated. on_progress, when non-nil, receives
// Build_stale / Build_import / Build_fts / Build_ready events; the import
// events are byte-weighted like the on-disk flow, using the compressed sizes
// measured up front with HEAD requests.
func Open_stream(ctx context.Context, index_db_path string, base string, logs *slog.Logger, on_progress func(Build_progress)) (*Index, error) {
	client := &http.Client{Timeout: 2 * time.Hour}
	stems, total := stream_manifest(client, base)
	if on_progress != nil {
		on_progress(Build_progress{Step: Build_stale, Total: total})
	}
	exports := stream_exports(client, ctx, base, stems)
	if err := build_index_db_exports(index_db_path, logs, on_progress, exports); err != nil {
		return nil, err
	}
	if on_progress != nil {
		on_progress(Build_progress{Step: Build_ready})
	}
	db, err := open_file_db(index_db_path)
	if err != nil {
		return nil, fmt.Errorf("imdb_datasets: open index: %w", err)
	}
	return &Index{db: db}, nil
}

// stream_manifest probes which datasets the server publishes and sums their
// compressed sizes, so build progress can be reported against one overall
// total. Exports the server does not publish are left out.
func stream_manifest(client *http.Client, base string) ([]string, int64) {
	var stems []string
	var total int64
	for _, stem := range dataset_stems {
		resp, err := client.Head(base + "/" + stem + ".tsv.gz")
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
			stems = append(stems, stem)
			total += resp.ContentLength
		}
		resp.Body.Close()
	}
	return stems, total
}

// stream_exports returns per-stem export sources that fetch each dataset over
// HTTP and decompress it on the fly.
func stream_exports(client *http.Client, ctx context.Context, base string, stems []string) map[string]export_source {
	exports := make(map[string]export_source, len(stems))
	for _, stem := range stems {
		stem := stem
		exports[stem] = export_source{open: func(counter *byte_counter) (io.ReadCloser, int64, error) {
			return open_stream_export(client, ctx, base, stem, counter)
		}}
	}
	return exports
}

// open_stream_export opens one dataset's compressed export over HTTP, wires the
// byte counter to the raw compressed body so its count tracks the compressed
// size, and returns a decompressed reader plus the advertised content length.
func open_stream_export(client *http.Client, ctx context.Context, base, stem string, counter *byte_counter) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+stem+".tsv.gz", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("imdb_datasets: fetch %s: %w", stem, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("imdb_datasets: fetch %s: HTTP %d", stem, resp.StatusCode)
	}
	counter.r = resp.Body
	gz, err := gzip.NewReader(counter)
	if err != nil {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("imdb_datasets: decompress %s: %w", stem, err)
	}
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	return stream_gz{gz: gz, body: resp.Body}, total, nil
}

// stream_gz yields decompressed data from an HTTP response body and closes the
// underlying connection when done.
type stream_gz struct {
	gz   *gzip.Reader
	body io.ReadCloser
}

func (r stream_gz) Read(p []byte) (int, error) { return r.gz.Read(p) }
func (r stream_gz) Close() error {
	_ = r.gz.Close()
	return r.body.Close()
}
