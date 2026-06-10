//go:build canary

package canary_test

import (
	"os"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

// hardcoverClient returns a client authenticated from HARDCOVER_API_TOKEN,
// or skips the test when the token is absent (every Hardcover GraphQL query
// requires auth, so there is nothing useful to canary without it).
func hardcoverClient(t *testing.T) *hardcover.Client {
	t.Helper()
	token := os.Getenv("HARDCOVER_API_TOKEN")
	if token == "" {
		t.Skip("HARDCOVER_API_TOKEN is not set; skipping Hardcover canary (add the repo secret to enable)")
	}
	return hardcover.New().WithToken(token)
}

// TestHardcoverSearchBooksAndGetBook verifies the Typesense-backed search
// query still parses, then feeds the first result's foreign ID straight into
// GetBook to verify the books-by-slug/id GraphQL query as well. Chaining
// from the live search result (rather than hardcoding a slug) keeps the
// GetBook leg valid by construction even if Hardcover renames slugs, while
// still exercising the exact client code and response shapes.
func TestHardcoverSearchBooksAndGetBook(t *testing.T) {
	c := hardcoverClient(t)
	ctx := testCtx(t)

	books, err := c.SearchBooks(ctx, "Dune")
	if err != nil {
		t.Fatalf("SearchBooks(%q): %v", "Dune", err)
	}
	if len(books) == 0 {
		t.Fatalf("SearchBooks(%q) returned no results; expected at least one", "Dune")
	}
	first := books[0]
	if first.ForeignID == "" || first.Title == "" {
		t.Fatalf("first search result is missing core fields: ForeignID=%q Title=%q", first.ForeignID, first.Title)
	}

	got, err := c.GetBook(ctx, first.ForeignID)
	if err != nil {
		t.Fatalf("GetBook(%q): %v", first.ForeignID, err)
	}
	if got == nil {
		t.Fatalf("GetBook(%q) returned nil for an ID Hardcover search just produced", first.ForeignID)
	}
	if got.Title == "" {
		t.Errorf("GetBook(%q) parsed a book with no title", first.ForeignID)
	}
}

// TestHardcoverGetAuthorWorksByName is the regression canary for #1048: the
// author-works query once selected an edition-only field (`language`) on the
// `books` type and Hardcover rejected the entire query with a GraphQL
// validation error, silently breaking the author-works supplement for every
// author. Any schema drift in this query surfaces here as a returned error
// (the client folds GraphQL `errors` payloads into err), so the only
// assertions needed are "no error" and "non-empty".
func TestHardcoverGetAuthorWorksByName(t *testing.T) {
	c := hardcoverClient(t)

	books, err := c.GetAuthorWorksByName(testCtx(t), "Frank Herbert")
	if err != nil {
		t.Fatalf("GetAuthorWorksByName(%q): %v (a GraphQL validation error here means the Hardcover schema drifted under our query, as in #1048)", "Frank Herbert", err)
	}
	if len(books) == 0 {
		t.Fatalf("GetAuthorWorksByName(%q) returned no works; Frank Herbert has dozens on Hardcover", "Frank Herbert")
	}
	if b := books[0]; b.ForeignID == "" || b.Title == "" {
		t.Errorf("first work is missing core fields: ForeignID=%q Title=%q", b.ForeignID, b.Title)
	}
}
