package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cybersouris/tomovee/internal/scan"
)

// Job is a single scan run and its most recent progress.
type Job struct {
	Id          string
	Started_at  time.Time
	Finished_at time.Time
	Running     bool
	Progress    scan.Progress
	Result      *scan.Result
	Error       string
}

// Job_manager serializes scan runs and fans progress out to SSE subscribers.
type Job_manager struct {
	logger *slog.Logger
	mu     sync.Mutex
	job    *Job
	cancel context.CancelFunc
	subs   map[int]chan scan.Progress
	next   int
}

// Err_job_running is returned when a scan is already in progress.
var Err_job_running = fmt.Errorf("a scan is already running")

func new_job_manager(logger *slog.Logger) *Job_manager {
	return &Job_manager{logger: logger, subs: make(map[int]chan scan.Progress)}
}

// Start launches a scan in the background and returns a snapshot of its job.
func (jm *Job_manager) Start(run func(context.Context, func(scan.Progress)) (*scan.Result, error)) (*Job, error) {
	jm.mu.Lock()
	if jm.job != nil && jm.job.Running {
		jm.mu.Unlock()
		return nil, Err_job_running
	}
	ctx, cancel := context.WithCancel(context.Background())
	jm.cancel = cancel
	jm.job = &Job{
		Id:         fmt.Sprintf("%d", time.Now().UnixNano()),
		Started_at: time.Now(),
		Running:    true,
		Progress:   scan.Progress{Phase: "starting"},
	}
	snapshot := jm.snapshot_locked()
	jm.mu.Unlock()

	go func() {
		result, err := run(ctx, jm.report)
		jm.mu.Lock()
		jm.job.Running = false
		jm.job.Finished_at = time.Now()
		jm.job.Result = result
		if err != nil {
			jm.job.Error = err.Error()
		}
		jm.job.Progress.Phase = "done"
		progress := jm.job.Progress
		jm.mu.Unlock()
		jm.broadcast(progress)
		cancel()
	}()
	return snapshot, nil
}

// Cancel requests cancellation of the running scan, if any.
func (jm *Job_manager) Cancel() {
	jm.mu.Lock()
	cancel := jm.cancel
	jm.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Snapshot returns a copy of the current job, or nil when none has run.
func (jm *Job_manager) Snapshot() *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.snapshot_locked()
}

func (jm *Job_manager) snapshot_locked() *Job {
	if jm.job == nil {
		return nil
	}
	copy := *jm.job
	return &copy
}

// Subscribe registers a progress channel and returns it with an unsubscribe
// function. The current progress is delivered immediately when a job exists.
func (jm *Job_manager) Subscribe() (<-chan scan.Progress, func()) {
	jm.mu.Lock()
	id := jm.next
	jm.next++
	ch := make(chan scan.Progress, 32)
	jm.subs[id] = ch
	if jm.job != nil {
		select {
		case ch <- jm.job.Progress:
		default:
		}
	}
	jm.mu.Unlock()

	unsubscribe := func() {
		jm.mu.Lock()
		if existing, ok := jm.subs[id]; ok {
			delete(jm.subs, id)
			close(existing)
		}
		jm.mu.Unlock()
	}
	return ch, unsubscribe
}

func (jm *Job_manager) report(progress scan.Progress) {
	jm.mu.Lock()
	if jm.job != nil {
		jm.job.Progress = progress
	}
	jm.mu.Unlock()
	jm.broadcast(progress)
}

func (jm *Job_manager) broadcast(progress scan.Progress) {
	jm.mu.Lock()
	subs := make([]chan scan.Progress, 0, len(jm.subs))
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
	Matched       int    `json:"matched"`
	Unmatched     int    `json:"unmatched"`
	Skipped       int    `json:"skipped"`
	Errors        int    `json:"errors"`
}

type result_item struct {
	Found     int      `json:"found"`
	Scanned   int      `json:"scanned"`
	New       int      `json:"new"`
	Matched   int      `json:"matched"`
	Unmatched int      `json:"unmatched"`
	Skipped   int      `json:"skipped"`
	Missing   int      `json:"missing"`
	Errors    []string `json:"errors,omitempty"`
}

func job_response_from(job *Job) *job_response {
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
			New_files: job.Progress.New_files, Matched: job.Progress.Matched,
			Unmatched: job.Progress.Unmatched, Skipped: job.Progress.Skipped,
			Errors: job.Progress.Errors,
		},
	}
	if !job.Finished_at.IsZero() {
		response.Finished_at = job.Finished_at.UTC().Format(time.RFC3339)
	}
	if job.Result != nil {
		response.Result = &result_item{
			Found: job.Result.Found, Scanned: job.Result.Scanned, New: job.Result.New,
			Matched: job.Result.Matched, Unmatched: job.Result.Unmatched,
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
	job, err := s.jobs.Start(func(ctx context.Context, progress func(scan.Progress)) (*scan.Result, error) {
		return s.scanner.Run_with_progress(ctx, progress)
	})
	if err != nil {
		write_error(w, http.StatusConflict, err.Error())
		return
	}
	write_json(w, http.StatusAccepted, job_response_from(job))
}

func (s *Server) handle_scan_status(w http.ResponseWriter, r *http.Request) {
	write_json(w, http.StatusOK, map[string]any{"job": job_response_from(s.jobs.Snapshot())})
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

	progress, unsubscribe := s.jobs.Subscribe()
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
				Matched: update.Matched, Unmatched: update.Unmatched,
				Skipped: update.Skipped, Errors: update.Errors,
			}}) {
				return
			}
		}
	}
}
