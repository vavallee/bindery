package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A credited photo or bio from an enriched upstream (#3287) follows the
// refresh merge rule on an ABS import, never the conflict planner: it fills
// an empty field with its credit, never displaces the library's own value,
// and raises no conflict.
func TestPlanUpstreamAuthorMergeCreditedFields(t *testing.T) {
	env := covImpNewEnv(t)
	ctx := context.Background()
	matcher, err := env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wiki := &models.ProfileSource{Provider: "wikipedia", URL: "https://no.wikipedia.example/k", License: "CC BY-SA 4.0"}
	commons := &models.ProfileSource{Provider: "wikimedia-commons", URL: "https://commons.example/File:K.jpg", License: "CC BY 2.5"}
	full := &models.Author{Name: "Kari Nordmann", Description: "Kari Nordmann is a writer.", DescriptionSource: wiki,
		ImageURL: "https://commons.example/k.jpg", ImageSource: commons}
	item := NormalizedLibraryItem{ItemID: "li_1", LibraryID: "lib_1", Authors: []NormalizedAuthor{{Name: "Kari Nordmann"}}}

	// Empty fields: filled, credited, no conflict.
	author := &models.Author{ID: 1, Name: "Kari Nordmann"}
	plan, err := env.importer.planUpstreamAuthorMerge(ctx, ImportConfig{}, item, author, full, matcher)
	if err != nil {
		t.Fatal(err)
	}
	if author.Description != full.Description || author.DescriptionSource != wiki || author.ImageSource != commons || !plan.write {
		t.Errorf("empty fields: %+v, write=%v", author, plan.write)
	}
	if len(plan.conflicts) != 0 || plan.result.Conflicts != 0 {
		t.Errorf("credited values raised conflicts: %+v", plan.conflicts)
	}

	// The library's own bio stays, with no credit put on it.
	author = &models.Author{ID: 1, Name: "Kari Nordmann", Description: "From the library."}
	if plan, err = env.importer.planUpstreamAuthorMerge(ctx, ImportConfig{}, item, author, full, matcher); err != nil {
		t.Fatal(err)
	}
	if author.Description != "From the library." || author.DescriptionSource != nil || len(plan.conflicts) != 0 {
		t.Errorf("own bio: %q from %+v, conflicts %+v", author.Description, author.DescriptionSource, plan.conflicts)
	}

	// A primary value on both sides is still a conflict.
	author = &models.Author{ID: 1, Name: "Kari Nordmann", Description: "From the library."}
	primary := &models.Author{Name: "Kari Nordmann", Description: "From the provider."}
	if plan, err = env.importer.planUpstreamAuthorMerge(ctx, ImportConfig{}, item, author, primary, matcher); err != nil {
		t.Fatal(err)
	}
	if plan.result.Conflicts != 1 {
		t.Errorf("uncredited values: %d conflicts, want 1", plan.result.Conflicts)
	}
}

// A conflict choice that changes a photo or bio drops the old credit; one
// that keeps the value keeps it.
func TestApplyAuthorConflictValueCredit(t *testing.T) {
	wiki := &models.ProfileSource{Provider: "wikipedia"}
	author := &models.Author{Description: "Wiki bio.", DescriptionSource: wiki, ImageURL: "https://a/k.jpg", ImageSource: wiki}
	if err := ApplyAuthorConflictValue(author, "description", "Wiki bio."); err != nil || author.DescriptionSource != wiki {
		t.Errorf("same value: credit %+v, %v", author.DescriptionSource, err)
	}
	if err := ApplyAuthorConflictValue(author, "description", "From the library."); err != nil || author.DescriptionSource != nil {
		t.Errorf("changed value kept credit %+v, %v", author.DescriptionSource, err)
	}
	if err := ApplyAuthorConflictValue(author, "image_url", "https://b/k.jpg"); err != nil || author.ImageSource != nil || author.ImageURL != "https://b/k.jpg" {
		t.Errorf("changed photo: %q from %+v, %v", author.ImageURL, author.ImageSource, err)
	}
}
