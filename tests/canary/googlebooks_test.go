//go:build canary

package canary_test

import (
	"os"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/googlebooks"
)

// TestGoogleBooksSearchBooks verifies the volumes search endpoint still
// responds with our key and that volumes parse into books. The client works
// keyless against the shared quota pool, but the canary skips without a key
// so a quota-exhausted shared pool can't produce false alarms.
func TestGoogleBooksSearchBooks(t *testing.T) {
	key := os.Getenv("GOOGLE_BOOKS_API_KEY")
	if key == "" {
		t.Skip("GOOGLE_BOOKS_API_KEY is not set; skipping Google Books canary (add the repo secret to enable)")
	}

	books, err := googlebooks.New(key).SearchBooks(testCtx(t), "Dune")
	if err != nil {
		t.Fatalf("SearchBooks(%q): %v", "Dune", err)
	}
	if len(books) == 0 {
		t.Fatalf("SearchBooks(%q) returned no results; expected at least one", "Dune")
	}
	if b := books[0]; b.Title == "" {
		t.Errorf("first book is missing core fields: ForeignID=%q Title=%q", b.ForeignID, b.Title)
	}
}
