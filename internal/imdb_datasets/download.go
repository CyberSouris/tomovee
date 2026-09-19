package imdb_datasets

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Datasets_base_url is where the IMDb non-commercial exports are published.
// The server can download them from here and import them for offline matching.
// Using the data requires displaying the attribution shown in the Settings UI:
// "Information courtesy of IMDb (https://www.imdb.com). Used with permission."
const Datasets_base_url = "https://datasets.imdbws.com"

// dataset_stems are the exports downloaded and imported, in build order.
// title.akas is included so alternate titles contribute to matching.
var dataset_stems = []string{"title.basics", "title.akas", "title.episode", "title.ratings"}

// download_progress_interval throttles progress reports while streaming a
// dataset body.
const download_progress_interval = 2_000_000

// Download fetches the IMDb datasets into dir (created if missing) and returns
// the written paths. Each dataset is streamed to "<stem>.tsv.gz.part" and
// atomically renamed when complete, so a failed download never leaves a partial
// dataset that would be imported later. on_progress, when non-nil, receives
// Build_download events reporting cumulative bytes against the total size of
// every download, measured up front with HEAD requests.
func Download(ctx context.Context, dir string, on_progress func(Build_progress)) ([]string, error) {
	return download_from(ctx, Datasets_base_url, dir, on_progress)
}

// download_from is Download with a configurable base URL for tests.
func download_from(ctx context.Context, base string, dir string, on_progress func(Build_progress)) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("imdb_datasets: create download directory: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	sizes, err := dataset_sizes(client, base)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, size := range sizes {
		total += size
	}
	report := func(dataset string, bytes int64) {
		if on_progress != nil {
			on_progress(Build_progress{
				Step: Build_download, Dataset: dataset, Bytes: bytes, Total: total,
			})
		}
	}
	var written []string
	var accumulated int64
	var last_report int64
	for _, stem := range dataset_stems {
		report(stem+".tsv.gz", accumulated)
		dst, err := download_file(client, ctx, base, stem, dir, func(n int64) {
			accumulated += n
			if accumulated-last_report >= download_progress_interval {
				last_report = accumulated
				report(stem+".tsv.gz", accumulated)
			}
		})
		if err != nil {
			return written, err
		}
		report(stem+".tsv.gz", accumulated)
		written = append(written, dst)
	}
	if on_progress != nil {
		on_progress(Build_progress{Step: Build_download, Bytes: accumulated, Total: total, Done: true})
	}
	return written, nil
}

// dataset_sizes returns each dataset's advertised content length via a HEAD
// request, so download progress can be reported against one overall total.
func dataset_sizes(client *http.Client, base string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(dataset_stems))
	for _, stem := range dataset_stems {
		url := base + "/" + stem + ".tsv.gz"
		resp, err := client.Head(url)
		if err != nil {
			return nil, fmt.Errorf("imdb_datasets: resolve %s: %w", stem, err)
		}
		size := resp.ContentLength
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("imdb_datasets: %s not available (HTTP %d)", stem, status)
		}
		if size < 0 {
			size = 0
		}
		sizes[stem] = size
	}
	return sizes, nil
}

// download_file streams one dataset into dir as "<stem>.tsv.gz.part" and
// renames it to "<stem>.tsv.gz" on success, removing the partial file on any
// error. on_bytes, when non-nil, receives the number of body bytes written.
func download_file(client *http.Client, ctx context.Context, base, stem, dir string, on_bytes func(int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+stem+".tsv.gz", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imdb_datasets: download %s: %w", stem, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imdb_datasets: download %s: HTTP %d", stem, resp.StatusCode)
	}
	dst := filepath.Join(dir, stem+".tsv.gz")
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", fmt.Errorf("imdb_datasets: create %s: %w", part, err)
	}
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(part)
				return "", fmt.Errorf("imdb_datasets: write %s: %w", part, werr)
			}
			if on_bytes != nil {
				on_bytes(int64(n))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(part)
			return "", fmt.Errorf("imdb_datasets: read %s: %w", stem, rerr)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("imdb_datasets: close %s: %w", part, err)
	}
	if err := os.Rename(part, dst); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("imdb_datasets: finalize %s: %w", dst, err)
	}
	return dst, nil
}
