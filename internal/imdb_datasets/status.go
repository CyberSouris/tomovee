package imdb_datasets

import (
	"sync"
)

// Status_state describes the offline index build state shown to the user.
type Status_state string

const (
	// State_idle reports no build has started yet.
	State_idle Status_state = "idle"
	// State_building reports the index is being rebuilt or populated.
	State_building Status_state = "building"
	// State_ready reports the index is up to date and usable.
	State_ready Status_state = "ready"
	// State_error reports the last build failed.
	State_error Status_state = "error"
)

// Status is a snapshot of the offline index build for status endpoints.
type Status struct {
	State    Status_state `json:"state"`
	Step     Build_step   `json:"step,omitempty"`
	Dataset  string       `json:"dataset,omitempty"`
	Bytes    int64        `json:"bytes,omitempty"`
	Total    int64        `json:"total,omitempty"`
	Percent  int          `json:"percent"`
	Message  string       `json:"message,omitempty"`
	Has_path bool         `json:"has_path"`
}

// Tracker records the progress of an offline index build made of
// Build_progress events, presenting a thread-safe snapshot for the web UI.
// It computes an overall percentage by weighting each dataset by its source
// file size, which correlates well with import wall time.
type Tracker struct {
	mu        sync.Mutex
	state     Status_state
	step      Build_step
	dataset   string
	bytes     int64
	processed int64 // bytes of fully imported datasets
	total     int64 // total source bytes of the whole build
	current   int64 // total source bytes of the dataset being imported
	message   string
	has_path  bool
}

// New_tracker creates a Tracker in the idle state.
func New_tracker() *Tracker {
	return &Tracker{state: State_idle}
}

// Observe applies one build event to the tracker.
func (t *Tracker) Observe(p Build_progress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch p.Step {
	case Build_stale:
		t.state = State_building
		t.step = Build_stale
		t.dataset = ""
		t.bytes = 0
		t.processed = 0
		t.current = 0
		t.total = p.Total
		t.message = "rebuilding dataset index"
	case Build_import:
		t.step = Build_import
		if !p.Done {
			t.dataset = p.Dataset
			t.current = p.Total
			t.bytes = p.Bytes
		} else {
			t.processed += p.Total
			t.bytes = 0
			t.current = 0
			t.dataset = ""
		}
	case Build_download:
		t.state = State_building
		t.step = Build_download
		t.dataset = p.Dataset
		t.bytes = p.Bytes
		t.total = p.Total
		t.current = 0
		t.message = "downloading IMDb datasets"
	case Build_fts:
		t.step = Build_fts
		t.dataset = ""
		t.bytes = 0
		t.message = "building full-text search index"
	case Build_ready:
		t.state = State_ready
		t.step = Build_ready
		t.dataset = ""
		t.processed = t.total
		t.bytes = 0
		t.current = 0
		t.message = ""
	case Build_reuse:
		t.state = State_ready
		t.step = Build_reuse
		t.message = "index is up to date"
	}
}

// Fail records a failed build.
func (t *Tracker) Fail(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = State_error
	t.message = message
}

// Set_has_path records whether a dataset path is configured at all.
func (t *Tracker) Set_has_path(has bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.has_path = has
}

// Snapshot returns a thread-safe copy of the tracker's current status.
func (t *Tracker) Snapshot() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := Status{
		State: t.state, Step: t.step, Dataset: t.dataset,
		Bytes: t.bytes, Total: t.total, Percent: t.percent_from_locked(),
		Message: t.message, Has_path: t.has_path,
	}
	return status
}

// percent_from_locked returns the current estimated build percentage using the
// accumulated state. Callers must hold mu.
func (t *Tracker) percent_from_locked() int {
	if t.total <= 0 {
		return 0
	}
	done := t.processed + t.bytes
	percent := int(float64(done) * 100 / float64(t.total))
	if percent > 100 {
		percent = 100
	}
	return percent
}
