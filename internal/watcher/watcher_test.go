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

func (f *fake_runner) Run_libraries(_ context.Context, names []string, _ func(scan.Progress)) (*scan.Result, error) {
	f.calls = append(f.calls, append([]string(nil), names...))
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

func Test_scan_once_scans_enabled_libraries(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Config_set(ctx, "watch_enabled", "true"); err != nil {
		t.Fatalf("config: %v", err)
	}
	if err := store.Upsert_library(ctx, "enabled", "/media/enabled", true); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if err := store.Upsert_library(ctx, "disabled", "/media/disabled", false); err != nil {
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
	if len(runner.calls[0]) != 1 || runner.calls[0][0] != "enabled" {
		t.Fatalf("names = %v", runner.calls[0])
	}

	libraries, err := store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	for _, library := range libraries {
		if library.Name == "enabled" && library.Last_scan == "" {
			t.Errorf("last_scan not recorded for enabled library")
		}
		if library.Name == "disabled" && library.Last_scan != "" {
			t.Errorf("last_scan recorded for disabled library")
		}
	}
}

func Test_scan_once_uses_default_when_unset(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
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

func Test_scan_once_routes_through_run_scan(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
	}

	calls := 0
	runner := &fake_runner{}
	w := New(store, runner, Options{
		Default_enabled: true,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Run_scan: func(_ context.Context, names []string) (*scan.Result, error) {
			calls++
			if len(names) != 1 || names[0] != "media" {
				t.Errorf("names = %v", names)
			}
			return &scan.Result{}, nil
		},
	})
	if err := w.Scan_once(ctx); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if calls != 1 {
		t.Errorf("run_scan calls = %d, want 1", calls)
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner called %d times, want 0", len(runner.calls))
	}
}

func Test_scan_once_skips_quietly_when_scan_running(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
	}

	calls := 0
	w := New(store, &fake_runner{}, Options{
		Default_enabled: true,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Run_scan: func(context.Context, []string) (*scan.Result, error) {
			calls++
			return nil, scan.Err_scan_in_progress
		},
	})
	if err := w.Scan_once(ctx); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if calls != 1 {
		t.Errorf("run_scan calls = %d, want 1", calls)
	}

	libraries, err := store.List_libraries(ctx)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	for _, library := range libraries {
		if library.Last_scan != "" {
			t.Errorf("last_scan recorded despite skipped scan")
		}
	}
}
