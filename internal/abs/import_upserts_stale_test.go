package abs

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The author matcher is loaded once per import, so its copy of an author is
// as old as the run. Resolving a manual author must not write that copy back
// over an edit saved since (#3407).
func TestResolveManualAuthorKeepsEditsSavedDuringImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := covImpConfig("lib-books")

	newAuthor := func(t *testing.T, env *covImpEnv) *models.Author {
		t.Helper()
		a := &models.Author{ForeignID: absForeignID("author", "lib-books", "x"), Name: "Kari Nordmann", SortName: "Nordmann, Kari", MetadataProvider: providerAudiobookshelf, Monitored: true}
		if err := env.authors.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	matcherFor := func(t *testing.T, env *covImpEnv) *authorMatcher {
		t.Helper()
		m, err := env.importer.newAuthorMatcher(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("keeps a bio saved after the matcher loaded", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		a := newAuthor(t, env)
		matcher := matcherFor(t, env)

		saved, _ := env.authors.GetByID(ctx, a.ID)
		saved.Description = "Skriver om Fjellvinden."
		saved.DescriptionSource = &models.ProfileSource{Provider: "user"}
		if ok, err := env.authors.UpdateIfUnchanged(ctx, saved, saved.UpdatedAtRaw); err != nil || !ok {
			t.Fatalf("save bio = %v %v", ok, err)
		}

		got, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-UP", "Kari Nordmann"), matcher)
		if err != nil || got.ID != a.ID {
			t.Fatalf("resolve = %+v %v", got, err)
		}
		stored, _ := env.authors.GetByID(ctx, a.ID)
		if stored.ForeignID != "OL-UP" {
			t.Fatalf("foreign id = %q, want OL-UP", stored.ForeignID)
		}
		if stored.Description != "Skriver om Fjellvinden." || stored.DescriptionSource == nil || stored.DescriptionSource.Provider != "user" {
			t.Fatalf("bio = %q %+v, want the saved bio and its credit kept", stored.Description, stored.DescriptionSource)
		}
		if got.Description != stored.Description {
			t.Fatalf("returned author bio = %q, want the stored row, not the matcher's copy", got.Description)
		}
	})

	t.Run("leaves an author another write already re-identified", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		a := newAuthor(t, env)
		matcher := matcherFor(t, env)

		moved, _ := env.authors.GetByID(ctx, a.ID)
		moved.ForeignID = "OL-OTHER"
		moved.MetadataProvider = models.AuthorProviderFromForeignID("OL-OTHER")
		if err := env.authors.Update(ctx, moved); err != nil {
			t.Fatal(err)
		}

		got, _, _, _, err := env.importer.resolveManualAuthor(ctx, cfg, 0, covImpManualItem("OL-UP", "Kari Nordmann"), matcher)
		if err != nil || got.ID != a.ID {
			t.Fatalf("resolve = %+v %v", got, err)
		}
		if stored, _ := env.authors.GetByID(ctx, a.ID); stored.ForeignID != "OL-OTHER" {
			t.Fatalf("foreign id = %q, want OL-OTHER left alone", stored.ForeignID)
		}
	})

	// resolveManualAuthor binds the identifier first, which already fails for
	// a deleted author, so the guard is reached on its own here.
	t.Run("fails when the author was deleted", func(t *testing.T) {
		t.Parallel()
		env := covImpNewEnv(t)
		a := newAuthor(t, env)
		env.exec(t, "DELETE FROM authors WHERE id = ?", a.ID)

		_, err := env.importer.replaceAuthorIdentity(ctx, a.ID, "OL-UP")
		if err == nil || !strings.Contains(err.Error(), "deleted during the import") {
			t.Fatalf("err = %v, want the deleted author reported", err)
		}
	})
}
