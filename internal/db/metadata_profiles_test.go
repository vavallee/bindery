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
