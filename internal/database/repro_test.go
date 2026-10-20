package database_test

import (
	"context"
	"testing"

	"github.com/cybersouris/tomovee/internal/database"
)

func Test_repro_update_two_entries_same_imdb_collides(t *testing.T) {
	d, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := database.New_store(d)
	ctx := context.Background()

	a, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{Media_type: "series", Title: "Dukes A"})
	if err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	b, err := store.Upsert_catalog_entry(ctx, database.Catalog_entry{Media_type: "series", Title: "Dukes B"})
	if err != nil {
		t.Fatalf("upsert b: %v", err)
	}

	if err := store.Update_catalog_entry(ctx, a, database.Catalog_entry{
		Media_type: "series", Title: "The Dukes Of Hazzard", Imdb_id: "tt0078614",
	}); err != nil {
		t.Fatalf("update a: %v", err)
	}
	err = store.Update_catalog_entry(ctx, b, database.Catalog_entry{
		Media_type: "series", Title: "The Dukes Of Hazzard", Imdb_id: "tt0078614",
	})
	t.Logf("update b err = %v", err)
	if err == nil {
		t.Fatal("expected a collision error from updating a second entry with the same imdb_id")
	}
}
