package decision

import (
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestMultiBookPackSpec is the specification-level half of #2276.
func TestMultiBookPackSpec(t *testing.T) {
	const packRelease = "Red Rising Series - Books 1 - 4 by Pierce Brown [ENG / M4B MP3] [VIP]"
	redRising := models.Book{Title: "Red Rising"}

	t.Run("rejects the reported pack", func(t *testing.T) {
		ok, reason := MultiBookPackSpec{}.IsSatisfiedBy(Release{Title: packRelease}, redRising)
		if ok {
			t.Fatal("the four-book pack was accepted for a single-book search")
		}
		if !strings.HasPrefix(reason, RejectionMultiBookPack) {
			t.Errorf("reason %q does not carry the %q prefix the scheduler matches on", reason, RejectionMultiBookPack)
		}
		if !strings.Contains(reason, "Books 1 - 4") {
			t.Errorf("reason %q does not quote the words it judged", reason)
		}
	})

	t.Run("accepts a single-book release", func(t *testing.T) {
		ok, reason := MultiBookPackSpec{}.IsSatisfiedBy(
			Release{Title: "Red Rising - Pierce Brown [M4B]"}, redRising)
		if !ok {
			t.Fatalf("single-book release rejected: %s", reason)
		}
	})

	// Someone tracking a box set or omnibus as one book record wants exactly
	// the release this spec otherwise refuses, and the one-destination problem
	// does not arise for them: the pack is the book.
	t.Run("accepts a pack when the book is itself a bundle", func(t *testing.T) {
		bundle := models.Book{Title: "Red Rising Series Box Set"}
		ok, reason := MultiBookPackSpec{}.IsSatisfiedBy(Release{Title: packRelease}, bundle)
		if !ok {
			t.Fatalf("pack rejected for a book that is itself a bundle: %s", reason)
		}
	})
}

// TestMultiBookPackSpec_FoundationSearch is #3224: the pack auto-grabbed for
// Foundation is refused, the single book release from the same search is not,
// and a pack is still allowed for a book that is itself a bundle.
func TestMultiBookPackSpec_FoundationSearch(t *testing.T) {
	foundation := models.Book{Title: "Foundation"}
	if ok, _ := (MultiBookPackSpec{}).IsSatisfiedBy(Release{Title: "Foundation series by Isaac Asimov EPUB"}, foundation); ok {
		t.Error("the Foundation series pack was accepted for the single book")
	}
	if ok, reason := (MultiBookPackSpec{}).IsSatisfiedBy(Release{Title: "Foundation: HarperVoyager edition by Isaac Asimov, Book 1 EPUB"}, foundation); !ok {
		t.Errorf("the single book release was refused: %s", reason)
	}
	if ok, reason := (MultiBookPackSpec{}).IsSatisfiedBy(Release{Title: "The Foundation Collection Isaac Asimov .EPUB (11 Books)"}, models.Book{Title: "The Foundation Trilogy Omnibus"}); !ok {
		t.Errorf("a pack was refused for a book that is a bundle: %s", reason)
	}
}

// TestMultiBookPackSpec_LibraryContext is #3247: the packs only recognisable
// against the wanted book, from the issue thread.
func TestMultiBookPackSpec_LibraryContext(t *testing.T) {
	macomber := MultiBookPackSpec{SeriesTitle: "Heart of Texas", AuthorTitles: []string{"Lonesome Cowboy", "Texas Two-Step"}}
	for _, tc := range []struct {
		spec    MultiBookPackSpec
		release string
		book    string
		marker  string
	}{
		{MultiBookPackSpec{}, "Dominion of Blades 1-2", "Dominion of Blades: A LitRPG Adventure (Volume 1)", "Dominion of Blades 1-2"},
		{
			MultiBookPackSpec{AuthorTitles: []string{"The Firm", "The Appeal"}},
			"John Grisham - Three Classic Thrillers (The Firm; The Appeal; The Chamber) (v5.0) (epub)", "Chamber",
			"The Firm; The Appeal; The Chamber",
		},
		{
			macomber,
			"Debbie Macomber - [Heart of Texas 01-03] - Lonesome Cowboy; Texas Two-Step; Caroline's Child (v5.0) (epub)", "Caroline's Child",
			"Heart of Texas 01-03",
		},
	} {
		ok, reason := tc.spec.IsSatisfiedBy(Release{Title: tc.release}, models.Book{Title: tc.book})
		if ok {
			t.Errorf("%q was accepted for %q", tc.release, tc.book)
			continue
		}
		if !strings.HasPrefix(reason, RejectionMultiBookPack) || !strings.Contains(reason, tc.marker) {
			t.Errorf("reason %q should carry the pack prefix and quote %q", reason, tc.marker)
		}
	}

	// A single book release from the same series passes.
	if ok, reason := macomber.IsSatisfiedBy(
		Release{Title: "Debbie Macomber - Heart of Texas Book 3 - Caroline's Child (epub)"},
		models.Book{Title: "Caroline's Child"},
	); !ok {
		t.Errorf("single book release refused: %s", reason)
	}

	// The wanted book is itself the series pack: the exemption reads the
	// book's title against the same context.
	if ok, reason := macomber.IsSatisfiedBy(
		Release{Title: "Debbie Macomber - [Heart of Texas 01-03] (epub)"},
		models.Book{Title: "Heart of Texas 1-3"},
	); !ok {
		t.Errorf("pack rejected for a book that is itself the series pack: %s", reason)
	}
}
