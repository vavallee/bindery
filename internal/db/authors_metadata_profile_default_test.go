package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestCreateForUser_StampsDefaultMetadataProfile is #1803. Six author-creation
// paths inserted a NULL metadata_profile_id: ABS import twice, the Calibre
// importer, the Goodreads migration, the CSV importer, and Hardcover list sync
// before #1783 patched that one by hand. Migration 071 backfilled the NULLs
// that had accumulated, but a one-shot cleanup in front of an ongoing leak only
// means the next import starts refilling it.
//
// Asserting at the repo rather than per importer is the point of the fix: all
// six funnel through Create/CreateForUser, so one stamp closes all of them and
// any path added later.
func TestCreateForUser_StampsDefaultMetadataProfile(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	authors := NewAuthorRepo(database)
	ctx := context.Background()

	a := &models.Author{Name: "Nil Profile", ForeignID: "mp-nil", SortName: "Nil Profile"}
	if a.MetadataProfileID != nil {
		t.Fatal("fixture already has a profile, the test would prove nothing")
	}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The in-memory struct must carry it, since callers copy fields off the
	// author they just created.
	if a.MetadataProfileID == nil {
		t.Fatal("in-memory MetadataProfileID is nil after Create, want the default")
	}
	if *a.MetadataProfileID != models.DefaultMetadataProfileID {
		t.Errorf("in-memory MetadataProfileID = %d, want %d", *a.MetadataProfileID, models.DefaultMetadataProfileID)
	}

	// And the row itself, which is what migration 071 had to go back and clean.
	stored, err := authors.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil {
		t.Fatal("author not found after create")
	}
	if stored.MetadataProfileID == nil {
		t.Fatal("stored metadata_profile_id is NULL, want the default (#1803)")
	}
	if *stored.MetadataProfileID != models.DefaultMetadataProfileID {
		t.Errorf("stored metadata_profile_id = %d, want %d", *stored.MetadataProfileID, models.DefaultMetadataProfileID)
	}
}

// TestCreateForUser_KeepsAnExplicitMetadataProfile guards the other direction:
// the stamp is a default, not an override. An author created against a specific
// profile must keep it.
func TestCreateForUser_KeepsAnExplicitMetadataProfile(t *testing.T) {
	t.Parallel()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	authors := NewAuthorRepo(database)
	profiles := NewMetadataProfileRepo(database)
	ctx := context.Background()

	custom := &models.MetadataProfile{Name: "German only", AllowedLanguages: "ger"}
	if err := profiles.Create(ctx, custom); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if custom.ID == models.DefaultMetadataProfileID {
		t.Fatalf("seeded profile got id %d, which collides with the default and would make this test vacuous", custom.ID)
	}

	a := &models.Author{Name: "Explicit Profile", ForeignID: "mp-explicit", SortName: "Explicit Profile", MetadataProfileID: &custom.ID}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, err := authors.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.MetadataProfileID == nil {
		t.Fatal("stored metadata_profile_id is NULL, want the caller's profile")
	}
	if *stored.MetadataProfileID != custom.ID {
		t.Errorf("stored metadata_profile_id = %d, want the caller's %d", *stored.MetadataProfileID, custom.ID)
	}
}
