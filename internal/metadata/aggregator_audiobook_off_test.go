package metadata

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/audnex"
	"github.com/vavallee/bindery/internal/models"
)

type countingAudible struct{ calls atomic.Int32 }

func (c *countingAudible) SearchBooksByAuthor(context.Context, string) ([]models.Book, error) {
	c.calls.Add(1)
	return []models.Book{{Title: "Audible Only", ForeignID: "audible:B01N5IB20Q"}}, nil
}

type countingAudnex struct{ calls atomic.Int32 }

func (c *countingAudnex) GetBook(_ context.Context, asin string) (*audnex.Book, error) {
	c.calls.Add(1)
	return &audnex.Book{ASIN: asin, Title: "T", Narrators: []audnex.Person{{Name: "Narrator N"}}}, nil
}

// TestAudibleDisabledMakesNoRequestAndServesNoCache covers #2015: while
// Audible is off GetAuthorAudiobooks returns nothing and never reaches the
// client, including for an author whose list was cached while it was on, and
// turning it back on restores the lookup without a restart.
func TestAudibleDisabledMakesNoRequestAndServesNoCache(t *testing.T) {
	ctx := context.Background()
	aud := &countingAudible{}
	agg := NewAggregator(&tagProvider{name: "openlibrary"}).WithAudibleCatalogue(aud)

	if books, err := agg.GetAuthorAudiobooks(ctx, "Author A"); err != nil || len(books) != 1 {
		t.Fatalf("enabled: books=%v err=%v", books, err)
	}
	agg.SetAudiobookSourcesDisabled(true, false)
	for _, name := range []string{"Author A", "Author B"} {
		if books, err := agg.GetAuthorAudiobooks(ctx, name); err != nil || len(books) != 0 {
			t.Errorf("disabled %q: books=%v err=%v, want none", name, books, err)
		}
	}
	if n := aud.calls.Load(); n != 1 {
		t.Errorf("client calls = %d, want 1 (only the enabled lookup)", n)
	}
	agg.SetAudiobookSourcesDisabled(false, false)
	if books, _ := agg.GetAuthorAudiobooks(ctx, "Author B"); len(books) != 1 {
		t.Errorf("after re-enabling: books=%v, want 1", books)
	}
}

// TestAudnexDisabledMakesNoRequestAndServesNoCache is the audnex side: the
// narrator filled while it was on is not filled again for a new book, and the
// canonical ASIN lookup answers nothing without a request.
func TestAudnexDisabledMakesNoRequestAndServesNoCache(t *testing.T) {
	ctx := context.Background()
	ax := &countingAudnex{}
	agg := NewAggregator(&tagProvider{name: "openlibrary"}).WithAudnexClient(ax)
	const asin = "B01N5IB20Q"

	on := &models.Book{ASIN: asin, MediaType: models.MediaTypeAudiobook}
	if err := agg.EnrichAudiobook(ctx, on); err != nil || on.Narrator == "" {
		t.Fatalf("enabled: narrator=%q err=%v", on.Narrator, err)
	}
	before := ax.calls.Load()

	agg.SetAudiobookSourcesDisabled(false, true)
	off := &models.Book{ASIN: asin, MediaType: models.MediaTypeAudiobook}
	if err := agg.EnrichAudiobook(ctx, off); err != nil || off.Narrator != "" {
		t.Errorf("disabled: narrator=%q err=%v, want untouched", off.Narrator, err)
	}
	if b, err := agg.GetCanonicalBookByASIN(ctx, "B000SEI1DM"); b != nil || err != nil {
		t.Errorf("disabled canonical lookup = %v, %v", b, err)
	}
	if n := ax.calls.Load(); n != before {
		t.Errorf("audnex received %d calls while disabled", n-before)
	}

	agg.SetAudiobookSourcesDisabled(false, false)
	again := &models.Book{ASIN: asin, MediaType: models.MediaTypeAudiobook}
	if err := agg.EnrichAudiobook(ctx, again); err != nil || again.Narrator == "" {
		t.Errorf("after re-enabling: narrator=%q err=%v", again.Narrator, err)
	}
}

// TestAudiobookSourcesDisabledLeavesEbooksAlone checks the ebook path never
// involved either source and still does not.
func TestAudiobookSourcesDisabledLeavesEbooksAlone(t *testing.T) {
	ctx := context.Background()
	aud, ax := &countingAudible{}, &countingAudnex{}
	agg := NewAggregator(&tagProvider{name: "openlibrary"}).WithAudibleCatalogue(aud).WithAudnexClient(ax)
	agg.SetAudiobookSourcesDisabled(true, true)

	book := &models.Book{ASIN: "B01N5IB20Q", MediaType: models.MediaTypeEbook}
	if err := agg.EnrichAudiobook(ctx, book); err != nil || book.Narrator != "" {
		t.Errorf("ebook enrich changed the book: %+v err=%v", book, err)
	}
	if _, err := agg.SearchBooks(ctx, "dune"); err != nil {
		t.Fatal(err)
	}
	if aud.calls.Load() != 0 || ax.calls.Load() != 0 {
		t.Errorf("calls: audible=%d audnex=%d", aud.calls.Load(), ax.calls.Load())
	}
}
