package openlibrary

import (
	"context"
	"os"
	"testing"
)

func skipIfShort(t *testing.T) {
	if os.Getenv("BINDERY_INTEGRATION") == "" {
		t.Skip("skipping integration test; set BINDERY_INTEGRATION=1 to run")
	}
}

func TestSearchAuthors_Integration(t *testing.T) {
	skipIfShort(t)
	c := New()
	authors, err := c.SearchAuthors(context.Background(), "Stephen King")
	if err != nil {
		t.Fatalf("search authors: %v", err)
	}
	if len(authors) == 0 {
		t.Fatal("expected at least one author result")
	}
	found := false
	for _, a := range authors {
		if a.Name == "Stephen King" {
			found = true
			if a.ForeignID == "" {
				t.Error("expected non-empty foreign ID for Stephen King")
			}
		}
	}
	if !found {
		t.Error("expected 'Stephen King' in results")
	}
}

func TestSearchBooks_Integration(t *testing.T) {
	skipIfShort(t)
	c := New()
	books, err := c.SearchBooks(context.Background(), "Dark Matter")
	if err != nil {
		t.Fatalf("search books: %v", err)
	}
	if len(books) == 0 {
		t.Fatal("expected at least one book result")
	}
	t.Logf("found %d books for 'Dark Matter'", len(books))
	t.Logf("first: %s (ID: %s)", books[0].Title, books[0].ForeignID)
}

func TestGetBook_Integration(t *testing.T) {
	skipIfShort(t)
	c := New()
	// OL20617889W is "The Shining" by Stephen King
	book, err := c.GetBook(context.Background(), "OL20617889W")
	if err != nil {
		t.Fatalf("get book: %v", err)
	}
	if book.Title == "" {
		t.Error("expected non-empty title")
	}
	t.Logf("book: %s, description length: %d", book.Title, len(book.Description))
}

func TestGetEditions_Integration(t *testing.T) {
	skipIfShort(t)
	c := New()
	editions, err := c.GetEditions(context.Background(), "OL20617889W")
	if err != nil {
		t.Fatalf("get editions: %v", err)
	}
	if len(editions) == 0 {
		t.Fatal("expected at least one edition")
	}
	t.Logf("found %d editions", len(editions))
	for i, e := range editions {
		if i >= 3 {
			break
		}
		t.Logf("  edition: %s (ISBN13: %v, format: %s)", e.Title, e.ISBN13, e.Format)
	}
}

func TestSortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Stephen King", "King, Stephen"},
		{"J.R.R. Tolkien", "Tolkien, J.R.R."},
		{"Madonna", "Madonna"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sortName(tt.input)
		if got != tt.want {
			t.Errorf("sortName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		input interface{}
		want  string
	}{
		{"plain string", "plain string"},
		{map[string]interface{}{"type": "/type/text", "value": "rich text"}, "rich text"},
		{nil, ""},
		{42, ""},
	}
	for _, tt := range tests {
		got := extractText(tt.input)
		if got != tt.want {
			t.Errorf("extractText(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseSeriesRef(t *testing.T) {
	tests := []struct {
		raw       string
		wantTitle string
		wantPos   string
		wantFID   string
	}{
		{
			raw:       "Dune Chronicles",
			wantTitle: "Dune Chronicles",
			wantPos:   "",
			wantFID:   "ol-series:dune-chronicles",
		},
		{
			raw:       "Dune Chronicles #1",
			wantTitle: "Dune Chronicles",
			wantPos:   "1",
			wantFID:   "ol-series:dune-chronicles",
		},
		{
			raw:       "Mistborn #3",
			wantTitle: "Mistborn",
			wantPos:   "3",
			wantFID:   "ol-series:mistborn",
		},
		{
			raw:       "Wheel of Time, Book 1",
			wantTitle: "Wheel of Time",
			wantPos:   "1",
			wantFID:   "ol-series:wheel-of-time",
		},
		{
			raw:       "Harry Potter -- Book 3",
			wantTitle: "Harry Potter",
			wantPos:   "3",
			wantFID:   "ol-series:harry-potter",
		},
		{
			raw:       "A Series #1.5",
			wantTitle: "A Series",
			wantPos:   "1.5",
			wantFID:   "ol-series:a-series",
		},
		{
			// Extra whitespace should be trimmed.
			raw:       "  The Dark Tower  #7  ",
			wantTitle: "The Dark Tower",
			wantPos:   "7",
			wantFID:   "ol-series:the-dark-tower",
		},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := parseSeriesRef(tt.raw)
			if !ok {
				t.Fatalf("parseSeriesRef(%q) returned ok=false; want a usable ref", tt.raw)
			}
			if got.Title != tt.wantTitle {
				t.Errorf("Title: want %q, got %q", tt.wantTitle, got.Title)
			}
			if got.Position != tt.wantPos {
				t.Errorf("Position: want %q, got %q", tt.wantPos, got.Position)
			}
			if got.ForeignID != tt.wantFID {
				t.Errorf("ForeignID: want %q, got %q", tt.wantFID, got.ForeignID)
			}
		})
	}
}

func TestSeriesSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Dune Chronicles", "dune-chronicles"},
		{"Harry Potter & the Philosopher's Stone", "harry-potter-the-philosopher-s-stone"},
		{"  spaces  ", "spaces"},
		{"A", "a"},
	}
	for _, tt := range tests {
		got := seriesSlug(tt.input)
		if got != tt.want {
			t.Errorf("seriesSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestSeriesSlugNonLatin is the regression test for #1645. The previous slug
// kept only ASCII [a-z0-9], so a title with no ASCII alphanumerics produced ""
// and the ForeignID degenerated to the bare "ol-series:" prefix — which, via
// CreateOrGet's INSERT OR IGNORE on foreign_id, merged every non-Latin series
// onto a single shared row. Accented Latin collided the same way.
func TestSeriesSlugNonLatin(t *testing.T) {
	// Previously "" (→ shared row); must now be distinct and non-empty.
	nonLatin := []string{"三体", "ハリー・ポッター", "Гарри Поттер", "Ὀδύσσεια", "سلسلة"}
	seen := map[string]string{}
	for _, title := range nonLatin {
		got := seriesSlug(title)
		if got == "" {
			t.Errorf("seriesSlug(%q) = %q; non-Latin titles must produce a usable slug", title, got)
			continue
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("seriesSlug collision: %q and %q both produce %q", prev, title, got)
		}
		seen[got] = title
	}

	// Accented Latin folds to its base letters rather than dropping the rune,
	// which is what made these two collide on "dland".
	if got, want := seriesSlug("Ödland"), "odland"; got != want {
		t.Errorf("seriesSlug(%q) = %q, want %q", "Ödland", got, want)
	}
	if got, want := seriesSlug("Ådland"), "adland"; got != want {
		t.Errorf("seriesSlug(%q) = %q, want %q", "Ådland", got, want)
	}
	if seriesSlug("Ödland") == seriesSlug("Ådland") {
		t.Error("Ödland and Ådland must not share a slug")
	}

	// Only genuinely identity-free titles yield "", and those must be dropped
	// by the caller rather than emitting a prefix-only ForeignID.
	for _, empty := range []string{"", "   ", "---", "!!!"} {
		if got := seriesSlug(empty); got != "" {
			t.Errorf("seriesSlug(%q) = %q, want empty", empty, got)
		}
		if _, ok := parseSeriesRef(empty); ok {
			t.Errorf("parseSeriesRef(%q) returned ok=true; a slugless title must be dropped", empty)
		}
	}
}

// TestParseSeriesRefPrimaryTracksKeptRefs guards the ordering subtlety in the
// #1645 fix: Primary must mark the first ref actually kept, so dropping an
// unusable leading entry doesn't leave the book with no primary series.
func TestParseSeriesRefPrimaryTracksKeptRefs(t *testing.T) {
	refs := seriesRefsFrom([]string{"!!!", "Dune Chronicles #1", "Other Series"})
	if len(refs) != 2 {
		t.Fatalf("expected the slugless entry to be dropped, got %d refs", len(refs))
	}
	if !refs[0].Primary {
		t.Error("first KEPT ref should be Primary after a leading entry is dropped")
	}
	if refs[1].Primary {
		t.Error("only one ref should be Primary")
	}
}
