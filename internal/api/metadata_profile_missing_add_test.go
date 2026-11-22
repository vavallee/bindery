package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The reported repro (#4132): Add Book for an author not in the library, with
// the seeded profile 1 deleted, used to create the author on profile id 1.
func TestMissingMetadataProfile_AddBookAndAddAuthorStampAnExistingProfile(t *testing.T) {
	ctx := context.Background()
	deleteSeeded := func(t *testing.T, f coreFixture) int64 {
		t.Helper()
		p := &models.MetadataProfile{Name: "Deutsch", AllowedLanguages: "ger", UnknownLanguageBehavior: models.UnknownLanguagePass}
		if err := f.profiles.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := f.profiles.Delete(ctx, models.DefaultMetadataProfileID); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	t.Run("add book", func(t *testing.T) {
		f := newCoreFixture(t, addBookBackCatalogueStub(false))
		want := deleteSeeded(t, f)
		res, err := f.h.addBookCore(ctx, wellsAddBookParams)
		if err != nil {
			t.Fatal(err)
		}
		f.drain(t)
		stored, err := f.authors.GetByID(ctx, res.Author.ID)
		if err != nil || stored == nil || stored.MetadataProfileID == nil || *stored.MetadataProfileID != want {
			t.Fatalf("author profile = %v err=%v, want %d", stored, err, want)
		}
	})

	t.Run("add author", func(t *testing.T) {
		f := newCoreFixture(t, wellsCreateProvider())
		want := deleteSeeded(t, f)
		res, err := f.h.createAuthorCore(ctx, wellsCreateParams)
		if err != nil {
			t.Fatal(err)
		}
		f.drain(t)
		stored, err := f.authors.GetByID(ctx, res.Author.ID)
		if err != nil || stored == nil || stored.MetadataProfileID == nil || *stored.MetadataProfileID != want {
			t.Fatalf("author profile = %v err=%v, want %d", stored, err, want)
		}
	})
}
