package abs

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// datingSimVolumes builds Hardcover catalog entries for the given volume
// numbers of a light novel series: every title differs from every other by one
// digit in an otherwise identical string, which is the shape TitleScore cannot
// separate (#2347).
func datingSimVolumes(numbers ...int) []metadata.SeriesCatalogBook {
	books := make([]metadata.SeriesCatalogBook, 0, len(numbers))
	for _, n := range numbers {
		title := fmt.Sprintf("Trapped in a Dating Sim Vol. %d", n)
		foreignID := fmt.Sprintf("hc:dating-sim-vol-%d", n)
		books = append(books, metadata.SeriesCatalogBook{
			ForeignID:  foreignID,
			ProviderID: strconv.Itoa(700 + n),
			Title:      title,
			Position:   strconv.Itoa(n),
			Book: models.Book{
				ForeignID: foreignID,
				Title:     title,
			},
		})
	}
	return books
}

// TestMatchHardcoverCatalogBookNoSequenceVolumes is the #2347 regression guard.
// An Audiobookshelf item whose record carries no series sequence gets no
// position filter, so every volume in the catalog is scored at a threshold of
// 88, well under the 93-100 band sibling volumes score against each other.
// PartialRatio then hands "Vol. 13" a perfect 100 against "Vol. 1".
func TestMatchHardcoverCatalogBookNoSequenceVolumes(t *testing.T) {
	// The volume the user owns is in the catalog, so it must be found. Before
	// the fix volume 1 tied volume 13 at 100 and the matches==1 guard refused
	// to bind anything, leaving the item unmatched.
	t.Run("binds the volume it actually is", func(t *testing.T) {
		item := NormalizedLibraryItem{Title: "Trapped in a Dating Sim Vol. 13"}
		books := datingSimVolumes(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13)

		best, score, positionMatched, ok := matchHardcoverCatalogBook(item, "", books)
		if !ok {
			t.Fatalf("expected a match for the volume the item is, got none")
		}
		if best.ForeignID != "hc:dating-sim-vol-13" {
			t.Fatalf("matched %q (score %d), want hc:dating-sim-vol-13", best.ForeignID, score)
		}
		if positionMatched {
			t.Fatal("positionMatched should be false with no sequence")
		}
	})

	// The volume the user owns is NOT in the catalog. Before the fix volume 1
	// scored 100 against volume 13 on its own, matches==1, and the item was
	// bound to the wrong volume.
	t.Run("binds nothing rather than the wrong volume", func(t *testing.T) {
		item := NormalizedLibraryItem{Title: "Trapped in a Dating Sim Vol. 1"}
		books := datingSimVolumes(13)

		best, score, _, ok := matchHardcoverCatalogBook(item, "", books)
		if ok {
			t.Fatalf("volume 1 was bound to %q at score %d; the catalog does not hold volume 1", best.ForeignID, score)
		}
	})

	// A sequence keeps its own filter and is unaffected: the position narrows
	// the candidates first, on harder evidence than the title.
	t.Run("a sequence still binds by position", func(t *testing.T) {
		item := NormalizedLibraryItem{Title: "Trapped in a Dating Sim Vol. 1"}
		books := datingSimVolumes(1, 2, 3, 13)

		best, _, positionMatched, ok := matchHardcoverCatalogBook(item, "1", books)
		if !ok || best.ForeignID != "hc:dating-sim-vol-1" {
			t.Fatalf("matched %q (ok=%v), want hc:dating-sim-vol-1", best.ForeignID, ok)
		}
		if !positionMatched {
			t.Fatal("positionMatched should be true when the sequence lines up")
		}
	})
}
