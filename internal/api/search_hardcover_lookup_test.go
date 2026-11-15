package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// hardcoverLookupBook is long enough in description and has a cover, so the
// aggregator returns it without an enrichment pass.
func hardcoverLookupBook() *models.Book {
	return &models.Book{
		ForeignID:   "hc:howl-of-the-hearthworld",
		Title:       "Howl of the Hearthworld",
		Description: "A description long enough to skip the aggregator's enrichment branch.",
		ImageURL:    "https://assets.hardcover.app/howl.jpg",
		Author:      &models.Author{ForeignID: "hc:some-author", Name: "Some Author"},
	}
}

func hardcoverLookup(t *testing.T, h *SearchHandler, ref string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Lookup(rec, httptest.NewRequest(http.MethodGet, "/api/v1/book/lookup?hardcover="+url.QueryEscape(ref), nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// A book whose Hardcover page exists but that the title search does not
// surface can be fetched by its slug (#3839), with or without the hc: prefix
// Bindery stores Hardcover ids under, and comes back as the record a search hit
// would be.
func TestLookup_Hardcover(t *testing.T) {
	hardcover := &stubMetaProvider{
		name:        "hardcover",
		getBookByID: map[string]*models.Book{"hc:howl-of-the-hearthworld": hardcoverLookupBook()},
	}
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}, hardcover), nil, nil)

	for _, ref := range []string{"howl-of-the-hearthworld", "hc:howl-of-the-hearthworld", "  howl-of-the-hearthworld  "} {
		rec, body := hardcoverLookup(t, h, ref)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d (%s), want 200", ref, rec.Code, rec.Body.String())
		}
		if body["foreignBookId"] != "hc:howl-of-the-hearthworld" || body["title"] != "Howl of the Hearthworld" {
			t.Errorf("%q: got %v, want the Hardcover record", ref, body)
		}
	}
}

func TestLookup_HardcoverMiss(t *testing.T) {
	hardcover := &stubMetaProvider{name: "hardcover"}
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}, hardcover), nil, nil)

	rec, body := hardcoverLookup(t, h, "no-such-book")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "no-such-book") {
		t.Errorf("error = %q, want it to name the slug", msg)
	}
}

// countingHardcover counts the GetBook calls that reach Hardcover.
type countingHardcover struct {
	stubMetaProvider
	calls int
}

func (p *countingHardcover) GetBook(ctx context.Context, fid string) (*models.Book, error) {
	p.calls++
	return p.stubMetaProvider.GetBook(ctx, fid)
}

// A value that cannot be a slug is refused before Hardcover is asked.
func TestLookup_HardcoverRejectsNonSlug(t *testing.T) {
	asked := &countingHardcover{stubMetaProvider: stubMetaProvider{name: "hardcover"}}
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}, asked), nil, nil)

	for _, ref := range []string{"hc:", "two words", "books/dune", "https://hardcover.app/books/dune", "dune?x=1"} {
		rec, body := hardcoverLookup(t, h, ref)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", ref, rec.Code)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "slug") {
			t.Errorf("%q: error = %q, want it to explain the slug", ref, msg)
		}
	}
	if asked.calls != 0 {
		t.Errorf("Hardcover asked %d times for values that are not slugs", asked.calls)
	}
}

// With Hardcover turned off as a source it is not registered, so the lookup
// says that rather than claiming the book does not exist.
func TestLookup_HardcoverSourceOff(t *testing.T) {
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}), nil, nil)

	rec, body := hardcoverLookup(t, h, "howl-of-the-hearthworld")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "turned off") {
		t.Errorf("error = %q, want it to say the Hardcover source is turned off", msg)
	}
}

// Without a token the Hardcover client answers ErrProviderNotConfigured; the
// user is told to save one instead of getting a generic provider failure.
func TestLookup_HardcoverNoToken(t *testing.T) {
	hardcover := &stubMetaProvider{
		name:           "hardcover",
		getBookErrByID: map[string]error{"hc:howl-of-the-hearthworld": metadata.ErrProviderNotConfigured},
	}
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}, hardcover), nil, nil)

	rec, body := hardcoverLookup(t, h, "howl-of-the-hearthworld")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "API token") {
		t.Errorf("error = %q, want it to ask for a Hardcover API token", msg)
	}
}

func TestLookup_HardcoverUpstreamError(t *testing.T) {
	hardcover := &stubMetaProvider{
		name:           "hardcover",
		getBookErrByID: map[string]error{"hc:howl-of-the-hearthworld": errors.New("hardcover get book: HTTP 503")},
	}
	h := NewSearchHandler(metadata.NewAggregator(&stubProvider{}, hardcover), nil, nil)

	rec, body := hardcoverLookup(t, h, "howl-of-the-hearthworld")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if body["error"] != "metadata provider unavailable" {
		t.Errorf("error = %v, want the generic upstream message", body["error"])
	}
}
