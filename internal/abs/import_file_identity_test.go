package abs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// fileIdentityFixture is a library holding one book whose file is tracked,
// and an ABS item for that same file under another spelling of the title.
type fileIdentityFixture struct {
	env      *covImpEnv
	owned    *models.Book
	item     NormalizedLibraryItem
	authorID int64
}

// newFileIdentityFixture seeds bookAuthor (owned by authorOwner) with
// "Chapterhouse: Dune" (owned by bookOwner) tracking the file in format, and
// an ABS item crediting itemAuthor and titled "Chapter House Dune" that points
// at the same file. Owner ids name real users; 0 means no owner.
func newFileIdentityFixture(t *testing.T, format, bookAuthor, itemAuthor string, authorOwner, bookOwner func(*covImpEnv) int64) fileIdentityFixture {
	t.Helper()
	ctx := context.Background()
	env := covImpNewEnv(t)
	storageRoot := t.TempDir()
	libraryDir := filepath.Join(storageRoot, "books")
	audiobookDir := filepath.Join(storageRoot, "audiobooks")
	env.importer.WithStoragePaths(libraryDir, audiobookDir, nil)

	author := &models.Author{ForeignID: "OL79034A", Name: bookAuthor, SortName: bookAuthor, MetadataProvider: "openlibrary", Monitored: true}
	if owner := authorOwner(env); owner != 0 {
		if err := env.authors.CreateForUser(ctx, author, owner); err != nil {
			t.Fatal(err)
		}
	} else if err := env.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	owned := &models.Book{
		ForeignID: "OL893415W", AuthorID: author.ID, OwnerUserID: bookOwner(env),
		Title: "Chapterhouse: Dune", SortTitle: "Chapterhouse: Dune",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true, MediaType: format, MetadataProvider: "openlibrary",
	}
	if err := env.books.Create(ctx, owned); err != nil {
		t.Fatal(err)
	}

	item := sampleABSItem()
	item.ItemID = "li-chapterhouse"
	item.Title = "Chapter House Dune"
	item.ASIN = ""
	item.Series = nil
	item.Authors = []NormalizedAuthor{{ID: "author-herbert", Name: itemAuthor}}
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
	if err := env.books.SetFormatFilePath(ctx, owned.ID, format, trackedPath); err != nil {
		t.Fatalf("track file: %v", err)
	}
	return fileIdentityFixture{env: env, owned: owned, item: item, authorID: author.ID}
}

func noOwner(*covImpEnv) int64 { return 0 }

// createUser adds a real user, so owner_user_id names a row that exists.
func createUser(t *testing.T, env *covImpEnv, name string) int64 {
	t.Helper()
	u, err := db.NewUserRepo(env.db).Create(context.Background(), name, "h-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// TestImporter_MatchesExistingBookByTrackedFile is #1691's owned and wanted
// twin. Bindery already has the book, owned, with the file on disk tracked,
// under the provider's title. The ABS item for that same file carries another
// spelling of the title, so neither the ABS keys (this item was never
// imported) nor the title match find it. The importer used to create a second
// row, which could not take the file (another book tracks it) and so stayed
// wanted beside the owned one: a standing order to download a book the user
// already has. The matched book keeps its own author and title.
func TestImporter_MatchesExistingBookByTrackedFile(t *testing.T) {
	t.Parallel()
	for _, format := range []string{models.MediaTypeEbook, models.MediaTypeAudiobook} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newFileIdentityFixture(t, format, "Frank Herbert", "Frank Herbert", noOwner, noOwner)

			runSingleABSImport(t, f.env.importer, f.item)

			books, err := f.env.books.ListIncludingExcluded(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				for _, b := range books {
					t.Logf("book %d %q status=%s", b.ID, b.Title, b.Status)
				}
				t.Fatalf("books = %d, want the ABS item matched to the book that already tracks its file", len(books))
			}
			got := books[0]
			if got.ID != f.owned.ID || got.Status != models.BookStatusImported {
				t.Errorf("book = id %d status %s, want id %d still imported", got.ID, got.Status, f.owned.ID)
			}
			if got.Title != "Chapterhouse: Dune" || got.AuthorID != f.authorID {
				t.Errorf("book = %q author %d, want its own title and author kept", got.Title, got.AuthorID)
			}
			links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.owned.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != 1 || links[0].ExternalID != f.item.ItemID {
				t.Errorf("provenance = %+v, want the ABS item recorded on the existing book", links)
			}
		})
	}
}

// TestImporter_FileMatchRequiresTheSameAuthor: a shared file does not let an
// item credited to Frank Herbert claim a book by Brian Herbert. The book must
// keep its author, title and provenance.
func TestImporter_FileMatchRequiresTheSameAuthor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Brian Herbert", "Frank Herbert", noOwner, noOwner)

	runSingleABSImport(t, f.env.importer, f.item)

	got, err := f.env.books.GetByID(ctx, f.owned.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.AuthorID != f.authorID || got.Title != "Chapterhouse: Dune" {
		t.Errorf("book = %q author %d, want it left with Brian Herbert (%d) and its title", got.Title, got.AuthorID, f.authorID)
	}
	links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("provenance = %+v, want the item not linked to another author's book", links)
	}
}

// TestImporter_FileMatchStaysWithinOneOwner: authors an ABS import creates or
// links have no owner, so the import must not reach a book that belongs to a
// user through a shared file. Two real users are seeded so the owner ids are
// genuine.
func TestImporter_FileMatchStaysWithinOneOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var bob int64
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Frank Herbert", "Frank Herbert", noOwner, func(env *covImpEnv) int64 {
		createUser(t, env, "alice")
		bob = createUser(t, env, "bob")
		return bob
	})

	runSingleABSImport(t, f.env.importer, f.item)

	links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("provenance = %+v, want bob's book (owner %d) left alone by an import with no owner", links, bob)
	}
	got, err := f.env.books.GetByID(ctx, f.owned.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Title != "Chapterhouse: Dune" || got.OwnerUserID != bob {
		t.Errorf("book = %q owner %d, want it unchanged", got.Title, got.OwnerUserID)
	}
}

// TestHardcoverAuthorScoreAcceptsDroppedInitial: the Hardcover series match
// adds this to a title score, so an author that only leaves out an initial
// scores as an auto match (#2881), and one whose initial disagrees does not.
func TestHardcoverAuthorScoreAcceptsDroppedInitial(t *testing.T) {
	if got := hardcoverAuthorScore("J. Rowling", "J.K. Rowling"); got != 20 {
		t.Errorf("hardcoverAuthorScore(dropped initial) = %d, want 20", got)
	}
	if got := hardcoverAuthorScore("J. R. Smith", "J. T. Smith"); got >= 20 {
		t.Errorf("hardcoverAuthorScore(conflicting initial) = %d, want below 20", got)
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
