package metadata

import (
	"context"
	"errors"
	"github.com/vavallee/bindery/internal/models"
	"testing"
)

func TestQuotaDeferredPreservesPrimaryIdentity(t *testing.T) {
	primary := &mockProvider{name: "hardcover", getByISBNErr: ErrProviderDeferred}
	fallback := &mockProvider{name: "openlibrary", getByISBN: &models.Book{ForeignID: "OL123W"}}
	agg := NewAggregator(primary, fallback)
	book, err := agg.GetBookByISBN(context.Background(), "9780441172719")
	if book != nil || !errors.Is(err, ErrProviderDeferred) || fallback.getByISBNCalls != 0 {
		t.Fatalf("deferred lookup became a fallback identity: %+v %v", book, err)
	}
	primary.getByISBNErr = nil
	primary.getByISBN = &models.Book{ForeignID: "hc:123", Title: "Dune"}
	book, err = agg.GetBookByISBN(context.Background(), "9780441172719")
	if err != nil || book == nil || book.ForeignID != "hc:123" {
		t.Fatalf("did not resume: %+v %v", book, err)
	}
}
