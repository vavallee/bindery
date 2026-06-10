//go:build canary

package canary_test

import (
	"testing"

	"github.com/vavallee/bindery/internal/metadata/openlibrary"
)

// frankHerbertOLID is the OpenLibrary author ID for Frank Herbert
// (https://openlibrary.org/authors/OL79034A). OL author IDs are permanent
// identifiers, so this is safe to hardcode.
const frankHerbertOLID = "OL79034A"

// TestOpenLibrarySearchAuthors verifies the author search endpoint still
// responds and that the documents parse into authors with their core
// identity fields populated.
func TestOpenLibrarySearchAuthors(t *testing.T) {
	authors, err := openlibrary.New().SearchAuthors(testCtx(t), "Frank Herbert")
	if err != nil {
		t.Fatalf("SearchAuthors(%q): %v", "Frank Herbert", err)
	}
	if len(authors) == 0 {
		t.Fatalf("SearchAuthors(%q) returned no results; expected at least one", "Frank Herbert")
	}
	if a := authors[0]; a.ForeignID == "" || a.Name == "" {
		t.Errorf("first author is missing core fields: ForeignID=%q Name=%q", a.ForeignID, a.Name)
	}
}

// TestOpenLibrarySearchBooks verifies /search.json still responds (OL has
// deprecated search endpoints under us before, see issue #462) and that the
// documents parse into books with identity fields populated.
func TestOpenLibrarySearchBooks(t *testing.T) {
	books, err := openlibrary.New().SearchBooks(testCtx(t), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks(%q): %v", "Dune", err)
	}
	if len(books) == 0 {
		t.Fatalf("SearchBooks(%q) returned no results; expected at least one", "Dune")
	}
	if b := books[0]; b.ForeignID == "" || b.Title == "" {
		t.Errorf("first book is missing core fields: ForeignID=%q Title=%q", b.ForeignID, b.Title)
	}
}

// TestOpenLibraryGetAuthorWorks verifies the author-works path (the
// /authors/{id}/works.json backfill plus the search-index enrichment) still
// parses for a stable, prolific author.
func TestOpenLibraryGetAuthorWorks(t *testing.T) {
	books, err := openlibrary.New().GetAuthorWorks(testCtx(t), frankHerbertOLID)
	if err != nil {
		t.Fatalf("GetAuthorWorks(%q): %v", frankHerbertOLID, err)
	}
	if len(books) == 0 {
		t.Fatalf("GetAuthorWorks(%q) returned no works; Frank Herbert has hundreds", frankHerbertOLID)
	}
	if b := books[0]; b.ForeignID == "" || b.Title == "" {
		t.Errorf("first work is missing core fields: ForeignID=%q Title=%q", b.ForeignID, b.Title)
	}
}
