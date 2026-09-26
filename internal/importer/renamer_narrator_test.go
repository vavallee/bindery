package importer

import (
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestRenamerNarratorToken covers the {Narrator} token (#2717). book.Narrator
// holds the whole credit as a comma-joined string (see audnex.NarratorList and
// audible's product decode), so the token renders every narrator rather than
// picking the first. Before the token existed the group matched no keyword and
// renderSegment kept it verbatim, so "{Narrator}" landed in the path as
// literal braces.
func TestRenamerNarratorToken(t *testing.T) {
	r := NewRenamer("{Author}/{Narrator}/{Title}.{ext}")
	author := &models.Author{Name: "Brandon Sanderson"}
	book := &models.Book{
		Title:    "The Way of Kings",
		Narrator: "Michael Kramer, Kate Reading",
	}

	got, err := r.DestPath("/books", author, book, "", "", "book.m4b")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Brandon Sanderson", "Michael Kramer, Kate Reading", "The Way of Kings.m4b")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestRenamerNarratorTokenEmpty pins the no-narrator case to the convention the
// other optional tokens already follow: the value renders empty, and a segment
// that renders empty is dropped. Nothing here invents a fallback or leaves the
// braces behind.
func TestRenamerNarratorTokenEmpty(t *testing.T) {
	r := NewRenamer("{Author}/{Narrator}/{Title}.{ext}")
	author := &models.Author{Name: "Author"}
	book := &models.Book{Title: "Some Book"}

	got, err := r.DestPath("/books", author, book, "", "", "book.epub")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Author", "Some Book.epub")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestRenamerNarratorSanitized proves a narrator credit goes through the same
// sanitizePath as every other text token, so a ":" or "?" in the name cannot
// reach the filesystem.
func TestRenamerNarratorSanitized(t *testing.T) {
	r := NewRenamer("{Author}/{Narrator}/{Title}.{ext}")
	author := &models.Author{Name: "Author"}
	book := &models.Book{
		Title:    "Some Book",
		Narrator: "Michael Kramer: Narrator?",
	}

	got, err := r.DestPath("/books", author, book, "", "", "book.m4b")
	if err != nil {
		t.Fatalf("DestPath: %v", err)
	}
	want := filepath.Join("/books", "Author", "Michael Kramer- Narrator", "Some Book.m4b")
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
