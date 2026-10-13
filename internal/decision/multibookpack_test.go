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
