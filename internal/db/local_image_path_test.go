package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestListWithLocalImagePath covers the two listers the #2564 startup repair
// runs over: only rows whose image_url is an absolute filesystem path come
// back, URLs and references do not, and SetImageURL takes a row out of the
// list once it has been rewritten.
func TestListWithLocalImagePath(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	editionRepo := NewEditionRepo(database)
	a := &models.Author{ForeignID: "calibre:author:1", Name: "A", SortName: "A", MetadataProvider: "calibre", Monitored: true}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	mk := func(fid, image string, excluded bool) *models.Book {
		b := &models.Book{
			ForeignID: fid, AuthorID: a.ID, Title: fid, SortTitle: fid, ImageURL: image,
			Status: "imported", Genres: []string{}, MetadataProvider: "calibre", Monitored: true,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if excluded {
			if err := bookRepo.SetExcluded(ctx, b.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	hostPath := mk("calibre:book:1", "/mnt/storage/calibre/A/Book (1)/cover.jpg", false)
	mk("calibre:book:2", "https://assets.hardcover.app/x.jpg", false)
	mk("calibre:book:3", "", false)
	mk("calibre:book:4", "bindery-cover:abc.jpg", false)
	excluded := mk("calibre:book:5", "/mnt/storage/calibre/A/Book (5)/cover.jpg", true)
	// A library imported on the Windows build stores a drive letter path.
	windows := mk("calibre:book:6", `C:\Calibre\A\Book (6)\cover.jpg`, false)

	books, err := bookRepo.ListWithLocalImagePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 3 || books[0].ID != hostPath.ID || books[1].ID != excluded.ID || books[2].ID != windows.ID {
		t.Fatalf("books with local image path = %+v, want ids %d, %d and %d", books, hostPath.ID, excluded.ID, windows.ID)
	}

	for i, image := range []string{"/lib/a/cover.jpg", "https://example.com/a.jpg", "", "bindery-cover:abc.jpg", `D:\lib\a\cover.jpg`} {
		e := &models.Edition{
			ForeignID: "calibre:edition:" + string(rune('a'+i)), BookID: hostPath.ID, Title: "E",
			Format: "EPUB", Language: "eng", ImageURL: image, IsEbook: true, Monitored: true,
		}
		if err := editionRepo.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	editions, err := editionRepo.ListWithLocalImagePath(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(editions) != 2 || editions[0].ImageURL != "/lib/a/cover.jpg" || editions[1].ImageURL != `D:\lib\a\cover.jpg` {
		t.Fatalf("editions with local image path = %+v, want the two host path rows", editions)
	}
	if err := editionRepo.SetImageURL(ctx, editions[1].ID, "bindery-cover:def.jpg"); err != nil {
		t.Fatal(err)
	}

	if err := editionRepo.SetImageURL(ctx, editions[0].ID, "bindery-cover:def.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := bookRepo.SetImageURL(ctx, hostPath.ID, "bindery-cover:def.jpg"); err != nil {
		t.Fatal(err)
	}
	editions, _ = editionRepo.ListWithLocalImagePath(ctx)
	books, _ = bookRepo.ListWithLocalImagePath(ctx)
	if len(editions) != 0 || len(books) != 2 {
		t.Fatalf("after rewrite: %d editions, %d books still listed", len(editions), len(books))
	}
	got, _ := bookRepo.GetByID(ctx, hostPath.ID)
	if got.ImageURL != "bindery-cover:def.jpg" {
		t.Errorf("book image_url = %q after SetImageURL", got.ImageURL)
	}
}
