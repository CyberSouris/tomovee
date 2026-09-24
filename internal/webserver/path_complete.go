package webserver

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// handle_path_complete suggests local directories that match a typed prefix,
// so the settings page can autocomplete library paths. An empty query lists
// the root; an unterminated segment filters its parent's subdirectories.
func (s *Server) handle_path_complete(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = "/"
	}
	if !strings.HasPrefix(query, "/") {
		query = "/" + query
	}

	dir := filepath.Clean(query)
	prefix := ""
	if !strings.HasSuffix(query, "/") {
		dir = filepath.Dir(dir)
		prefix = filepath.Base(filepath.Clean(query))
	}

	seen := make(map[string]bool)
	var outs []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			outs = append(outs, path)
		}
	}

	if prefix != "" {
		if info, err := os.Stat(filepath.Join(dir, prefix)); err == nil && info.IsDir() {
			add(filepath.Join(dir, prefix))
		}
	}

	if entries, err := os.ReadDir(dir); err == nil {
		var names []string
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if prefix != "" && !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			add(filepath.Join(dir, name))
			if len(outs) >= 20 {
				break
			}
		}
	}

	if outs == nil {
		outs = []string{}
	}
	write_json(w, http.StatusOK, map[string]any{"paths": outs})
}
