package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #4132: an import used to fall back to search.preferredLanguage for an author
// whose metadata profile had been deleted, while search applied no filter. It
// now holds the file to the profile the shared resolver picks.
func TestImportAllowedLanguages_MissingProfileUsesTheResolvedOne(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	profiles := db.NewMetadataProfileRepo(database)
	deutsch := &models.MetadataProfile{Name: "Deutsch", AllowedLanguages: "ger"}
	if err := profiles.Create(ctx, deutsch); err != nil {
		t.Fatal(err)
	}
	if err := profiles.Delete(ctx, models.DefaultMetadataProfileID); err != nil {
		t.Fatal(err)
	}
	gone := models.DefaultMetadataProfileID
	author := &models.Author{ID: 1, MetadataProfileID: &gone}

	s := &Scanner{metadataProfiles: profiles}
	langs, source := s.importAllowedLanguages(ctx, author)
	if len(langs) != 1 || langs[0] != "ger" || source != `the metadata profile "Deutsch"` {
		t.Fatalf("langs = %v source = %q, want [ger] from Deutsch", langs, source)
	}
}

// A failing profile lookup must not block an import: the file is held to the
// preferred language when that is set, and to nothing otherwise.
func TestImportAllowedLanguages_LookupFailureFallsThrough(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := database.Exec("ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x"); err != nil {
		t.Fatal(err)
	}
	id := int64(1)
	author := &models.Author{ID: 1, MetadataProfileID: &id}
	settings := db.NewSettingsRepo(database)
	s := &Scanner{metadataProfiles: db.NewMetadataProfileRepo(database), settings: settings}

	if langs, source := s.importAllowedLanguages(ctx, author); langs != nil || source != "" {
		t.Fatalf("no preferred language: %v %q, want no restriction", langs, source)
	}
	if err := settings.Set(ctx, "search.preferredLanguage", "en"); err != nil {
		t.Fatal(err)
	}
	langs, source := s.importAllowedLanguages(ctx, author)
	if len(langs) != 1 || langs[0] != "eng" || source != "the preferred search language" {
		t.Fatalf("preferred language: %v %q, want [eng]", langs, source)
	}
}
