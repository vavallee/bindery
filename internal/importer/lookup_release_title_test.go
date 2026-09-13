package importer

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// releaseTitleFixture seeds a catalogue and returns a scanner over it. No files
// on disk: the tier under test reads the release name and nothing else.
func releaseTitleFixture(t *testing.T, seed func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo)) *Scanner {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	seed(context.Background(), books, authors)

	return NewScanner(
		db.NewDownloadRepo(database),
		db.NewDownloadClientRepo(database),
		books,
		authors,
		db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	)
}

func seedBook(t *testing.T, ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo, authorName, title string) *models.Book {
	t.Helper()
	a := &models.Author{ForeignID: "OL-" + authorName, Name: authorName, SortName: authorName}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OL-" + title, AuthorID: a.ID, Title: title, SortTitle: title,
		Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{},
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// The reported case (#2470). An audiobook grabbed from the free-text Search
// page: no BookID, no EPUB to read metadata from, and track filenames that say
// nothing. The release name is the only thing that identifies it, and it says
// exactly which book it is.
func TestMatchBookForDownload_MatchesOnReleaseTitle(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Amy Tan", "The Kitchen God's Wife")
		seedBook(t, ctx, books, authors, "Mike Carey", "The Girl with All the Gifts")
	})

	files := []string{"/dl/01 - Chapter One.mp3", "/dl/02 - Chapter Two.mp3"}
	book, author := s.matchBookForDownload(context.Background(), files,
		"The Kitchen God's Wife by Amy Tan [ENG / MP3]")

	if book == nil {
		t.Fatal("no match; the release name identifies the book unambiguously (#2470)")
	}
	if book.Title != "The Kitchen God's Wife" {
		t.Errorf("matched %q, want The Kitchen God's Wife", book.Title)
	}
	if author == nil || author.Name != "Amy Tan" {
		t.Errorf("author = %+v, want Amy Tan", author)
	}
}

// titleMatch is generous, so a one-word title matches almost any release
// containing that word. The author is what separates a real match from an
// accidental one, and here only the accidental candidate is in the library, so
// the "single confident match" rule cannot save it.
func TestMatchBookForDownload_ReleaseTitleNeedsTheAuthorToAgree(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		// Titles that titleMatch accepts against the release below, by authors
		// the release never names.
		seedBook(t, ctx, books, authors, "Marilynne Robinson", "Wife")
	})

	book, _ := s.matchBookForDownload(context.Background(), nil,
		"The Kitchen God's Wife by Amy Tan [ENG / MP3]")

	if book != nil {
		t.Fatalf("matched %q by a different author on a title-word collision; an unmatched download is recoverable by hand, a wrong import is not", book.Title)
	}
}

// Two books whose titles both match and whose authors are both named would be
// a guess. The tier declines rather than picking one, same rule as every other
// tier in this matcher.
func TestMatchBookForDownload_ReleaseTitleDeclinesAmbiguity(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Amy Tan", "The Kitchen God's Wife")
		// A second edition row under the same author with the same title, the
		// shape a duplicate import leaves behind.
		a2 := &models.Author{ForeignID: "OL-Amy Tan 2", Name: "Amy Tan", SortName: "Amy Tan"}
		if err := authors.Create(ctx, a2); err != nil {
			t.Fatal(err)
		}
		b2 := &models.Book{
			ForeignID: "OL-dup", AuthorID: a2.ID, Title: "The Kitchen God's Wife", SortTitle: "kitchen gods wife",
			Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{},
		}
		if err := books.Create(ctx, b2); err != nil {
			t.Fatal(err)
		}
	})

	book, _ := s.matchBookForDownload(context.Background(), nil,
		"The Kitchen God's Wife by Amy Tan [ENG / MP3]")

	if book != nil {
		t.Errorf("picked %q (id %d) between two equally good candidates; the matcher must never guess", book.Title, book.ID)
	}
}

// An empty release name must not match the whole catalogue, or the first book
// in the list becomes the answer to every unmatched download.
func TestMatchBookForDownload_EmptyReleaseTitleMatchesNothing(t *testing.T) {
	s := releaseTitleFixture(t, func(ctx context.Context, books *db.BookRepo, authors *db.AuthorRepo) {
		seedBook(t, ctx, books, authors, "Amy Tan", "The Kitchen God's Wife")
	})

	if book, _ := s.matchBookForDownload(context.Background(), nil, ""); book != nil {
		t.Errorf("empty release name matched %q", book.Title)
	}
	if book, _ := s.matchBookForDownload(context.Background(), nil, "   "); book != nil {
		t.Errorf("blank release name matched %q", book.Title)
	}
}

// An author whose name carries no significant tokens cannot corroborate
// anything. Answered as "no match" rather than "author agrees", so a download
// falls to manual matching instead of being imported on the title alone.
func TestReleaseNamesAuthor_NoSignificantTokensIsRefusal(t *testing.T) {
	tokens := map[string]bool{"kitchen": true, "gods": true, "wife": true, "amy": true, "tan": true}
	for _, name := range []string{"", "   ", "X"} {
		if releaseNamesAuthor(tokens, name) {
			t.Errorf("releaseNamesAuthor(%q) = true; an author that cannot be checked must not count as checked", name)
		}
	}
	if !releaseNamesAuthor(tokens, "Amy Tan") {
		t.Error("releaseNamesAuthor(\"Amy Tan\") = false; the release names that author")
	}
	if releaseNamesAuthor(tokens, "Mike Carey") {
		t.Error("releaseNamesAuthor(\"Mike Carey\") = true; the release does not name that author")
	}
	// Order and punctuation are the release's business, not ours.
	if !releaseNamesAuthor(tokens, "Tan, Amy") {
		t.Error("an inverted author name should still corroborate")
	}
}
