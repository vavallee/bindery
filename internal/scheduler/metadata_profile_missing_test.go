package scheduler

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// #4132: automatic search used to apply no language filter at all to an author
// whose metadata profile had been deleted. It now filters by the profile the
// shared resolver picks, the same one import, sync and reconcile use.
func TestResolveAllowedLanguages_MissingProfileUsesTheResolvedOne(t *testing.T) {
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

	s := &Scheduler{profiles: profiles}
	got := s.resolveAllowedLanguages(ctx, author)
	if len(got) != 1 || got[0] != "ger" {
		t.Fatalf("languages = %v, want [ger]", got)
	}
}

// A failing lookup filters nothing, and so does an unwired repo: grabbing
// nothing because a profile could not be read would be worse.
func TestResolveAllowedLanguages_LookupFailureFiltersNothing(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("ALTER TABLE metadata_profiles RENAME TO metadata_profiles_x"); err != nil {
		t.Fatal(err)
	}
	id := int64(1)
	author := &models.Author{ID: 1, MetadataProfileID: &id}
	s := &Scheduler{profiles: db.NewMetadataProfileRepo(database)}
	if got := s.resolveAllowedLanguages(context.Background(), author); len(got) != 0 {
		t.Fatalf("languages on a lookup failure = %v, want none", got)
	}
	if got := (&Scheduler{}).resolveAllowedLanguages(context.Background(), author); got != nil {
		t.Fatalf("languages without a repo = %v, want nil", got)
	}
}
