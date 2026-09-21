package webserver

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_handle_path_complete_lists_matching_directories(t *testing.T) {
	server, _ := new_test_server(t)
	root := t.TempDir()
	sub := filepath.Join(root, "Movies")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	response := do_request(t, server, "GET",
		"/api/v1/path-complete?q="+sub, "")
	out := decode[struct {
		Paths []string `json:"paths"`
	}](t, response)

	if len(out.Paths) == 0 {
		t.Fatalf("no suggestions returned, got %+v", out.Paths)
	}
	found := false
	for _, path := range out.Paths {
		if path == sub {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q among suggestions, got %+v", sub, out.Paths)
	}
}