package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestSearchAuthorsWithOutcomeReportsPrimaryFailure is the distinction #2271
// turns on. Before it, both cases below produced the same thing at the call
// site — a result set with no Hardcover records and a nil error — so the
// Audiobookshelf import bound the author to OpenLibrary in both, and one of
// them was a transient 429.
func TestSearchAuthorsWithOutcomeReportsPrimaryFailure(t *testing.T) {
	ol := &mockProvider{name: "openlibrary", searchAuthors: []models.Author{
		{Name: "Adrian Tchaikovsky", ForeignID: "OL7468980A"},
	}}

	t.Run("primary answered and simply has no record", func(t *testing.T) {
		hc := &mockProvider{name: "hardcover"}
		agg := NewAggregator(hc, ol)
		authors, outcome, err := agg.SearchAuthorsWithOutcome(context.Background(), "Adrian Tchaikovsky")
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(authors) == 0 {
			t.Fatal("expected the fallback provider's record to still be returned")
		}
		if outcome.PrimaryFailed {
			t.Error("a primary that answered with nothing has not failed")
		}
		if !outcome.SafeToBind("OL7468980A") {
			t.Error("a genuine miss may still be bound: that is #2237's case, not this one")
		}
	})

	t.Run("primary was rate limited", func(t *testing.T) {
		hc := &mockProvider{name: "hardcover", searchAuthErr: errors.New("HTTP 429: API rate limit exceeded for tier 'Free'. Try again in 1 seconds.")}
		agg := NewAggregator(hc, ol)
		authors, outcome, err := agg.SearchAuthorsWithOutcome(context.Background(), "Adrian Tchaikovsky")
		if err != nil {
			t.Fatalf("one provider failing must not fail the search: %v", err)
		}
		if len(authors) == 0 {
			t.Fatal("the surviving provider's records should still come back")
		}
		if !outcome.PrimaryFailed {
			t.Fatal("a rate-limited primary must be reported as failed")
		}
		if outcome.Primary != "hardcover" {
			t.Errorf("outcome.Primary = %q, want hardcover", outcome.Primary)
		}
		if outcome.FailureSummary() != "hardcover" {
			t.Errorf("FailureSummary() = %q, want hardcover", outcome.FailureSummary())
		}
		if outcome.SafeToBind("OL7468980A") {
			t.Error("binding an OpenLibrary id while the primary was throttled is exactly the permanent downgrade in #2271")
		}
		if !outcome.SafeToBind("hc:adrian-tchaikovsky") {
			t.Error("a record that IS from the primary is always safe to bind")
		}
	})
}

// TestSearchOutcomeUnconfiguredProviderIsNotAFailure: an install with no
// Hardcover token must not look permanently degraded. ErrProviderNotConfigured
// means the provider was never in the running, which is a different thing from
// dropping out of it.
func TestSearchOutcomeUnconfiguredProviderIsNotAFailure(t *testing.T) {
	hc := &mockProvider{name: "hardcover", searchAuthErr: ErrProviderNotConfigured}
	ol := &mockProvider{name: "openlibrary", searchAuthors: []models.Author{{Name: "A", ForeignID: "OL1A"}}}
	agg := NewAggregator(ol, hc)

	_, outcome, err := agg.SearchAuthorsWithOutcome(context.Background(), "A")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(outcome.FailedProviders) != 0 {
		t.Errorf("an unconfigured provider must not count as failed, got %v", outcome.FailedProviders)
	}
	if !outcome.SafeToBind("OL1A") {
		t.Error("nothing failed, so binding is safe")
	}
}

func TestSearchOutcomeSafeToBindWithNoPrimary(t *testing.T) {
	// No configured primary means no provider is privileged, so there is no
	// downgrade to protect against and every match is bindable.
	var outcome SearchOutcome
	if !outcome.SafeToBind("OL1A") {
		t.Error("the zero outcome must permit binding")
	}
	outcome = SearchOutcome{PrimaryFailed: true}
	if !outcome.SafeToBind("OL1A") {
		t.Error("PrimaryFailed with no named primary must not block binding")
	}
}

// TestSearchAuthorsStillDelegates guards the compatibility of the original
// two-value signature, which a dozen non-binding call sites still use.
func TestSearchAuthorsStillDelegates(t *testing.T) {
	hc := &mockProvider{name: "hardcover", searchAuthErr: errors.New("HTTP 429")}
	ol := &mockProvider{name: "openlibrary", searchAuthors: []models.Author{{Name: "A", ForeignID: "OL1A"}}}
	agg := NewAggregator(hc, ol)

	authors, err := agg.SearchAuthors(context.Background(), "A")
	if err != nil {
		t.Fatalf("SearchAuthors: %v", err)
	}
	if len(authors) != 1 {
		t.Errorf("expected the surviving provider's one record, got %d", len(authors))
	}
}

// TestSearchAuthorsAllProvidersFailStillErrors: the pre-existing contract is
// that an error surfaces only when nothing answered at all.
func TestSearchAuthorsAllProvidersFailStillErrors(t *testing.T) {
	boom := errors.New("HTTP 503")
	hc := &mockProvider{name: "hardcover", searchAuthErr: boom}
	ol := &mockProvider{name: "openlibrary", searchAuthErr: boom}
	agg := NewAggregator(hc, ol)

	_, outcome, err := agg.SearchAuthorsWithOutcome(context.Background(), "A")
	if err == nil {
		t.Fatal("every provider failing must still surface an error")
	}
	if !outcome.PrimaryFailed || len(outcome.FailedProviders) != 2 {
		t.Errorf("outcome should name both failures, got %+v", outcome)
	}
}

// TestSearchBooksWithOutcomeReportsPrimaryFailure: the book search fan out
// carries the same distinction as the author search, for the Goodreads
// importer's title and author fallback (#2332).
func TestSearchBooksWithOutcomeReportsPrimaryFailure(t *testing.T) {
	const dnbAuthor = "dnb:gnd:1052464211"
	dnb := &mockProvider{name: "dnb", searchBooks: []models.Book{{
		ForeignID: "dnb:bib-1", Title: "Project Hail Mary",
		Author: &models.Author{ForeignID: dnbAuthor, Name: "Andy Weir"},
	}}}
	for _, tc := range []struct {
		name       string
		olErr      error
		wantFailed bool
	}{
		{"primary answered with nothing", nil, false},
		{"primary timed out", context.DeadlineExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ol := &mockProvider{name: "openlibrary", searchBookErr: tc.olErr}
			books, outcome, err := NewAggregator(ol, dnb).SearchBooksWithOutcome(context.Background(), "Project Hail Mary")
			if err != nil {
				t.Fatalf("one provider failing must not fail the search: %v", err)
			}
			if len(books) != 1 {
				t.Fatalf("books = %d, want the fallback's record either way", len(books))
			}
			if outcome.PrimaryFailed != tc.wantFailed {
				t.Errorf("PrimaryFailed = %v, want %v", outcome.PrimaryFailed, tc.wantFailed)
			}
			if got := outcome.SafeToBind(dnbAuthor); got != !tc.wantFailed {
				t.Errorf("SafeToBind(dnb) = %v, want %v", got, !tc.wantFailed)
			}
		})
	}
}

// TestResolveBookByISBNWithOutcomeReportsPrimaryFailure: the ISBN walk steps
// past a failing primary to the next provider, and must say it did, or the
// Goodreads importer binds the fallback's author (#2332). A provider with no
// credentials never took part and is not a failure.
func TestResolveBookByISBNWithOutcomeReportsPrimaryFailure(t *testing.T) {
	const dnbAuthor = "dnb:gnd:1052464211"
	dnb := &mockProvider{name: "dnb", getByISBN: &models.Book{
		ForeignID: "dnb:bib-1", Title: "Project Hail Mary",
		Author: &models.Author{ForeignID: dnbAuthor, Name: "Andy Weir"},
	}}
	for _, tc := range []struct {
		name       string
		olErr      error
		wantFailed bool
	}{
		{"primary answered with nothing", nil, false},
		{"primary timed out", context.DeadlineExceeded, true},
		{"primary not configured", ErrProviderNotConfigured, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ol := &mockProvider{name: "openlibrary", getByISBNErr: tc.olErr}
			book, outcome, err := NewAggregator(ol, dnb).ResolveBookByISBNWithOutcome(context.Background(), "9780593135204")
			if err != nil {
				t.Fatalf("ResolveBookByISBNWithOutcome: %v", err)
			}
			if book == nil || book.Author == nil || book.Author.ForeignID != dnbAuthor {
				t.Fatalf("book = %+v, want the dnb hit", book)
			}
			if outcome.PrimaryFailed != tc.wantFailed {
				t.Errorf("PrimaryFailed = %v, want %v (failed=%v)", outcome.PrimaryFailed, tc.wantFailed, outcome.FailedProviders)
			}
			if got := outcome.SafeToBind(dnbAuthor); got != !tc.wantFailed {
				t.Errorf("SafeToBind(dnb) = %v, want %v", got, !tc.wantFailed)
			}
		})
	}
}
