package api

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// evidenceSeriesProvider is a primary provider that answers language evidence
// queries, the way Hardcover does, with a fixed state or a fixed error.
type evidenceSeriesProvider struct {
	*stubSeriesProvider
	state metadata.AuthorWorkLanguageEvidenceState
	err   error
}

func (p *evidenceSeriesProvider) GetAuthorWorkLanguageEvidence(_ context.Context, books []models.Book, _ []string) (map[string]metadata.AuthorWorkLanguageEvidence, error) {
	if p.err != nil {
		return nil, p.err
	}
	out := make(map[string]metadata.AuthorWorkLanguageEvidence, len(books))
	for _, b := range books {
		out[b.ForeignID] = metadata.AuthorWorkLanguageEvidence{State: p.state}
	}
	return out, nil
}

// TestCatalogBookProfileFilterLanguage pins the language rules a series fill
// applies (#2208 review). Hardcover series catalogue entries carry no
// language, so a blank language must pass even under "unknown fails", or add
// all rejects every book whenever OpenLibrary is primary and no evidence can
// rescue it. A failed evidence lookup (an outage, or a held request) is no
// answer and must pass too; only a definitive "not allowed" rejects.
func TestCatalogBookProfileFilterLanguage(t *testing.T) {
	ctx := context.Background()
	profile := &models.MetadataProfile{AllowedLanguages: "eng", UnknownLanguageBehavior: models.UnknownLanguageFail}
	openLibraryLike := metadata.NewAggregator(&stubSeriesProvider{}).WithAudnexClient(nil)
	evidence := func(state metadata.AuthorWorkLanguageEvidenceState, err error) *metadata.Aggregator {
		return metadata.NewAggregator(&evidenceSeriesProvider{stubSeriesProvider: &stubSeriesProvider{}, state: state, err: err}).WithAudnexClient(nil)
	}
	cases := []struct {
		name     string
		meta     *metadata.Aggregator
		language string
		want     string
	}{
		{"blank language, no evidence provider", openLibraryLike, "", ""},
		{"blank language, evidence lookup fails", evidence(0, errors.New("held")), "", ""},
		{"allowed language", openLibraryLike, "eng", ""},
		{"foreign language, no evidence provider", openLibraryLike, "spa", profileFilterLanguage},
		{"foreign language, evidence lookup fails", evidence(0, errors.New("held")), "spa", ""},
		{"foreign language, evidence says allowed", evidence(metadata.AuthorWorkLanguageAllowed, nil), "spa", ""},
		{"foreign language, evidence says not allowed", evidence(metadata.AuthorWorkLanguageNotAllowed, nil), "spa", profileFilterLanguage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &models.Book{ForeignID: "hc:book", Title: "A Book", Language: tc.language}
			if got := catalogBookProfileFilter(ctx, tc.meta, profile, b); got != tc.want {
				t.Fatalf("catalogBookProfileFilter = %q, want %q", got, tc.want)
			}
		})
	}
}
