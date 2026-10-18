package matching_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
)

// new_trigger_service opens a file-backed database, applies migrations through
// the normal Open path, and hands back the path so a failure trigger can be
// added before the code under test runs.
func new_trigger_service(t *testing.T, offline matcher.Offline_source) (*matching.Matching, *database.Store, string) {
	t.Helper()
	database_path := filepath.Join(t.TempDir(), "trigger.db")
	d, err := database.Open(database_path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := matcher.New(matcher.Options{Metadata: fake_movie_metadata{}, Offline: offline, Logger: logger})
	return matching.New(store, m, nil, logger), store, database_path
}

// inject_failure installs a SQLite trigger that aborts the given statement, so
// a store write can be made to fail after the fixture is in place. The trigger
// lives in the database file, so the service's own connection sees it.
func inject_failure(t *testing.T, database_path, trigger string) {
	t.Helper()
	dsn := "file:" + url.PathEscape(database_path) + "?_pragma=journal_mode(WAL)"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(trigger); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
}

// merge_show_folder_fixture seeds the merge shape: an existing series owning a
// numbered episode plus a loose movie sharing the show folder. It returns the
// movie, the entry a re-typed rematch starts from.
func merge_show_folder_fixture(t *testing.T, store *database.Store) int64 {
	t.Helper()
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())

	series_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "series", Title: "The Office", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert series: %v", err)
	}
	episode_id, err := store.Upsert_episode(ctx, database.Episode{
		Catalog_entry_id: series_id, Season_number: 1, Episode_number: 1, Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Episode_id: episode_id, Library_id: lib, File_path: "The.Office/Season 1/The.Office.S01E01.mkv", Size_bytes: 1000,
	}); err != nil {
		t.Fatalf("save series version: %v", err)
	}

	movie_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office Interviews", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: lib, File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 900,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}
	return movie_id
}

// movie_show_folder_fixture seeds a lone movie inside a show folder — no
// sibling series — so a reclassify flips it in place: the anchor is renamed to
// the folder and its file numbered as the first episode.
func movie_show_folder_fixture(t *testing.T, store *database.Store) int64 {
	t.Helper()
	ctx := context.Background()
	lib := library_id(t, store, "TV", t.TempDir())
	movie_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
		Media_type: "movie", Title: "The Office Interviews", Status: "needs_lookup",
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if _, err := store.Save_version(ctx, database.Version{
		Catalog_entry_id: movie_id, Library_id: lib, File_path: "The.Office/Season 1/Interviews.mkv", Size_bytes: 900,
	}); err != nil {
		t.Fatalf("save movie version: %v", err)
	}
	return movie_id
}

// Test_reclassify_merge_fold_failure_returns_error makes every version UPDATE
// abort: folding the drained movie into the existing series fails inside
// fold_drained_versions, and the error must propagate out of Reclassify with
// the anchor untouched.
func Test_reclassify_merge_fold_failure_returns_error(t *testing.T) {
	service, store, database_path := new_trigger_service(t, fake_series_hit{})
	movie_id := merge_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_version_update BEFORE UPDATE ON version
		BEGIN SELECT RAISE(ABORT, 'injected fold failure'); END`)

	target, applied, candidates, err := service.Reclassify(context.Background(), movie_id, true)
	if err == nil || !strings.Contains(err.Error(), "injected fold failure") {
		t.Fatalf("reclassify err = %v, want the fold failure to propagate", err)
	}
	if target != movie_id || applied || candidates != nil {
		t.Errorf("target=%d applied=%v candidates=%v, want the anchor unchanged on failure", target, applied, candidates)
	}
}

// Test_reclassify_merge_delete_failure_returns_error lets folding succeed but
// aborts the drained movie's catalog DELETE: delete_drained_owners fails after
// its version check, and the error must propagate out of Reclassify.
func Test_reclassify_merge_delete_failure_returns_error(t *testing.T) {
	service, store, database_path := new_trigger_service(t, fake_series_hit{})
	movie_id := merge_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_entry_delete BEFORE DELETE ON catalog_entry
		BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)

	target, applied, candidates, err := service.Reclassify(context.Background(), movie_id, true)
	if err == nil || !strings.Contains(err.Error(), "injected delete failure") {
		t.Fatalf("reclassify err = %v, want the drained-owner delete failure to propagate", err)
	}
	if target != movie_id || applied || candidates != nil {
		t.Errorf("target=%d applied=%v candidates=%v, want the anchor unchanged on failure", target, applied, candidates)
	}
}

// Test_reclassify_rename_failure_returns_error aborts the first title change:
// the flip re-writes the entry under its own title, but only the rename to the
// show folder changes it, so rename_anchor_to_folder fails and its error must
// propagate out of Reclassify.
func Test_reclassify_rename_failure_returns_error(t *testing.T) {
	service, store, database_path := new_trigger_service(t, fake_series_hit{})
	movie_id := movie_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_rename BEFORE UPDATE OF title ON catalog_entry
		WHEN NEW.title <> OLD.title
		BEGIN SELECT RAISE(ABORT, 'injected rename failure'); END`)

	target, applied, candidates, err := service.Reclassify(context.Background(), movie_id, true)
	if err == nil || !strings.Contains(err.Error(), "injected rename failure") {
		t.Fatalf("reclassify err = %v, want the anchor rename failure to propagate", err)
	}
	if target != movie_id || applied || candidates != nil {
		t.Errorf("target=%d applied=%v candidates=%v, want the anchor unchanged on failure", target, applied, candidates)
	}
}

// Test_reclassify_numbering_failure_returns_error lets the rename pass but
// aborts the first episode INSERT: numbering the loose file fails inside
// number_series_versions, and the error must propagate out of Reclassify.
func Test_reclassify_numbering_failure_returns_error(t *testing.T) {
	service, store, database_path := new_trigger_service(t, fake_series_hit{})
	movie_id := movie_show_folder_fixture(t, store)
	inject_failure(t, database_path, `CREATE TRIGGER fail_episode_insert BEFORE INSERT ON episode
		BEGIN SELECT RAISE(ABORT, 'injected episode failure'); END`)

	target, applied, candidates, err := service.Reclassify(context.Background(), movie_id, true)
	if err == nil || !strings.Contains(err.Error(), "injected episode failure") {
		t.Fatalf("reclassify err = %v, want the numbering failure to propagate", err)
	}
	if target != movie_id || applied || candidates != nil {
		t.Errorf("target=%d applied=%v candidates=%v, want the anchor unchanged on failure", target, applied, candidates)
	}
}
