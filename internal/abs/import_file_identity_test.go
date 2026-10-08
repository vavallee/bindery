package abs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_MatchesExistingBookByTrackedFile is #1691's owned and wanted
// twin. Bindery already has the book, owned, with the file on disk tracked,
// under the provider's title. The ABS item for that same file carries another
// spelling of the title, so neither the ABS keys (this item was never
// imported) nor the title match find it. The importer used to create a second
// row, which could not take the file (another book tracks it) and so stayed
// wanted beside the owned one: a standing order to download a book the user
// already has.
func TestImporter_MatchesExistingBookByTrackedFile(t *testing.T) {
	t.Parallel()
	for _, format := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			importer, authorRepo, bookRepo, _, _, provenanceRepo, _, _, _, _ := newABSImporterFixture(t)
			storageRoot := t.TempDir()
			libraryDir := filepath.Join(storageRoot, "books")
			audiobookDir := filepath.Join(storageRoot, "audiobooks")
			importer.WithStoragePaths(libraryDir, audiobookDir, nil)

			author := &models.Author{ForeignID: "OL79034A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary", Monitored: true}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			owned := &models.Book{
				ForeignID: "OL893415W", AuthorID: author.ID, Title: "Chapterhouse: Dune", SortTitle: "Chapterhouse: Dune",
				Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: format, MetadataProvider: "openlibrary",
			}
			if err := bookRepo.Create(ctx, owned); err != nil {
				t.Fatal(err)
			}

			item := sampleABSItem()
			item.ItemID = "li-chapterhouse"
			item.Title = "Chapter House Dune"
			item.ASIN = ""
			item.Series = nil
			item.Authors = []NormalizedAuthor{{ID: "author-herbert", Name: "Frank Herbert"}}
			var trackedPath string
			if format == models.MediaTypeEbook {
				trackedPath = filepath.Join(libraryDir, "Frank Herbert", "Chapterhouse Dune.epub")
				mustWriteFile(t, trackedPath)
				item.EbookPath = trackedPath
				item.Path = ""
				item.AudioFiles = nil
			} else {
				trackedPath = filepath.Join(audiobookDir, "Frank Herbert", "Chapterhouse Dune")
				mustWriteFile(t, filepath.Join(trackedPath, "part1.m4b"))
				item.EbookPath = ""
				item.Path = trackedPath
				item.AudioFiles = []NormalizedAudioFile{{INO: "audio-1", Path: filepath.Join(trackedPath, "part1.m4b")}}
			}
			if err := bookRepo.SetFormatFilePath(ctx, owned.ID, format, trackedPath); err != nil {
				t.Fatalf("track file: %v", err)
			}

			runSingleABSImport(t, importer, item)

			books, err := bookRepo.ListIncludingExcluded(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				for _, b := range books {
					t.Logf("book %d %q status=%s", b.ID, b.Title, b.Status)
				}
				t.Fatalf("books = %d, want the ABS item matched to the book that already tracks its file", len(books))
			}
			if books[0].ID != owned.ID || books[0].Status != models.BookStatusImported {
				t.Errorf("book = id %d status %s, want id %d still imported", books[0].ID, books[0].Status, owned.ID)
			}
			links, err := provenanceRepo.ListByLocal(ctx, entityTypeBook, owned.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != 1 || links[0].ExternalID != item.ItemID {
				t.Errorf("provenance = %+v, want the ABS item recorded on the existing book", links)
			}
		})
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
