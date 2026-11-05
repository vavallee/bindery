package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestReorganize_EbookAtCollisionSuffixIsInPlace: since #3560 an import whose
// rendered ebook name is held by another book or an untracked file lands at
// "Name (2).epub". Reorganize must read that file as already in place while
// the base name is still taken, instead of reporting "destination already
// exists" against the file holding the base name on every preview. Once the
// base name is free it is an ordinary move back to it.
func TestReorganize_EbookAtCollisionSuffixIsInPlace(t *testing.T) {
	cases := []struct {
		name       string
		holder     string // "book", "untracked" or "" (base free)
		suffix     string
		wantStatus string
	}{
		{"base held by another book", "book", " (2)", ReorgStatusNoop},
		{"base held by an untracked file", "untracked", " (2)", ReorgStatusNoop},
		{"higher suffix", "book", " (3)", ReorgStatusNoop},
		{"base free again", "", " (2)", ReorgStatusMove},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, libraryDir, _, ctx := reorgFixture(t)
			author := env.seedAuthor(t, ctx, "Jane Doe")
			book := env.seedBook(t, ctx, author, "My Book")
			base := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
			switch tc.holder {
			case "book":
				other := &models.Book{
					ForeignID: "hc:other", AuthorID: author.ID, Title: "My Book", SortTitle: "My Book",
					Status: models.BookStatusImported, MediaType: models.MediaTypeEbook, MetadataProvider: "hardcover",
				}
				if err := env.books.Create(ctx, other); err != nil {
					t.Fatal(err)
				}
				writeFileAt(t, base)
				if err := env.books.AddBookFile(ctx, other.ID, models.MediaTypeEbook, base); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				writeFileAt(t, base)
			}
			cur := strings.TrimSuffix(base, ".epub") + tc.suffix + ".epub"
			writeFileAt(t, cur)
			if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, cur); err != nil {
				t.Fatal(err)
			}

			moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(moves) != 1 || moves[0].Status != tc.wantStatus {
				t.Fatalf("moves = %+v, want one with status %q", moves, tc.wantStatus)
			}
			if tc.wantStatus == ReorgStatusMove && moves[0].Proposed != base {
				t.Errorf("proposed = %q, want the free base name %q", moves[0].Proposed, base)
			}
			if tc.wantStatus != ReorgStatusNoop {
				return
			}
			results := env.s.ApplyReorganize(ctx, []int64{moves[0].FileID})
			if len(results) != 1 || results[0].Status != ReorgStatusNoop {
				t.Errorf("apply results = %+v, want one noop", results)
			}
			if _, err := os.Stat(cur); err != nil {
				t.Errorf("in place file moved: %v", err)
			}
		})
	}
}

// TestPreviewImportDestination_SuffixedFileNotYetTheBooksIsNotInPlace: the
// Fix match destination preview shares proposedPathFor with Reorganize, but
// Fix match keeps the rendered base name. A "(2)" file the target book does
// not track yet must preview against that base name (a collision here), not
// as already in place.
func TestPreviewImportDestination_SuffixedFileNotYetTheBooksIsNotInPlace(t *testing.T) {
	env, libraryDir, _, ctx := reorgFixture(t)
	book := env.seed(t, ctx, "Jane Doe", "My Book")
	base := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)", "My Book - Jane Doe.epub")
	writeFileAt(t, base)
	cur := strings.TrimSuffix(base, ".epub") + " (2).epub"
	writeFileAt(t, cur)

	preview, err := env.s.PreviewImportDestination(ctx, book.ID, cur, models.MediaTypeEbook)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != ReorgStatusCollision || preview.Destination != base {
		t.Errorf("preview = %+v, want a collision at %s", preview, base)
	}
}

// TestReorganize_EbookUnrelatedParenthesisStillCollides: only the exact
// " (N)" sibling (N of 2 or more) of the rendered name counts as in place. A
// file named with other parenthesised text, or "(1)", is still proposed for
// the base name and so still collides with the file holding it.
func TestReorganize_EbookUnrelatedParenthesisStillCollides(t *testing.T) {
	for _, name := range []string{"My Book - Jane Doe (1).epub", "My Book - Jane Doe (copy).epub", "Other (2).epub", "My Book - Jane Doe (02).epub"} {
		t.Run(name, func(t *testing.T) {
			env, libraryDir, _, ctx := reorgFixture(t)
			book := env.seed(t, ctx, "Jane Doe", "My Book")
			dir := filepath.Join(libraryDir, "Jane Doe", "My Book (2020)")
			writeFileAt(t, filepath.Join(dir, "My Book - Jane Doe.epub"))
			cur := filepath.Join(dir, name)
			writeFileAt(t, cur)
			if err := env.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, cur); err != nil {
				t.Fatal(err)
			}
			moves, err := env.s.PreviewReorganizeBook(ctx, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(moves) != 1 || moves[0].Status != ReorgStatusCollision {
				t.Fatalf("moves = %+v, want a collision against the occupied base name", moves)
			}
		})
	}
}
