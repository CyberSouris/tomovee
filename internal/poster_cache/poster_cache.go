// Package poster_cache downloads poster images from TMDB and stores them on
// disk so the web UI can serve them locally.
package poster_cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Err_no_poster is returned by Ensure when an entry has no poster path.
var Err_no_poster = errors.New("poster_cache: entry has no poster path")

const (
	default_base_url     = "https://image.tmdb.org/t/p"
	default_size         = "w500"
	max_poster_bytes     = 10 << 20
	default_http_timeout = 30 * time.Second
)

// Options configures a Cache.
type Options struct {
	Dir        string
	Base_url   string
	Size       string
	User_agent string
	Http       *http.Client
	Logger     *slog.Logger
}

// Cache is a directory of poster images named "<catalog_entry_id>.<ext>".
type Cache struct {
	dir        string
	base_url   string
	size       string
	user_agent string
	http       *http.Client
	logger     *slog.Logger
}

// New builds a Cache from opts.
func New(opts Options) *Cache {
	base_url := strings.TrimRight(opts.Base_url, "/")
	if base_url == "" {
		base_url = default_base_url
	}
	size := opts.Size
	if size == "" {
		size = default_size
	}
	http_client := opts.Http
	if http_client == nil {
		http_client = &http.Client{Timeout: default_http_timeout}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Cache{
		dir:        opts.Dir,
		base_url:   base_url,
		size:       size,
		user_agent: opts.User_agent,
		http:       http_client,
		logger:     logger,
	}
}

// Local_path returns the on-disk path a poster would occupy, whether or not it
// has been downloaded yet.
func (c *Cache) Local_path(entry_id int64, poster_path string) string {
	return filepath.Join(c.dir, strconv.FormatInt(entry_id, 10)+poster_ext(poster_path))
}

// Ensure returns the local path of the poster, downloading it first if needed.
func (c *Cache) Ensure(ctx context.Context, entry_id int64, poster_path string) (string, error) {
	if poster_path == "" {
		return "", Err_no_poster
	}
	local := c.Local_path(entry_id, poster_path)
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, nil
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", err
	}
	target := c.base_url + "/" + c.size + poster_path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	if c.user_agent != "" {
		request.Header.Set("User-Agent", c.user_agent)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("poster_cache: %s: %s", target, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, max_poster_bytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > max_poster_bytes {
		return "", fmt.Errorf("poster_cache: %s: image exceeds %d bytes", target, max_poster_bytes)
	}
	tmp, err := os.CreateTemp(c.dir, "poster-*.tmp")
	if err != nil {
		return "", err
	}
	tmp_name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp_name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp_name)
		return "", err
	}
	if err := os.Rename(tmp_name, local); err != nil {
		os.Remove(tmp_name)
		return "", err
	}
	c.logger.Debug("poster cached", "entry", entry_id, "path", local)
	return local, nil
}

// Prune deletes cached posters whose catalog entry id is not in referenced.
// Files it does not recognize as posters (e.g. temporary downloads) are left
// alone. It returns the number of files removed.
func (c *Cache) Prune(referenced map[int64]bool) (int, error) {
	dir_entries, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range dir_entries {
		if entry.IsDir() {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		id, err := strconv.ParseInt(stem, 10, 64)
		if err != nil {
			continue
		}
		if referenced[id] {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func poster_ext(poster_path string) string {
	ext := filepath.Ext(poster_path)
	if ext == "" {
		return ".jpg"
	}
	return ext
}
