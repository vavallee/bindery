package db

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestPathsUnderForOtherBooks_WildcardsInFolderAreLiteral: % and _ in a
// folder name are LIKE wildcards; the prefix query must match them literally,
// so a folder named "100% Pure" or "A_B" returns only its own rows and not a
// sibling the wildcard would also match. Case is folded.
func TestPathsUnderForOtherBooks_WildcardsInFolderAreLiteral(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	a := seedAuthor(t, authors, "OL-WILD-A", "Wild Author")
	mine := seedBook(t, books, a.ID, "OL-WILD-MINE", "Mine")
	other := seedBook(t, books, a.ID, "OL-WILD-OTHER", "Other")

	lib := t.TempDir()
	add := func(bookID int64, parts ...string) string {
		p := filepath.Join(append([]string{lib}, parts...)...)
		if err := books.AddBookFile(ctx, bookID, models.MediaTypeEbook, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pct := add(other.ID, "100% Pure", "in.epub")
	pctUpper := add(other.ID, "100% PURE", "upper.epub")
	add(other.ID, "100X Pure", "sibling.epub")
	add(other.ID, "100% Pure Extra", "sibling.epub")
	under := add(other.ID, "A_B", "in.epub")
	add(other.ID, "AxB", "sibling.epub")
	add(mine.ID, "100% Pure", "mine.epub")

	cases := map[string][]string{
		filepath.Join(lib, "100% Pure"): {pct, pctUpper},
		filepath.Join(lib, "A_B"):       {under},
	}
	for dir, want := range cases {
		got, err := books.PathsUnderForOtherBooks(ctx, dir, mine.ID)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("PathsUnderForOtherBooks(%q) = %v, want %v", dir, got, want)
		}
	}
}
