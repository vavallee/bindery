package abs

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func creditedItemAuthors(names ...string) []NormalizedAuthor {
	out := make([]NormalizedAuthor, 0, len(names))
	for idx, n := range names {
		out = append(out, NormalizedAuthor{ID: "credited-" + string(rune('a'+idx)), Name: n})
	}
	return out
}

// TestConflictingTrackedBook_CreditedAuthors is #4189 point 2. The file is
// tracked on a book filed under bookAuthor; the item credits itemAuthors in
// order. Any credited author that is the same person as bookAuthor (exact
// normalised name, never fuzzy) means the item is the same book, not a
// conflict.
func TestConflictingTrackedBook_CreditedAuthors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		bookAuthor   string
		itemAuthors  []string
		wantConflict bool
	}{
		{"phoenix project, second of three", "Kevin Behr", []string{"Gene Kim", "Kevin Behr", "George Spafford"}, false},
		{"copper gauntlet, last of two", "Cassandra Clare", []string{"Holly Black", "Cassandra Clare"}, false},
		{"anthology, middle of eight", "Chris Wraight", []string{"Anthony Reynolds", "Chris Wraight", "Nick Kyme", "Graham McNeill", "Dan Abnett", "Aaron Dembski-Bowden", "John French", "Guy Haley"}, false},
		{"anthology, last of eight", "Guy Haley", []string{"Anthony Reynolds", "Chris Wraight", "Nick Kyme", "Graham McNeill", "Dan Abnett", "Aaron Dembski-Bowden", "John French", "Guy Haley"}, false},
		{"credited spelling normalises exactly", "J. K. Rowling", []string{"Robert Galbraith", "J.K. Rowling"}, false},
		{"different authors entirely", "Frank Herbert", []string{"Holly Black", "Cassandra Clare"}, true},
		{"near name, first credited", "Heather Gray", []string{"Heather Graham", "Someone Else"}, true},
		{"near name, later credited", "Heather Graham", []string{"Someone Else", "Heather Gray"}, true},
		{"substring of a credited author", "Gene Kim", []string{"Holly Black", "Gene Kimball"}, true},
		{"credited author inside a longer book author", "Gene Kimball", []string{"Holly Black", "Gene Kim"}, true},
		{"single author, different", "Frank Herbert", []string{"Brian Herbert"}, true},
		{"single author, same", "Frank Herbert", []string{"Frank Herbert"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newFileIdentityFixture(t, models.MediaTypeEbook, tc.bookAuthor, tc.itemAuthors[0], noOwner, noOwner)
			f.item.Authors = creditedItemAuthors(tc.itemAuthors...)
			matcher, err := f.env.importer.newAuthorMatcher(ctx)
			if err != nil {
				t.Fatal(err)
			}
			book, author, err := f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, matcher)
			if err != nil {
				t.Fatal(err)
			}
			if got := book != nil; got != tc.wantConflict {
				t.Fatalf("conflict = %v (book %+v author %+v), want %v", got, book, author, tc.wantConflict)
			}
			if tc.wantConflict && (book.ID != f.owned.ID || author.Name != tc.bookAuthor) {
				t.Errorf("conflict names book %d author %q, want book %d author %q", book.ID, author.Name, f.owned.ID, tc.bookAuthor)
			}
		})
	}
}

// An item with no credited authors is left to resolveAuthor, as before.
func TestConflictingTrackedBook_NoAuthors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Kevin Behr", "Kevin Behr", noOwner, noOwner)
	f.item.Authors = nil
	book, _, err := f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, nil)
	if err != nil || book != nil {
		t.Fatalf("conflict = %+v err=%v, want none", book, err)
	}
}

// A credited author's foreign id resolves exactly, with no name agreement.
func TestConflictingTrackedBook_CreditedAuthorForeignID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Kevin Behr", "Gene Kim", noOwner, noOwner)
	author, err := f.env.authors.GetByID(ctx, f.authorID)
	if err != nil {
		t.Fatal(err)
	}
	author.ForeignID = absForeignID("author", f.item.LibraryID, "abs-behr")
	if err := f.env.authors.Update(ctx, author); err != nil {
		t.Fatal(err)
	}
	f.item.Authors = []NormalizedAuthor{{ID: "abs-kim", Name: "Gene Kim"}, {ID: "abs-behr", Name: "K. Behr-Spelling"}}
	book, _, err := f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, nil)
	if err != nil || book != nil {
		t.Fatalf("conflict = %+v err=%v, want none: second credited author's id names the book's author", book, err)
	}
}

// A book held by another user is never reachable, whoever the item credits.
func TestConflictingTrackedBook_OwnedBookNotReachable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var ownerID int64
	owner := func(e *covImpEnv) int64 {
		if ownerID == 0 {
			ownerID = createUser(t, e, "reader")
		}
		return ownerID
	}
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Kevin Behr", "Gene Kim", owner, owner)
	f.item.Authors = creditedItemAuthors("Gene Kim", "Kevin Behr")
	book, _, err := f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, nil)
	if err != nil || book != nil {
		t.Fatalf("conflict = %+v err=%v, want none: owned books are skipped by the scan", book, err)
	}
}

// TestImporter_PathConflictFailureNamesBook is #4189 point 1. A source with
// no review queue (BookOrbit) fails the item; the stored message must name
// the book and author it conflicted with, in the ABS wording.
func TestImporter_PathConflictFailureNamesBook(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Frank Herbert", "Holly Black", noOwner, noOwner)
	f.item.Authors = creditedItemAuthors("Holly Black", "Cassandra Clare")
	f.env.importer.reviews = nil

	runID := runSingleABSImport(t, f.env.importer, f.item)

	entities, err := f.env.entities.ListByRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	want := `file already tracked on "Chapterhouse: Dune" (book ` + itoa64(f.owned.ID) + `) under a different author`
	for _, e := range entities {
		if e.EntityType == entityTypeBook && e.ExternalID == f.item.ItemID && e.Outcome == itemOutcomeFailed {
			if !strings.Contains(e.MetadataJSON, "book match is ambiguous") || !strings.Contains(e.MetadataJSON, strings.ReplaceAll(want, `"`, `\"`)) || !strings.Contains(e.MetadataJSON, "Frank Herbert") {
				t.Errorf("failure metadata = %s, want the reason plus %q and the author", e.MetadataJSON, want)
			}
			return
		}
	}
	t.Fatalf("no failed entity for %q in %+v", f.item.ItemID, entities)
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// The guard letting a co-authored item through is not enough: the item must
// then link the book holding its file, not mint a second wanted book beside
// it (#4077 again) or a second author for the book.
func TestImporter_CreditedAuthorLinksTrackedBook(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Kevin Behr", "Gene Kim", noOwner, noOwner)
	f.item.Authors = creditedItemAuthors("Gene Kim", "Kevin Behr", "George Spafford")
	f.env.importer.reviews = nil

	runSingleABSImport(t, f.env.importer, f.item)

	books, err := f.env.books.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].ID != f.owned.ID || books[0].AuthorID != f.authorID {
		t.Fatalf("books = %+v, want only the owned book under its own author", books)
	}
	links, err := f.env.provenance.ListByLocal(ctx, entityTypeBook, f.owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) == 0 {
		t.Errorf("provenance empty, want the item linked to the book holding its file")
	}
}

// A later credited author that resolves through a trusted alias counts, the
// same way the first author does.
func TestConflictingTrackedBook_CreditedAuthorTrustedAlias(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFileIdentityFixture(t, models.MediaTypeEbook, "Olivia Chase", "Gene Kim", noOwner, noOwner)
	if err := f.env.aliases.Create(ctx, &models.AuthorAlias{AuthorID: f.authorID, Name: "Olivia Ash", SourceOLID: "OL79034A"}); err != nil {
		t.Fatalf("create alias: %v", err)
	}
	f.item.Authors = creditedItemAuthors("Gene Kim", "  ", "Olivia Ash")
	matcher, err := f.env.importer.newAuthorMatcher(ctx)
	if err != nil {
		t.Fatal(err)
	}
	book, _, err := f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, matcher)
	if err != nil || book != nil {
		t.Fatalf("conflict = %+v err=%v, want none: the alias names the book's author", book, err)
	}
	// Without the alias the same item conflicts.
	f.item.Authors = creditedItemAuthors("Gene Kim", "Olivia Elsewhere")
	book, _, err = f.env.importer.conflictingTrackedBook(ctx, ImportConfig{}, f.item, matcher)
	if err != nil || book == nil {
		t.Fatalf("conflict = %+v err=%v, want the conflict kept", book, err)
	}
}
