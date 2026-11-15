package metadata

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// RanobeDB (#2012) is the only enricher that writes a book's title. These
// tests pin the merge side: the title is written only from RanobeDB, never
// over a lock, after the other enrichers have searched by the old title, and a
// cache hit replays exactly what the live call wrote. The client's own checks
// (clear winner, volume numbers, author name order) are in
// internal/metadata/ranobedb.

const (
	lnInputTitle = "Spice and Wolf, Vol. 3 (light novel)"
	lnRanobeDB   = "Spice and Wolf, Vol. 3"
)

func lightNovel(author string) *models.Book {
	return &models.Book{
		ForeignID:        "OL3188W",
		MetadataProvider: "openlibrary",
		Title:            lnInputTitle,
		SortTitle:        lnInputTitle,
		Author:           &models.Author{Name: author},
	}
}

// ranobeDBEnricher answers like the real client: one candidate per credited
// author, the family name first and marked by a comma.
func ranobeDBEnricher() *mockProvider {
	return &mockProvider{name: "ranobedb", searchBooks: []models.Book{{
		ForeignID:        "ranobedb:3188",
		Title:            lnRanobeDB,
		MetadataProvider: "ranobedb",
		Author:           &models.Author{Name: "Hasekura, Isuna"},
	}}}
}

func TestEnrichBookWritesRanobeDBTitle(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher())
	book := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), book)
	if book.Title != lnRanobeDB {
		t.Errorf("title = %q, want %q", book.Title, lnRanobeDB)
	}
	if book.SortTitle != lnRanobeDB {
		t.Errorf("sort title = %q, want it to follow the title", book.SortTitle)
	}
}

// TestEnrichBookRanobeDBAuthorNameOrder uses the real name from the issue:
// RanobeDB has "Hasekura Isuna", family name first; Bindery's providers have
// "Isuna Hasekura". The client sends "Hasekura, Isuna", and either order on
// the book must match it, while a different author must not.
func TestEnrichBookRanobeDBAuthorNameOrder(t *testing.T) {
	for _, tc := range []struct {
		author string
		want   string
	}{
		{author: "Isuna Hasekura", want: lnRanobeDB},
		{author: "Hasekura Isuna", want: lnRanobeDB},
		{author: "Hasekura, Isuna", want: lnRanobeDB},
		{author: "Reki Kawahara", want: lnInputTitle},
	} {
		t.Run(tc.author, func(t *testing.T) {
			agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher())
			book := lightNovel(tc.author)
			agg.enrichBook(context.Background(), book)
			if book.Title != tc.want {
				t.Errorf("title = %q, want %q", book.Title, tc.want)
			}
		})
	}
}

// TestEnrichBookRanobeDBNeedsTheComma pins why the client marks the family
// name: without the comma "Hasekura Isuna" against "Isuna Hasekura" is a bare
// reordering, which the enrichment author check refuses, so a client that sent
// RanobeDB's romanisation as given would never write a title.
func TestEnrichBookRanobeDBNeedsTheComma(t *testing.T) {
	enricher := ranobeDBEnricher()
	enricher.searchBooks[0].Author.Name = "Hasekura Isuna"
	agg := newTestAggregator(&mockProvider{name: "ol"}, enricher)
	book := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), book)
	if book.Title != lnInputTitle {
		t.Errorf("title = %q; a bare reordering of the name matched", book.Title)
	}
}

func TestEnrichBookHonoursTitleLock(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher())
	for i := range 2 {
		book := lightNovel("Isuna Hasekura")
		book.LockField(models.BookFieldTitle)
		agg.enrichBook(context.Background(), book)
		if book.Title != lnInputTitle || book.SortTitle != lnInputTitle {
			t.Errorf("call %d: title %q, sort title %q; a locked title was overwritten", i+1, book.Title, book.SortTitle)
		}
	}
	// A locked book must not seed the snapshot cache: its snapshot holds the
	// locked title, and an unlocked book of the same work replaying it would
	// never get RanobeDB's.
	book := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), book)
	if book.Title != lnRanobeDB {
		t.Errorf("unlocked book after locked ones: title %q, want %q", book.Title, lnRanobeDB)
	}
}

// TestEnrichBookTitleCacheHitMatchesLiveCall: the second book of the same
// work is served from the snapshot, and must come out exactly as the first.
func TestEnrichBookTitleCacheHitMatchesLiveCall(t *testing.T) {
	enricher := ranobeDBEnricher()
	agg := newTestAggregator(&mockProvider{name: "ol"}, enricher)

	live := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), live)
	cached := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), cached)

	if got := len(enricher.searchBookQueries); got != 1 {
		t.Fatalf("enricher asked %d times, want 1: the second call was not a cache hit", got)
	}
	if cached.Title != live.Title || cached.SortTitle != live.SortTitle {
		t.Errorf("cache hit gave (%q, %q), live call gave (%q, %q)",
			cached.Title, cached.SortTitle, live.Title, live.SortTitle)
	}
	if live.Title != lnRanobeDB {
		t.Errorf("live title = %q, want %q", live.Title, lnRanobeDB)
	}

	// A lock set on a book served from the cache still holds.
	locked := lightNovel("Isuna Hasekura")
	locked.LockField(models.BookFieldTitle)
	agg.enrichBook(context.Background(), locked)
	if locked.Title != lnInputTitle {
		t.Errorf("cache hit wrote a locked title: %q", locked.Title)
	}
}

// TestEnrichBookTitleOnlyFromRanobeDB: another enricher's title is a search
// result. It may match the book, but it is never written.
func TestEnrichBookTitleOnlyFromRanobeDB(t *testing.T) {
	other := ranobeDBEnricher()
	other.name = "hardcover"
	agg := newTestAggregator(&mockProvider{name: "ol"}, other)
	book := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), book)
	if book.Title != lnInputTitle {
		t.Errorf("title = %q; an enricher other than RanobeDB wrote it", book.Title)
	}
}

// TestEnrichBookTitleWrittenAfterOtherEnrichers: the enrichers after RanobeDB
// search by, and match against, the book's own title, not RanobeDB's.
func TestEnrichBookTitleWrittenAfterOtherEnrichers(t *testing.T) {
	after := &mockProvider{name: "hardcover", searchBooks: []models.Book{{
		Title:       lnInputTitle,
		Author:      &models.Author{Name: "Isuna Hasekura"},
		Description: "A description that only matches the book's own title.",
	}}}
	agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher(), after)
	book := lightNovel("Isuna Hasekura")
	agg.enrichBook(context.Background(), book)
	if len(after.searchBookQueries) != 1 || after.searchBookQueries[0] != lnInputTitle {
		t.Errorf("later enricher was asked %q, want [%q]", after.searchBookQueries, lnInputTitle)
	}
	if book.Description == "" {
		t.Error("later enricher's match was lost: it was matched against RanobeDB's title")
	}
	if book.Title != lnRanobeDB {
		t.Errorf("title = %q, want %q", book.Title, lnRanobeDB)
	}
}

// TestEnrichBookKeepsCustomSortTitle: a sort title that is not a copy of the
// title was chosen, and the title change leaves it alone, live and cached.
func TestEnrichBookKeepsCustomSortTitle(t *testing.T) {
	agg := newTestAggregator(&mockProvider{name: "ol"}, ranobeDBEnricher())
	for i := range 2 {
		book := lightNovel("Isuna Hasekura")
		book.SortTitle = "Spice and Wolf 03"
		agg.enrichBook(context.Background(), book)
		if book.Title != lnRanobeDB || book.SortTitle != "Spice and Wolf 03" {
			t.Errorf("call %d: title %q, sort title %q", i+1, book.Title, book.SortTitle)
		}
	}
}
