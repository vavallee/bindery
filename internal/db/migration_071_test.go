package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMigrate071BackfillsNullMetadataProfile is the #1736 regression: authors
// created via the pre-fix ensureAuthor (internal/hardcoverlistsyncer) landed
// with metadata_profile_id NULL, unlike every author-create path that goes
// through applyAuthorCreateOptions. Migration 071 backfills existing NULL
// rows to the same default those paths would have stamped at create time.
func TestMigrate071BackfillsNullMetadataProfile(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	repo := NewAuthorRepo(database)
	author := &models.Author{
		ForeignID:        "hc:pre-fix-author",
		Name:             "Pre Fix Author",
		SortName:         "Author, Pre Fix",
		MetadataProvider: "hardcover",
	}
	if err := repo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-#1736 state: metadata_profile_id left NULL, as
	// ensureAuthor produced before it stamped a default.
	if _, err := database.ExecContext(ctx, `UPDATE authors SET metadata_profile_id = NULL WHERE id = ?`, author.ID); err != nil {
		t.Fatalf("clear metadata_profile_id: %v", err)
	}

	v071 := migrationVersionForTest(t, "071_backfill_null_metadata_profile.sql")
	if _, err := database.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, v071); err != nil {
		t.Fatalf("clear migration 071 marker: %v", err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("rerun migration 071: %v", err)
	}

	got, err := repo.GetByID(ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("author not found after migration")
	}
	if got.MetadataProfileID == nil {
		t.Fatal("MetadataProfileID is nil after migration 071, want DefaultMetadataProfileID")
	}
	if *got.MetadataProfileID != models.DefaultMetadataProfileID {
		t.Errorf("MetadataProfileID = %d, want %d", *got.MetadataProfileID, models.DefaultMetadataProfileID)
	}
}
