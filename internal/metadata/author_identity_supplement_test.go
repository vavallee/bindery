package metadata

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// identitySupplementProvider can answer by name or by identity, and records
// which one the aggregator asked for.
type identitySupplementProvider struct {
	nameCalls     []string
	identityCalls []string
	byName        []models.Book
	byIdentity    []models.Book
}

func (p *identitySupplementProvider) Name() string { return "hardcover" }

func (p *identitySupplementProvider) GetAuthorWorksByName(_ context.Context, name string) ([]models.Book, error) {
	p.nameCalls = append(p.nameCalls, name)
	return cloneBooks(p.byName), nil
}

func (p *identitySupplementProvider) GetAuthorWorksByIdentity(_ context.Context, id string) ([]models.Book, error) {
	p.identityCalls = append(p.identityCalls, id)
	return cloneBooks(p.byIdentity), nil
}

func hcBookFor(title string) models.Book {
	return models.Book{
		ForeignID: "hc:" + strings.ToLower(strings.ReplaceAll(title, " ", "-")),
		Title:     title, SortTitle: strings.ToLower(title),
		MetadataProvider: "hardcover",
	}
}

// TestAuthorWorksSupplement_PrefersIdentity is the #1734 behaviour at the seam:
// when the row carries a Hardcover identity, the supplement must use it rather
// than the author's name, which cannot separate two people who share one.
func TestAuthorWorksSupplement_PrefersIdentity(t *testing.T) {
	p := &identitySupplementProvider{byIdentity: []models.Book{hcBookFor("Right Book")}}
	a := &Aggregator{}

	author := models.Author{
		ID: 1, Name: "J.A. Andrews", ForeignID: "OL123A",
		ProviderIdentifiers: map[string]string{"hardcover": "hc:the-right-andrews"},
	}
	books, err := a.authorWorksSupplement(context.Background(), p, author, author.Name)
	if err != nil {
		t.Fatalf("authorWorksSupplement: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Right Book" {
		t.Errorf("got %+v, want the identity result", books)
	}
	if len(p.nameCalls) != 0 {
		t.Errorf("fell back to the name query: %v", p.nameCalls)
	}
	if len(p.identityCalls) != 1 || p.identityCalls[0] != "hc:the-right-andrews" {
		t.Errorf("identity calls = %v, want the linked Hardcover id", p.identityCalls)
	}
}

// TestAuthorWorksSupplement_UsesPrimaryForeignIDWhenItIsTheProvider: an author
// linked directly to Hardcover needs no identifiers row.
func TestAuthorWorksSupplement_UsesPrimaryForeignIDWhenItIsTheProvider(t *testing.T) {
	p := &identitySupplementProvider{byIdentity: []models.Book{hcBookFor("Right Book")}}
	a := &Aggregator{}

	author := models.Author{ID: 1, Name: "J.A. Andrews", ForeignID: "hc:primary-andrews"}
	if _, err := a.authorWorksSupplement(context.Background(), p, author, author.Name); err != nil {
		t.Fatalf("authorWorksSupplement: %v", err)
	}
	if len(p.identityCalls) != 1 || p.identityCalls[0] != "hc:primary-andrews" {
		t.Errorf("identity calls = %v, want the primary foreign id", p.identityCalls)
	}
}

// TestAuthorWorksSupplement_FallsBackToNameWhenUnlinked: an author with no
// Hardcover identity has nothing better available, and the name query is still
// how the first link gets made.
func TestAuthorWorksSupplement_FallsBackToNameWhenUnlinked(t *testing.T) {
	p := &identitySupplementProvider{byName: []models.Book{hcBookFor("Some Book")}}
	a := &Aggregator{}

	author := models.Author{ID: 1, Name: "Unlinked Author", ForeignID: "OL999A"}
	books, err := a.authorWorksSupplement(context.Background(), p, author, author.Name)
	if err != nil {
		t.Fatalf("authorWorksSupplement: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if len(p.identityCalls) != 0 {
		t.Errorf("queried by identity for an unlinked author: %v", p.identityCalls)
	}
	if len(p.nameCalls) != 1 || p.nameCalls[0] != "Unlinked Author" {
		t.Errorf("name calls = %v", p.nameCalls)
	}
}

// TestAuthorWorksSupplement_IdentityWithNoWorksDoesNotWiden is the negative
// that matters. Falling back to the name query when an identity finds nothing
// would re-merge the same-named authors at exactly the point precision was
// requested.
func TestAuthorWorksSupplement_IdentityWithNoWorksDoesNotWiden(t *testing.T) {
	p := &identitySupplementProvider{byName: []models.Book{hcBookFor("Wrong Andrews Book")}}
	a := &Aggregator{}

	author := models.Author{
		ID: 1, Name: "J.A. Andrews", ForeignID: "OL123A",
		ProviderIdentifiers: map[string]string{"hardcover": "hc:the-right-andrews"},
	}
	books, err := a.authorWorksSupplement(context.Background(), p, author, author.Name)
	if err != nil {
		t.Fatalf("authorWorksSupplement: %v", err)
	}
	if len(books) != 0 {
		t.Errorf("got %+v, want nothing rather than the other Andrews' books", books)
	}
	if len(p.nameCalls) != 0 {
		t.Errorf("widened to the name query: %v", p.nameCalls)
	}
}

// TestAuthorIdentityCacheKey_StableAndDistinct: the identity takes part in the
// author-works cache key, so a relink cannot be served the pre-relink answer.
func TestAuthorIdentityCacheKey_StableAndDistinct(t *testing.T) {
	one := models.Author{ProviderIdentifiers: map[string]string{"hardcover": "hc:a", "openlibrary": "OL1A"}}
	same := models.Author{ProviderIdentifiers: map[string]string{"openlibrary": "OL1A", "hardcover": "hc:a"}}
	other := models.Author{ProviderIdentifiers: map[string]string{"hardcover": "hc:b"}}

	if authorIdentityCacheKey(one) != authorIdentityCacheKey(same) {
		t.Error("key depends on map iteration order")
	}
	if authorIdentityCacheKey(one) == authorIdentityCacheKey(other) {
		t.Error("different identities produced the same key")
	}
	if authorIdentityCacheKey(models.Author{}) != "" {
		t.Error("an author with no identifiers should produce an empty key")
	}
}
