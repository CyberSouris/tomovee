package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cybersouris/tomovee/internal/imdb_datasets"
	"github.com/cybersouris/tomovee/internal/matching"
	"github.com/cybersouris/tomovee/internal/scan"
)

// job is a single background run and its most recent progress.
type job[R any, T any] struct {
	Id          string
	Started_at  time.Time
	Finished_at time.Time
	Running     bool
	Progress    R
	Result      *T
	Error       string
}

// pending_result carries the outcome of a queued run to the goroutine that
// submitted it.
type pending_result[T any] struct {
	result *T
	err    error
}

// pending_run is one queued job waiting for the manager's single slot. It is
// executed in FIFO order once the active job finishes.
type pending_run[R any, T any] struct {
	run  func(context.Context, func(R)) (*T, error)
	done chan pending_result[T]
}

// job_manager serializes background jobs of one kind and fans progress out to
// SSE subscribers.
type job_manager[R any, T any] struct {
	logger *slog.Logger
	mu     sync.Mutex
	job    *job[R, T]
	queue  []*pending_run[R, T]
	cancel context.CancelFunc
	subs   map[int]chan R
	next   int
}

// Err_job_running is returned when a job is already in progress.
var Err_job_running = errors.New("a job is already running")

// Err_too_many_subscribers is returned when an SSE feed already has the
// maximum number of concurrent subscribers, so a client cannot accumulate
// unbounded buffered-channel memory.
var Err_too_many_subscribers = errors.New("too many stream subscribers")

// max_subscribers caps the concurrent SSE connections a job feed admits.
const max_subscribers = 8

func new_job_manager[R any, T any](logger *slog.Logger) *job_manager[R, T] {
	return &job_manager[R, T]{logger: logger, subs: make(map[int]chan R)}
}

// Start launches a job in the background and returns a snapshot of it.
func (jm *job_manager[R, T]) Start(run func(context.Context, func(R)) (*T, error)) (*job[R, T], error) {
	ctx, cancel, snapshot, err := jm.begin()
	if err != nil {
		return nil, err
	}
	go func() {
		result, run_err := run(ctx, jm.report)
		jm.finish(result, run_err)
		cancel()
	}()
	return snapshot, nil
}

// Run_sync executes a job synchronously in the caller's goroutine while still
// keeping the manager's snapshot and SSE subscribers in sync. It lets callers
// that own their own scheduling (the folder watcher) surface their work in the
// global background status like any other job. It returns Err_job_running when
// another job is already in progress.
func (jm *job_manager[R, T]) Run_sync(run func(context.Context, func(R)) (*T, error)) (*T, error) {
	ctx, cancel, _, err := jm.begin()
	if err != nil {
		return nil, err
	}
	result, run_err := run(ctx, jm.report)
	jm.finish(result, run_err)
	cancel()
	return result, run_err
}

// Run_sync_queued is Run_sync with queueing: when another job is in progress
// the run is appended to the manager's FIFO queue instead of being rejected
// with Err_job_running, and the call blocks until the queued run actually
// completes so the caller receives its real result. The finishing job hands
// its slot to the next queued run, so queued work always runs after everything
// that preceded it while still surfacing in the global background status.
func (jm *job_manager[R, T]) Run_sync_queued(run func(context.Context, func(R)) (*T, error)) (*T, error) {
	for {
		ctx, cancel, _, err := jm.begin()
		if err == nil {
			result, run_err := run(ctx, jm.report)
			jm.finish(result, run_err)
			cancel()
			return result, run_err
		}
		if !errors.Is(err, Err_job_running) {
			return nil, err
		}
		pending := &pending_run[R, T]{run: run, done: make(chan pending_result[T], 1)}
		jm.mu.Lock()
		if jm.job == nil || !jm.job.Running {
			// The active job finished between the begin call above and this
			// enqueue, so nobody will ever drain the queue; retry and claim the
			// slot directly instead of waiting forever.
			jm.mu.Unlock()
			continue
		}
		jm.queue = append(jm.queue, pending)
		jm.mu.Unlock()
		outcome := <-pending.done
		return outcome.result, outcome.err
	}
}

// Start_queued is Start with FIFO queueing: when another job is running the
// run is appended to the queue instead of being rejected with Err_job_running,
// and the call returns immediately with a placeholder snapshot so the API can
// acknowledge the request. The finishing job hands its slot to the next queued
// run, which then surfaces in the global background status like a normal job.
func (jm *job_manager[R, T]) Start_queued(run func(context.Context, func(R)) (*T, error)) (*job[R, T], error) {
	for {
		snapshot, err := jm.Start(run)
		if err == nil {
			return snapshot, nil
		}
		if !errors.Is(err, Err_job_running) {
			return nil, err
		}
		pending := &pending_run[R, T]{run: run, done: make(chan pending_result[T], 1)}
		jm.mu.Lock()
		if jm.job == nil || !jm.job.Running {
			// The active job finished between the Start call above and this
			// enqueue, so nobody will ever drain the queue; retry and claim the
			// slot directly instead of leaving a run stuck in limbo.
			jm.mu.Unlock()
			continue
		}
		jm.queue = append(jm.queue, pending)
		jm.mu.Unlock()
		break
	}
	// Placeholder snapshot so the caller can acknowledge the request; the real
	// job replaces it in status/SSE once it dequeues.
	return &job[R, T]{
		Id:         fmt.Sprintf("%d", time.Now().UnixNano()),
		Started_at: time.Now(),
		Running:    true,
	}, nil
}

// begin claims the job slot for a new job, marking it running, and returns the
// derived context, its cancel function, and a snapshot suitable for the HTTP
// response. It returns Err_job_running when another job is in progress.
func (jm *job_manager[R, T]) begin() (context.Context, context.CancelFunc, *job[R, T], error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if jm.job != nil && jm.job.Running {
		return nil, nil, nil, Err_job_running
	}
	ctx, cancel := context.WithCancel(context.Background())
	jm.cancel = cancel
	var zero R
	jm.job = &job[R, T]{
		Id:         fmt.Sprintf("%d", time.Now().UnixNano()),
		Started_at: time.Now(),
		Running:    true,
		Progress:   zero,
	}
	return ctx, cancel, jm.snapshot_locked(), nil
}

// finish records the outcome of a finished job and broadcasts a final progress
// update so SSE subscribers observe the transition. Failures are logged
// server-side; the surfaced error message stays generic so internal details
// never reach the API. When queued runs are waiting, the slot is handed to the
// next one in the same critical section, so a concurrent Start or Run_sync can
// never observe the free slot ahead of the queue.
func (jm *job_manager[R, T]) finish(result *T, err error) {
	jm.mu.Lock()
	done := jm.job
	if done == nil {
		jm.mu.Unlock()
		return
	}
	done.Running = false
	done.Finished_at = time.Now()
	done.Result = result
	if err != nil {
		jm.logger.Error("background job failed", "job_id", done.Id, "error", err)
		done.Error = "background job failed"
	}
	progress := done.Progress

	next := jm.next_pending_locked()
	if next == nil {
		jm.mu.Unlock()
		jm.broadcast(progress)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	jm.cancel = cancel
	var zero R
	jm.job = &job[R, T]{
		Id:         fmt.Sprintf("%d", time.Now().UnixNano()),
		Started_at: time.Now(),
		Running:    true,
		Progress:   zero,
	}
	jm.mu.Unlock()
	jm.broadcast(progress)

	queued_result, queued_err := next.run(ctx, jm.report)
	next.done <- pending_result[T]{result: queued_result, err: queued_err}
	jm.finish(queued_result, queued_err)
	cancel()
}

// next_pending_locked pops the oldest queued run, or nil when the queue is
// empty. It must be called with mu held.
func (jm *job_manager[R, T]) next_pending_locked() *pending_run[R, T] {
	if len(jm.queue) == 0 {
		return nil
	}
	next := jm.queue[0]
	jm.queue = jm.queue[1:]
	return next
}

// Cancel requests cancellation of the running job, if any.
func (jm *job_manager[R, T]) Cancel() {
	jm.mu.Lock()
	cancel := jm.cancel
	jm.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Snapshot returns a copy of the current job, or nil when none has run.
func (jm *job_manager[R, T]) Snapshot() *job[R, T] {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.snapshot_locked()
}

func (jm *job_manager[R, T]) snapshot_locked() *job[R, T] {
	if jm.job == nil {
		return nil
	}
	copy := *jm.job
	return &copy
}

// Subscribe registers a progress channel and returns it with an unsubscribe
// function. The current progress is delivered immediately when a job exists.
func (jm *job_manager[R, T]) Subscribe() (<-chan R, func(), error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.subs) >= max_subscribers {
		return nil, nil, Err_too_many_subscribers
	}
	id := jm.next
	jm.next++
	ch := make(chan R, 32)
	jm.subs[id] = ch
	if jm.job != nil {
		select {
		case ch <- jm.job.Progress:
		default:
		}
	}

	unsubscribe := func() {
		jm.mu.Lock()
		if existing, ok := jm.subs[id]; ok {
			delete(jm.subs, id)
			close(existing)
		}
		jm.mu.Unlock()
	}
	return ch, unsubscribe, nil
}

func (jm *job_manager[R, T]) report(progress R) {
	jm.mu.Lock()
	if jm.job != nil {
		jm.job.Progress = progress
	}
	jm.mu.Unlock()
	jm.broadcast(progress)
}

func (jm *job_manager[R, T]) broadcast(progress R) {
	jm.mu.Lock()
	subs := make([]chan R, 0, len(jm.subs))
	for _, ch := range jm.subs {
		subs = append(subs, ch)
	}
	jm.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- progress:
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// Scan jobs

type job_response struct {
	Id          string        `json:"id"`
	Started_at  string        `json:"started_at"`
	Finished_at string        `json:"finished_at,omitempty"`
	Running     bool          `json:"running"`
	Progress    progress_item `json:"progress"`
	Result      *result_item  `json:"result,omitempty"`
	Error       string        `json:"error,omitempty"`
}

type progress_item struct {
	Phase         string `json:"phase"`
	Path          string `json:"path,omitempty"`
	Files_found   int    `json:"files_found"`
	Files_scanned int    `json:"files_scanned"`
	New_files     int    `json:"new_files"`
	Skipped       int    `json:"skipped"`
	Errors        int    `json:"errors"`
}

type result_item struct {
	Found   int      `json:"found"`
	Scanned int      `json:"scanned"`
	New     int      `json:"new"`
	Skipped int      `json:"skipped"`
	Missing int      `json:"missing"`
	Errors  []string `json:"errors,omitempty"`
}

func job_response_from(job *job[scan.Progress, scan.Result]) *job_response {
	if job == nil {
		return nil
	}
	response := &job_response{
		Id:         job.Id,
		Started_at: job.Started_at.UTC().Format(time.RFC3339),
		Running:    job.Running,
		Error:      job.Error,
		Progress: progress_item{
			Phase: job.Progress.Phase, Path: job.Progress.Path,
			Files_found: job.Progress.Files_found, Files_scanned: job.Progress.Files_scanned,
			New_files: job.Progress.New_files, Skipped: job.Progress.Skipped,
			Errors: job.Progress.Errors,
		},
	}
	if !job.Finished_at.IsZero() {
		response.Finished_at = job.Finished_at.UTC().Format(time.RFC3339)
	}
	if job.Result != nil {
		response.Result = &result_item{
			Found: job.Result.Found, Scanned: job.Result.Scanned, New: job.Result.New,
			Skipped: job.Result.Skipped, Missing: job.Result.Missing, Errors: job.Result.Errors,
		}
	}
	return response
}

func (s *Server) handle_scan_start(w http.ResponseWriter, r *http.Request) {
	if s.scanner == nil {
		write_error(w, http.StatusServiceUnavailable, "scanner is not configured")
		return
	}
	var request struct {
		Libraries []string `json:"libraries"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			write_error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	job, err := s.jobs.Start(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
		return s.scanner.Run_libraries(ctx, request.Libraries, progress)
	})
	if err != nil {
		write_error(w, http.StatusConflict, job_running_error("a scan is already running"))
		return
	}
	write_json(w, http.StatusAccepted, job_response_from(job))
}

func (s *Server) handle_scan_status(w http.ResponseWriter, r *http.Request) {
	write_json(w, http.StatusOK, map[string]any{"job": job_response_from(s.jobs.Snapshot())})
}

// Watch_scan runs a scan synchronously through the same job manager the manual
// scan endpoint uses, so folder-watcher triggers surface in the global
// background status and SSE feed. It returns scan.Err_scan_in_progress when
// another scan is already running, so the watcher can skip its round instead
// of colliding with a manual scan.
func (s *Server) Watch_scan(ctx context.Context, names []string) (*scan.Result, error) {
	if s.scanner == nil {
		return nil, errors.New("scanner is not configured")
	}
	result, err := s.jobs.Run_sync(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
		return s.scanner.Run_libraries(ctx, names, progress)
	})
	if errors.Is(err, Err_job_running) {
		return nil, scan.Err_scan_in_progress
	}
	return result, err
}

// background_status aggregates the state of every background activity (folder
// scans, matching runs, and the offline IMDb index build) so the UI can show a
// single global progress indicator.
type background_status struct {
	Scan     *job_response         `json:"scan,omitempty"`
	Match    *match_job_response   `json:"match,omitempty"`
	Datasets *imdb_datasets.Status `json:"datasets,omitempty"`
}

func (s *Server) handle_background_status(w http.ResponseWriter, r *http.Request) {
	datasets := (*imdb_datasets.Status)(nil)
	if s.datasets != nil {
		status := s.datasets.Snapshot()
		datasets = &status
	}
	write_json(w, http.StatusOK, background_status{
		Scan:     job_response_from(s.jobs.Snapshot()),
		Match:    match_job_response_from(s.matches.Snapshot()),
		Datasets: datasets,
	})
}

func (s *Server) handle_scan_stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		write_error(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	progress, unsubscribe, err := s.jobs.Subscribe()
	if err != nil {
		write_error(w, http.StatusServiceUnavailable, "too many scan stream subscribers")
		return
	}
	defer unsubscribe()

	send := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send("status", map[string]any{"job": job_response_from(s.jobs.Snapshot())})

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case update, ok := <-progress:
			if !ok {
				return
			}
			event := "progress"
			if update.Phase == "done" {
				event = "done"
			}
			if !send(event, map[string]any{"progress": progress_item{
				Phase: update.Phase, Path: update.Path, Files_found: update.Files_found,
				Files_scanned: update.Files_scanned, New_files: update.New_files,
				Skipped: update.Skipped, Errors: update.Errors,
			}}) {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Matching jobs

type match_job_response struct {
	Id          string              `json:"id"`
	Started_at  string              `json:"started_at"`
	Finished_at string              `json:"finished_at,omitempty"`
	Running     bool                `json:"running"`
	Progress    match_progress_item `json:"progress"`
	Result      *match_result_item  `json:"result,omitempty"`
	Error       string              `json:"error,omitempty"`
}

type match_progress_item struct {
	Phase     string `json:"phase"`
	Title     string `json:"title,omitempty"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	Matched   int    `json:"matched"`
	Unmatched int    `json:"unmatched"`
	Errors    int    `json:"errors"`
}

type match_result_item struct {
	Total      int      `json:"total"`
	Matched    int      `json:"matched"`
	Unmatched  int      `json:"unmatched"`
	Candidates int      `json:"candidates,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

func match_job_response_from(job *job[matching.Progress, matching.Result]) *match_job_response {
	if job == nil {
		return nil
	}
	response := &match_job_response{
		Id:         job.Id,
		Started_at: job.Started_at.UTC().Format(time.RFC3339),
		Running:    job.Running,
		Error:      job.Error,
		Progress: match_progress_item{
			Phase: job.Progress.Phase, Title: job.Progress.Title,
			Total: job.Progress.Total, Done: job.Progress.Done,
			Matched: job.Progress.Matched, Unmatched: job.Progress.Unmatched,
			Errors: job.Progress.Errors,
		},
	}
	if !job.Finished_at.IsZero() {
		response.Finished_at = job.Finished_at.UTC().Format(time.RFC3339)
	}
	if job.Result != nil {
		response.Result = &match_result_item{
			Total: job.Result.Total, Matched: job.Result.Matched,
			Unmatched: job.Result.Unmatched, Candidates: job.Result.Candidates,
			Errors: job.Result.Errors,
		}
	}
	return response
}

// Auto_match starts a background matching run at server startup, if matching
// is configured. It never blocks and is idempotent when a run is in progress.
func (s *Server) Auto_match() {
	if s.matching == nil {
		return
	}
	_, _ = s.matches.Start(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		return s.matching.Run(ctx, nil, progress)
	})
}

func (s *Server) handle_match_start(w http.ResponseWriter, r *http.Request) {
	if s.matching == nil || s.matcher == nil {
		write_error(w, http.StatusServiceUnavailable, "matching is not configured")
		return
	}
	if !s.matcher.Has_sources() {
		write_error(w, http.StatusServiceUnavailable, s.matching_unavailable_message())
		return
	}
	var request struct {
		Libraries []string `json:"libraries"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			write_error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	job, err := s.matches.Start(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		return s.matching.Run(ctx, request.Libraries, progress)
	})
	if err != nil {
		write_error(w, http.StatusConflict, job_running_error("a matching job is already running"))
		return
	}
	write_json(w, http.StatusAccepted, match_job_response_from(job))
}

// handle_rematch_all_start starts a background rematch run over every known
// entry. It goes through the same job manager as the Match button, so it shares
// the progress stream, and queues behind a running match job instead of failing
// with a conflict.
func (s *Server) handle_rematch_all_start(w http.ResponseWriter, r *http.Request) {
	if s.matching == nil || s.matcher == nil {
		write_error(w, http.StatusServiceUnavailable, "matching is not configured")
		return
	}
	if !s.matcher.Has_sources() {
		write_error(w, http.StatusServiceUnavailable, s.matching_unavailable_message())
		return
	}
	var request struct {
		Libraries []string `json:"libraries"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			write_error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	job, err := s.matches.Start_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		return s.matching.Run_rematch(ctx, request.Libraries, progress)
	})
	if err != nil {
		write_error(w, http.StatusConflict, job_running_error("a matching job is already running"))
		return
	}
	write_json(w, http.StatusAccepted, match_job_response_from(job))
}

// matching_unavailable_message explains why the background matching job cannot
// run, so users see what to configure instead of a dead-end error.
func (s *Server) matching_unavailable_message() string {
	datasets := ""
	var tmdb, open_subtitles string
	if s.cfg != nil {
		datasets = s.cfg.Imdb_datasets_path
		tmdb = s.cfg.Api.Tmdb_key
		open_subtitles = s.cfg.Api.Opensubtitles_api_key
	}
	if datasets != "" {
		percent := s.datasets_percent()
		if s.datasets_building() {
			return fmt.Sprintf("matching is configured but the local IMDb index is still being built in the background (%d%% done). Matching becomes available automatically once it finishes.", percent)
		}
		return "matching is configured but the local IMDb index failed to load. Check the tomovee log for errors."
	}
	if tmdb != "" || open_subtitles != "" {
		return "matching sources are configured but unavailable. Check the tomovee log for errors."
	}
	return "matching is not configured: set the tmdb_key and/or opensubtitles_api_key under api, or point imdb_datasets_path at your IMDb dataset exports in the config file, then restart."
}

func (s *Server) datasets_building() bool {
	return s.datasets != nil && s.datasets.Snapshot().State == imdb_datasets.State_building
}

func (s *Server) datasets_percent() int {
	if s.datasets == nil {
		return 0
	}
	return s.datasets.Snapshot().Percent
}

func (s *Server) handle_match_status(w http.ResponseWriter, r *http.Request) {
	write_json(w, http.StatusOK, map[string]any{"job": match_job_response_from(s.matches.Snapshot())})
}

func (s *Server) handle_match_stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		write_error(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	progress, unsubscribe, err := s.matches.Subscribe()
	if err != nil {
		write_error(w, http.StatusServiceUnavailable, "too many match stream subscribers")
		return
	}
	defer unsubscribe()

	send := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send("status", map[string]any{"job": match_job_response_from(s.matches.Snapshot())})

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case update, ok := <-progress:
			if !ok {
				return
			}
			event := "progress"
			if update.Phase == "done" {
				event = "done"
			}
			if !send(event, map[string]any{"progress": match_progress_item{
				Phase: update.Phase, Title: update.Title, Total: update.Total,
				Done: update.Done, Matched: update.Matched,
				Unmatched: update.Unmatched, Errors: update.Errors,
			}}) {
				return
			}
		}
	}
}

func job_running_error(message string) string {
	return message
}
