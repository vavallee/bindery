package bookhydrate

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// Promoting an owned ebook to 'both' adds a monitored format with nothing
// behind it. Status must follow, or the row keeps saying 'imported' and the
// wanted page, the scheduled sweep and the author bulk search never see the
// gap (#1634).
func TestDeriveAudiobookMetadata_PromotionReevaluatesStatus(t *testing.T) {
	book := &models.Book{
		Title:         "Owned Ebook",
		MediaType:     models.MediaTypeEbook,
		EbookFilePath: "/library/owned.epub",
		Status:        models.BookStatusImported,
	}

	if changed := deriveAudiobookMetadataFromEdition(book, models.Edition{}, false); !changed {
		t.Fatal("expected the promotion to report a change")
	}
	if book.MediaType != models.MediaTypeBoth {
		t.Fatalf("mediaType = %q, want both", book.MediaType)
	}
	if book.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want wanted: the audiobook slot is now monitored and empty", book.Status)
	}
}

// A book the user has skipped stays skipped: that is a decision, not a
// derived state.
func TestDeriveAudiobookMetadata_PromotionLeavesSkippedAlone(t *testing.T) {
	book := &models.Book{
		Title:         "Skipped Ebook",
		MediaType:     models.MediaTypeEbook,
		EbookFilePath: "/library/skipped.epub",
		Status:        models.BookStatusSkipped,
	}
	deriveAudiobookMetadataFromEdition(book, models.Edition{}, false)
	if book.Status != models.BookStatusSkipped {
		t.Errorf("status = %q, want skipped untouched", book.Status)
	}
}

// A pinned media type blocks the promotion entirely, so there is nothing to
// reevaluate and status must not move (#1732).
func TestDeriveAudiobookMetadata_PinnedLeavesStatusAlone(t *testing.T) {
	book := &models.Book{
		Title:         "Pinned Ebook",
		MediaType:     models.MediaTypeEbook,
		EbookFilePath: "/library/pinned.epub",
		Status:        models.BookStatusImported,
	}
	deriveAudiobookMetadataFromEdition(book, models.Edition{}, true)
	if book.MediaType != models.MediaTypeEbook {
		t.Fatalf("mediaType = %q, want ebook: pinned must block the promotion", book.MediaType)
	}
	if book.Status != models.BookStatusImported {
		t.Errorf("status = %q, want imported untouched", book.Status)
	}
}
