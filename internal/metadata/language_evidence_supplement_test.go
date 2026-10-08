package metadata

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// With OpenLibrary primary and Hardcover supplementing, the hc: works in the
// catalogue still get Hardcover's language evidence (#3091). Before, only the
// primary was asked, so a supplement's translations were never resolved.
func TestAggregator_GetAuthorWorkLanguageEvidence_AsksSupplementingProviders(t *testing.T) {
	want := map[string]AuthorWorkLanguageEvidence{
		"hc:o-ponto-azul-claro": {State: AuthorWorkLanguageNotAllowed, Language: "por"},
		"hc:pale-blue-dot":      {State: AuthorWorkLanguageAllowed, Language: "eng"},
	}
	hardcover := &mockAuthorWorkLanguageEvidenceProvider{mockProvider: mockProvider{name: "hardcover"}, evidence: want}
	agg := newTestAggregator(&mockProvider{name: "openlibrary"}, &mockProvider{name: "googlebooks"}, hardcover)

	books := []models.Book{{ForeignID: "OL1W"}, {ForeignID: "hc:o-ponto-azul-claro"}, {ForeignID: "hc:pale-blue-dot"}}
	got, err := agg.GetAuthorWorkLanguageEvidence(context.Background(), books, []string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	if hardcover.calls != 1 {
		t.Fatalf("supplementing Hardcover asked %d times, want 1", hardcover.calls)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
}

// A supplement's failure, or a supplement with no token, costs only that
// supplement's evidence: the primary's evidence still stands, and no error is
// returned for something the primary did not do.
func TestAggregator_GetAuthorWorkLanguageEvidence_SupplementFailureIsNotFatal(t *testing.T) {
	primaryEvidence := map[string]AuthorWorkLanguageEvidence{"p:1": {State: AuthorWorkLanguageAllowed, Language: "eng"}}
	primary := &mockAuthorWorkLanguageEvidenceProvider{mockProvider: mockProvider{name: "primary"}, evidence: primaryEvidence}
	for _, supplementErr := range []error{errors.New("rate limited"), ErrProviderNotConfigured} {
		supplement := &mockAuthorWorkLanguageEvidenceProvider{
			mockProvider: mockProvider{name: "hardcover"},
			evidence:     map[string]AuthorWorkLanguageEvidence{"hc:x": {State: AuthorWorkLanguageNotAllowed}},
			err:          supplementErr,
		}
		agg := newTestAggregator(primary, supplement)
		got, err := agg.GetAuthorWorkLanguageEvidence(context.Background(), []models.Book{{ForeignID: "p:1"}, {ForeignID: "hc:x"}}, []string{"eng"})
		if err != nil {
			t.Fatalf("%v: supplement failure surfaced as an error: %v", supplementErr, err)
		}
		if !reflect.DeepEqual(got, primaryEvidence) {
			t.Fatalf("%v: evidence = %#v, want only the primary's", supplementErr, got)
		}
	}
}
