package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestMetadataProfileRepo_CRUD(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewMetadataProfileRepo(database)

	// GetByID missing returns (nil, nil).
	missing, err := repo.GetByID(ctx, 9999)
	if err != nil {
		t.Fatalf("GetByID missing: %v", err)
	}
	if missing != nil {
		t.Errorf("want nil for missing profile, got %+v", missing)
	}

	// Create
	p := &models.MetadataProfile{
		Name:             "Strict",
		MinPopularity:    100,
		MinPages:         50,
		SkipMissingDate:  true,
		SkipMissingISBN:  false,
		SkipPartBooks:    true,
		AllowedLanguages: "eng,fra",
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// GetByID round-trips all bool fields accurately.
	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected profile")
		return
	}
	if got.Name != "Strict" || got.MinPopularity != 100 || got.MinPages != 50 {
		t.Errorf("scalar fields mismatch: %+v", got)
	}
	if !got.SkipMissingDate || got.SkipMissingISBN || !got.SkipPartBooks {
		t.Errorf("bool fields mismatch: %+v", got)
	}
	if got.AllowedLanguages != "eng,fra" {
		t.Errorf("AllowedLanguages mismatch: %q", got.AllowedLanguages)
	}

	// List returns the row.
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, mp := range list {
		if mp.ID == p.ID {
			found = true
		}
	}
	if !found {
		t.Error("created profile missing from List")
	}

	// Update flips bools and renames.
	p.Name = "Relaxed"
	p.SkipMissingDate = false
	p.SkipMissingISBN = true
	p.SkipPartBooks = false
	p.MinPages = 10
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = repo.GetByID(ctx, p.ID)
	if got.Name != "Relaxed" || got.MinPages != 10 {
		t.Errorf("update not applied: %+v", got)
	}
	if got.SkipMissingDate || !got.SkipMissingISBN || got.SkipPartBooks {
		t.Errorf("bool update mismatch: %+v", got)
	}

	// Delete
	if err := repo.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = repo.GetByID(ctx, p.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// TestMetadataProfileRepo_ScoreThresholds is the regression test for
// migration 086 (#2235): a pre-086 row (i.e. one Create doesn't set
// KeepThreshold/ExcludeThreshold on) must default to 0/0 — the value that
// reproduces internal/metadata/filterengine's pre-#2235 behavior exactly —
// and a profile that does set them must round-trip through Create, GetByID,
// List, and Update without loss.
func TestMetadataProfileRepo_ScoreThresholds(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewMetadataProfileRepo(database)

	// A profile created without touching the new fields gets the DEFAULT 0
	// migration 086 ships, not a zero-value-looks-unset ambiguity.
	legacy := &models.MetadataProfile{Name: "Legacy", AllowedLanguages: "eng"}
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByID(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.KeepThreshold != 0 || got.ExcludeThreshold != 0 {
		t.Errorf("legacy-shaped profile thresholds = keep=%v exclude=%v, want 0/0", got.KeepThreshold, got.ExcludeThreshold)
	}

	// A profile that does set them round-trips exactly.
	p := &models.MetadataProfile{
		Name:             "Graded",
		AllowedLanguages: "eng",
		KeepThreshold:    12.5,
		ExcludeThreshold: -260,
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err = repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.KeepThreshold != 12.5 || got.ExcludeThreshold != -260 {
		t.Errorf("thresholds after Create = keep=%v exclude=%v, want 12.5/-260", got.KeepThreshold, got.ExcludeThreshold)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	foundGraded := false
	for _, mp := range list {
		if mp.ID == p.ID {
			foundGraded = true
			if mp.KeepThreshold != 12.5 || mp.ExcludeThreshold != -260 {
				t.Errorf("List thresholds = keep=%v exclude=%v, want 12.5/-260", mp.KeepThreshold, mp.ExcludeThreshold)
			}
		}
	}
	if !foundGraded {
		t.Error("graded profile missing from List")
	}

	p.KeepThreshold = 0
	p.ExcludeThreshold = 0
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err = repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if got.KeepThreshold != 0 || got.ExcludeThreshold != 0 {
		t.Errorf("thresholds after Update = keep=%v exclude=%v, want 0/0", got.KeepThreshold, got.ExcludeThreshold)
	}
}
