package watcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/scan"
)

func Test_run_loops_until_cancelled(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
	}

	runner := &fake_runner{}
	w := New(store, runner, Options{
		Interval:        10 * time.Millisecond,
		Default_enabled: true,
	})

	run_ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(run_ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for len(runner.calls) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("watcher only ran %d times, want at least 2", len(runner.calls))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if len(runner.calls) < 2 {
		t.Errorf("runner calls = %d, want at least 2", len(runner.calls))
	}
}

type failing_runner struct {
	calls atomic.Int32
}

func (f *failing_runner) Run_libraries(context.Context, []string, func(scan.Progress)) (*scan.Result, error) {
	f.calls.Add(1)
	return nil, errors.New("scan exploded")
}

func Test_run_ignores_scan_errors(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
	}

	runner := &failing_runner{}
	w := New(store, runner, Options{
		Interval:        10 * time.Millisecond,
		Default_enabled: true,
	})

	run_ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(run_ctx) }()

	// The failure is only logged; Run must keep polling.
	deadline := time.Now().Add(2 * time.Second)
	for runner.calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("watcher only ran %d times, want at least 2", runner.calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func Test_scan_once_store_error_is_quiet(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := database.New_store(d)
	_ = d.Close()

	runner := &fake_runner{}
	w := test_watcher(store, runner, true)
	if err := w.Scan_once(context.Background()); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner calls = %d, want 0", len(runner.calls))
	}
}

func Test_scan_once_reports_result_to_on_scan(t *testing.T) {
	store := new_test_store(t)
	ctx := context.Background()
	if err := store.Upsert_library(ctx, "media", "/media/movies", true); err != nil {
		t.Fatalf("set library: %v", err)
	}

	var received []scan.Result
	w := New(store, &fake_runner{}, Options{
		Default_enabled: true,
		On_scan:         func(r scan.Result) { received = append(received, r) },
		Run_scan: func(context.Context, []string) (*scan.Result, error) {
			return &scan.Result{Found: 3, New: 2, Skipped: 1}, nil
		},
	})
	if err := w.Scan_once(ctx); err != nil {
		t.Fatalf("scan_once: %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("on_scan calls = %d, want 1", len(received))
	}
	if received[0].New != 2 || received[0].Found != 3 {
		t.Errorf("result = %+v, want New=2 Found=3", received[0])
	}
}
