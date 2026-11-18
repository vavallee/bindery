package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// SQLite's last_insert_rowid() belongs to the connection and a conflict
// update does not reset it, so LastInsertId after an upsert that updated can
// be the id of the previous insert on that connection (#3978). These tests
// run on the single connection OpenMemory gives and interleave a second
// insert before re-upserting the first row.

func TestEditionRepo_UpsertReturnsOwnIDAfterOtherInsert(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	a := &models.Author{ForeignID: "OL-3978-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-3978-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := NewBookRepo(database).Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)
	edA := &models.Edition{ForeignID: "abs:edition:lib:a:ebook", BookID: b.ID, Title: "A", Format: "EBOOK", Monitored: true}
	if err := repo.Upsert(ctx, edA); err != nil {
		t.Fatal(err)
	}
	edB := &models.Edition{ForeignID: "abs:edition:lib:b:ebook", BookID: b.ID, Title: "B", Format: "EBOOK", Monitored: true}
	if err := repo.Upsert(ctx, edB); err != nil {
		t.Fatal(err)
	}
	if edA.ID == edB.ID {
		t.Fatalf("fixture: editions share id %d", edA.ID)
	}
	wantA := edA.ID

	again := &models.Edition{ForeignID: edA.ForeignID, BookID: b.ID, Title: "A renamed", Format: "EBOOK", Monitored: true}
	if err := repo.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != wantA {
		t.Fatalf("re-upsert of edition A returned id %d, want %d (B is %d)", again.ID, wantA, edB.ID)
	}
}

func TestABSImportRunEntityRepo_RecordReturnsOwnIDAfterOtherInsert(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	run := &models.ABSImportRun{LibraryID: "lib", Status: "running"}
	if err := NewABSImportRunRepo(database).Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	repo := NewABSImportRunEntityRepo(database)
	mk := func(ext string) *models.ABSImportRunEntity {
		return &models.ABSImportRunEntity{RunID: run.ID, LibraryID: "lib", EntityType: "book", ExternalID: ext, LocalID: 7, Outcome: "created"}
	}
	first := mk("one")
	if err := repo.Record(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, mk("two")); err != nil {
		t.Fatal(err)
	}
	again := mk("one")
	again.Outcome = "updated"
	if err := repo.Record(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("re-recorded entity got id %d, want %d", again.ID, first.ID)
	}
}

func TestCalibreEntitySnapshotRepo_RecordReturnsOwnIDAfterOtherInsert(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	run := &models.CalibreImportRun{LibraryPath: "/lib", Status: "running"}
	if err := NewCalibreImportRunRepo(database).Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	repo := NewCalibreEntitySnapshotRepo(database)
	mk := func(ext string) *models.CalibreEntitySnapshot {
		return &models.CalibreEntitySnapshot{RunID: run.ID, SourceID: "default", EntityType: "book", ExternalID: ext, LocalID: 7, Outcome: "created"}
	}
	first := mk("one")
	if err := repo.Record(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, mk("two")); err != nil {
		t.Fatal(err)
	}
	again := mk("one")
	again.Outcome = "updated"
	if err := repo.Record(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("re-recorded snapshot got id %d, want %d", again.ID, first.ID)
	}
}
