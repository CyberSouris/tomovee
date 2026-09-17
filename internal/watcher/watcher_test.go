package watcher

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/scan"
)

type fake_runner struct {
	calls [][]string
}

func (f *fake_runner) Run_paths(_ context.Context, directories []string, _ func(scan.Progress)) (*scan.Result, error) {
	f.calls = append(f.calls, append([]string(nil), directories...))
	return &scan.Result{}, nil
}

func new_test_store(t *testing.T) *database.Store {
	t.Helper()
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return database.New_store(d)
}

func test_watcher(store *database.Store, runner Runner, default_enabled bool) *Watcher {
	return New(store, runner, Options{
		Default_enabled: default_enabled,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func Test_scan_once_skips_when_disabled(t *testing.T) {
	store := new_test_store(t)
	runner := &fake_runner{}
	w := test_watcher(store, runner, false)

	if err := w.Scan_once(context.Background()); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.calls))
	}
}

func Test_scan_once_scans_enabled_folders(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Config_set(ctx, "watch_enabled", "true"); err != nil {
		t.Fatalf("config: %v", err)
	}
	if err := store.Set_watch_folder(ctx, "/media/enabled", true); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if err := store.Set_watch_folder(ctx, "/media/disabled", false); err != nil {
		t.Fatalf("set disabled: %v", err)
	}

	runner := &fake_runner{}
	w := test_watcher(store, runner, false)
	if err := w.Scan_once(ctx); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	if len(runner.calls[0]) != 1 || runner.calls[0][0] != "/media/enabled" {
		t.Fatalf("directories = %v", runner.calls[0])
	}

	folders, err := store.List_watch_folders(ctx)
	if err != nil {
		t.Fatalf("list folders: %v", err)
	}
	for _, folder := range folders {
		if folder.Path == "/media/enabled" && folder.Last_scan == "" {
			t.Errorf("last_scan not recorded for enabled folder")
		}
		if folder.Path == "/media/disabled" && folder.Last_scan != "" {
			t.Errorf("last_scan recorded for disabled folder")
		}
	}
}

func Test_scan_once_uses_default_when_unset(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Set_watch_folder(ctx, "/media/movies", true); err != nil {
		t.Fatalf("set folder: %v", err)
	}

	runner := &fake_runner{}
	w := test_watcher(store, runner, true)
	if err := w.Scan_once(ctx); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
}
