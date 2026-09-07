package hardcoverlistsyncer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
)

// A complementary audiobook list widening a book the user already owns as an
// ebook creates a genuine gap: the audiobook slot becomes monitored with
// nothing behind it. The row must leave 'imported', or the wanted page, the
// scheduled sweep and the author bulk search all skip it forever and the
// format this sync just asked for is never searched (#1634).
func TestSyncOne_WideningAnImportedBookReopensItAsWanted(t *testing.T) {
	s, repo := newTestSyncer(t)
	ctx := context.Background()

	ebookList := testImportList("Ebooks", "hardcover", true)
	ebookList.URL = "ebooks"
	ebookList.MediaType = models.MediaTypeEbook
	if err := repo.Create(ctx, &ebookList); err != nil {
		t.Fatalf("seed ebook list: %v", err)
	}
	audiobookList := testImportList("Audiobooks", "hardcover", true)
	audiobookList.URL = "audiobooks"
	audiobookList.MediaType = models.MediaTypeAudiobook
	if err := repo.Create(ctx, &audiobookList); err != nil {
		t.Fatalf("seed audiobook list: %v", err)
	}

	s.WithClientFactory(func(string) hardcoverClient {
		return &fakeHardcoverClient{
			lists: []hardcover.HCList{
				{ID: 1, Slug: ebookList.URL, Name: ebookList.Name},
				{ID: 2, Slug: audiobookList.URL, Name: audiobookList.Name},
			},
			books: []models.Book{{
				ForeignID:        "hc:owned-ebook",
				Title:            "Owned Ebook",
				MetadataProvider: "hardcover",
				MediaType:        models.MediaTypeBoth,
				Author: &models.Author{
					ForeignID:        "hc:owned-author",
					Name:             "Owned Author",
					MetadataProvider: "hardcover",
				},
			}},
		}
	})

	if err := s.SyncOne(ctx, ebookList.ID); err != nil {
		t.Fatalf("sync ebook list: %v", err)
	}
	tracked, err := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if err != nil || tracked == nil {
		t.Fatalf("book not created: %v", err)
	}

	// The user then imports the ebook. AddBookFile derives the status, so the
	// book is legitimately 'imported' for its declared single format.
	if err := s.books.AddBookFile(ctx, tracked.ID, models.MediaTypeEbook, "/library/owned.epub"); err != nil {
		t.Fatalf("import the ebook: %v", err)
	}
	before, _ := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if before.Status != models.BookStatusImported || before.MediaType != models.MediaTypeEbook {
		t.Fatalf("precondition: book = %q/%q, want imported ebook", before.Status, before.MediaType)
	}

	// The complementary audiobook list now widens it to 'both'.
	if err := s.SyncOne(ctx, audiobookList.ID); err != nil {
		t.Fatalf("sync audiobook list: %v", err)
	}

	got, err := s.books.GetByForeignID(ctx, "hc:owned-ebook")
	if err != nil || got == nil {
		t.Fatalf("widened book not found: %v", err)
	}
	if got.MediaType != models.MediaTypeBoth {
		t.Fatalf("mediaType = %q, want both", got.MediaType)
	}
	if got.AudiobookFilePath != "" {
		t.Fatalf("audiobook path = %q, want empty: nothing imported it", got.AudiobookFilePath)
	}
	if got.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want wanted: the audiobook slot is monitored and empty", got.Status)
	}
}
