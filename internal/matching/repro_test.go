package matching_test

import (
	"context"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
)

// Two separately-titled series entries whose files live under the SAME show
// folder resolve to the same TMDB show; matching them must not collide on the
// unique imdb_id index.
func Test_repro_two_entries_same_show_do_not_collide(t *testing.T) {
	service, store := new_test_service(t)
	ctx := context.Background()

	// Distinct titles so the upsert keeps them as two rows, but both their files
	// sit under one show folder, so lookup_name resolves them to the same show.
	for i, title := range []string{"Breaking Badx", "Breaking Bady"} {
		entry_id, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{
			Media_type: "series", Title: title,
		})
		if err != nil {
			t.Fatalf("upsert entry: %v", err)
		}
		ep_id, err := store.Upsert_episode(ctx, database.Episode{
			Catalog_entry_id: entry_id, Season_number: 2, Episode_number: 7 + i,
		})
		if err != nil {
			t.Fatalf("upsert episode: %v", err)
		}
		if _, err := store.Save_version(ctx, database.Version{
			Episode_id: ep_id,
			File_path:  "Breaking Bad/Breaking.Bad.S02E07.mkv",
			Size_bytes: 8000,
		}); err != nil {
			t.Fatalf("save version: %v", err)
		}
	}

	result, err := service.Run(ctx, nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("result = %+v", result)
	for _, e := range result.Errors {
		t.Logf("error: %s", e)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("matching collided on the shared show: %v", result.Errors)
	}
	entries, _ := store.List_catalog_entries(ctx, database.Catalog_filter{})
	for _, e := range entries {
		t.Logf("entry %d: title=%q imdb=%q tmdb=%d matched=%v type=%q", e.Id, e.Title, e.Imdb_id, e.Tmdb_id, e.Matched, e.Media_type)
	}
}
