package metadata

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestEnrichBookRanobeDBNeedsAnAuthorName: an OpenLibrary work can carry an
// author ID and no author name, and pickEnrichmentMatch then skips its author
// check. RanobeDB must neither be asked nor allowed to write a title for such
// a book.
func TestEnrichBookRanobeDBNeedsAnAuthorName(t *testing.T) {
	for name, author := range map[string]*models.Author{
		"nil author":   nil,
		"empty name":   {ForeignID: "OL1A"},
		"blank name":   {ForeignID: "OL1A", Name: "  "},
		"named author": {ForeignID: "OL1A", Name: "Isuna Hasekura"},
	} {
		t.Run(name, func(t *testing.T) {
			enricher := ranobeDBEnricher()
			agg := newTestAggregator(&mockProvider{name: "ol"}, enricher)
			book := lightNovel("")
			book.Author = author
			agg.enrichBook(context.Background(), book)
			if author != nil && strings.TrimSpace(author.Name) != "" {
				if book.Title != lnRanobeDB {
					t.Errorf("title = %q, want %q", book.Title, lnRanobeDB)
				}
				return
			}
			if book.Title != lnInputTitle || book.SortTitle != lnInputTitle {
				t.Errorf("title %q, sort title %q; written on a title match alone", book.Title, book.SortTitle)
			}
			if len(enricher.searchBookQueries) != 0 {
				t.Errorf("RanobeDB was asked %q for a book with no author name", enricher.searchBookQueries)
			}
		})
	}
}

// TestEnrichBookCachedRanobeDBTitleNotReplayedWhenOff: a title cached while
// RanobeDB was wired is not served after it is switched off. The provider set
// is part of the cache key, so the off set misses and runs live.
func TestEnrichBookCachedRanobeDBTitleNotReplayedWhenOff(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher())

	on := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), on)
	if on.Title != lnRanobeDB {
		t.Fatalf("with RanobeDB on: title %q, want %q", on.Title, lnRanobeDB)
	}

	// What Reconfigure does when the setting is saved as false.
	agg.enrichers = nil
	off := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), off)
	if off.Title != lnInputTitle || off.SortTitle != lnInputTitle {
		t.Errorf("with RanobeDB off: title %q, sort title %q; a cached RanobeDB title was replayed", off.Title, off.SortTitle)
	}
}
