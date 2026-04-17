package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestEditionRepo_UpsertInsertAndUpdate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-ED-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-ED-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)

	isbn13 := "9780000000001"
	ed := &models.Edition{
		ForeignID: "calibre:1:EPUB",
		BookID:    b.ID,
		Title:     "First Edition",
		ISBN13:    &isbn13,
		Format:    "EPUB",
		Language:  "eng",
		ImageURL:  "http://img/1.jpg",
		IsEbook:   true,
		Monitored: true,
	}
	if err := repo.Upsert(ctx, ed); err != nil {
		t.Fatalf("insert upsert: %v", err)
	}
	if ed.ID == 0 {
		t.Fatal("expected non-zero ID after insert")
	}
	firstID := ed.ID

	got, err := repo.GetByForeignID(ctx, "calibre:1:EPUB")
	if err != nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if got == nil {
		t.Fatal("expected edition, got nil")
	}
	if got.Title != "First Edition" || !got.IsEbook || !got.Monitored {
		t.Errorf("unexpected round-trip: %+v", got)
	}
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 {
		t.Errorf("ISBN13 mismatch: %v", got.ISBN13)
	}

	// Second upsert updates the row in place; ImageURL empty should preserve
	// the original via COALESCE(NULLIF(...)), ISBN13 omitted should survive
	// via COALESCE.
	ed2 := &models.Edition{
		ForeignID: "calibre:1:EPUB",
		BookID:    b.ID,
		Title:     "Updated Edition",
		Format:    "MOBI",
		Language:  "fra",
		ImageURL:  "",
		IsEbook:   true,
	}
	if err := repo.Upsert(ctx, ed2); err != nil {
		t.Fatalf("update upsert: %v", err)
	}
	if ed2.ID != firstID {
		t.Errorf("upsert must preserve id: want %d, got %d", firstID, ed2.ID)
	}

	got, _ = repo.GetByForeignID(ctx, "calibre:1:EPUB")
	if got.Title != "Updated Edition" {
		t.Errorf("title not updated: %q", got.Title)
	}
	if got.Format != "MOBI" {
		t.Errorf("format not updated: %q", got.Format)
	}
	if got.ImageURL != "http://img/1.jpg" {
		t.Errorf("image_url should be preserved via COALESCE(NULLIF), got %q", got.ImageURL)
	}
	if got.ISBN13 == nil || *got.ISBN13 != isbn13 {
		t.Errorf("ISBN13 should survive update, got %v", got.ISBN13)
	}
}

func TestEditionRepo_GetByForeignIDNotFound(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewEditionRepo(database)

	got, err := repo.GetByForeignID(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil edition, got %+v", got)
	}
}

func TestEditionRepo_ListByBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := &models.Author{ForeignID: "OL-LB-A", Name: "A", SortName: "A", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-LB-B", AuthorID: a.ID, Title: "Book", SortTitle: "Book",
		Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	repo := NewEditionRepo(database)

	list, err := repo.ListByBook(ctx, b.ID)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want 0, got %d", len(list))
	}

	for _, fid := range []string{"calibre:2:EPUB", "calibre:2:PDF"} {
		if err := repo.Upsert(ctx, &models.Edition{
			ForeignID: fid, BookID: b.ID, Title: "t", Format: "x", Language: "eng", IsEbook: true,
		}); err != nil {
			t.Fatalf("upsert %s: %v", fid, err)
		}
	}

	list, err = repo.ListByBook(ctx, b.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("want 2 editions, got %d", len(list))
	}

	other, err := repo.ListByBook(ctx, 99999)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("unrelated book should have 0 editions, got %d", len(other))
	}
}
