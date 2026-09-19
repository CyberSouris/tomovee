package imdb_datasets

import (
	"testing"
)

func Test_tracker_reports_progress(t *testing.T) {
	tracker := New_tracker()

	tracker.Observe(Build_progress{Step: Build_stale, Total: 1000})
	status := tracker.Snapshot()
	if status.State != State_building || status.Percent != 0 {
		t.Fatalf("stale status = %+v, want building at 0%%", status)
	}

	tracker.Observe(Build_progress{Step: Build_import, Dataset: "title.basics", Total: 400})
	tracker.Observe(Build_progress{Step: Build_import, Dataset: "title.basics", Bytes: 200, Total: 400})
	status = tracker.Snapshot()
	if status.Dataset != "title.basics" || status.Percent != 20 {
		t.Fatalf("import status = %+v, want title.basics at 20%%", status)
	}

	tracker.Observe(Build_progress{Step: Build_import, Dataset: "title.basics", Rows: 9, Bytes: 400, Total: 400, Done: true})
	status = tracker.Snapshot()
	if status.Percent != 40 {
		t.Fatalf("done basics status = %+v, want 40%%", status)
	}

	tracker.Observe(Build_progress{Step: Build_fts})
	status = tracker.Snapshot()
	if status.Step != Build_fts {
		t.Fatalf("fts status = %+v, want fts step", status)
	}

	tracker.Observe(Build_progress{Step: Build_ready})
	status = tracker.Snapshot()
	if status.State != State_ready || status.Percent != 100 {
		t.Fatalf("ready status = %+v, want ready at 100%%", status)
	}
}

func Test_tracker_reuse_and_failure(t *testing.T) {
	tracker := New_tracker()
	tracker.Set_has_path(true)

	tracker.Observe(Build_progress{Step: Build_reuse})
	if status := tracker.Snapshot(); status.State != State_ready {
		t.Fatalf("reuse status = %+v, want ready", status)
	}

	tracker.Fail("boom")
	status := tracker.Snapshot()
	if status.State != State_error || status.Message != "boom" {
		t.Fatalf("fail status = %+v, want error", status)
	}
}

func Test_tracker_never_exceeds_hundred(t *testing.T) {
	tracker := New_tracker()
	tracker.Observe(Build_progress{Step: Build_stale, Total: 40})
	for i := 1; i <= 20; i++ {
		tracker.Observe(Build_progress{Step: Build_import, Bytes: int64(i * 10), Total: 40})
	}
	status := tracker.Snapshot()
	if status.Percent > 100 {
		t.Fatalf("percent = %d, want <= 100", status.Percent)
	}
}
