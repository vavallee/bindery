package db

import (
	"context"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestEditionNarratorsRoundTripAndBackCompat(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	a := &models.Author{ForeignID: "OL-NARR-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "OL-NARR-B", AuthorID: a.ID, Title: "B", SortTitle: "B", Status: "wanted",
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, Narrator: "X Y, Z W"}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	repo := NewEditionRepo(database)

	// A row written without the new fields reads as unknown, and the book's
	// string still yields the effective list.
	old := &models.Edition{ForeignID: "abs:old", BookID: b.ID, Title: "B", Format: "M4B", Monitored: true}
	if err := repo.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, old.ID)
	if len(got.Narrators) != 0 || got.EditionType != "" {
		t.Fatalf("legacy row: %+v", got)
	}
	if eff := models.EffectiveNarrators(got.Narrators, b.Narrator); !reflect.DeepEqual(eff, []string{"X Y", "Z W"}) {
		t.Fatalf("effective: %#v", eff)
	}

	// Writing stores the list and type; a later write that knows nothing
	// keeps them.
	ed := &models.Edition{ForeignID: "abs:new", BookID: b.ID, Title: "B", Format: "M4B", Monitored: true,
		Narrators: []string{"X Y", "Z W"}, EditionType: "unabridged"}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatal(err)
	}
	again := &models.Edition{ForeignID: "abs:new", BookID: b.ID, Title: "B", Format: "M4B", Monitored: true, EditionType: "bogus"}
	if err := repo.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, ed.ID)
	if !reflect.DeepEqual(got.Narrators, []string{"X Y", "Z W"}) || got.EditionType != "unabridged" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestQualityProfileNarratorPreferenceRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewQualityProfileRepo(database)
	p := &models.QualityProfile{Name: "Audio", Cutoff: "m4b", Items: []models.QualityItem{{Quality: "m4b", Allowed: true}}}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(ctx, p.ID)
	if len(got.PreferredNarrators) != 0 || got.EditionPreference != "" {
		t.Fatalf("default must be off: %+v", got)
	}
	got.PreferredNarrators = []string{"Scott Brick", "scott brick", "Simon Vance"}
	got.EditionPreference = models.EditionPrefRequireUnabridged
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, p.ID)
	if !reflect.DeepEqual(got.PreferredNarrators, []string{"Scott Brick", "Simon Vance"}) || got.EditionPreference != models.EditionPrefRequireUnabridged {
		t.Fatalf("round trip: %+v", got)
	}
}
