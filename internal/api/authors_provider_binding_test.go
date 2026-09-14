package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

const providerRateLimit429 = "HTTP 429: API rate limit exceeded for tier 'Free'. Try again in 1 seconds."

// TestRelinkUpstreamRefusesFallbackWhilePrimaryUnavailable: the relink
// endpoint resolves a name against every provider and writes the winner's id
// into the author row, which is what decides the provider every later
// catalogue sync uses. With the primary throttled, the fallback wins by
// walkover and the write is permanent (#2271), so the endpoint declines and
// says to retry instead.
func TestRelinkUpstreamRefusesFallbackWhilePrimaryUnavailable(t *testing.T) {
	primary := &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "hardcover"},
		searchAuthorsErr: errors.New(providerRateLimit429),
	}
	fallback := &searchableAuthorProvider{
		stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{"Adrian Tchaikovsky": {{Name: "Adrian Tchaikovsky", ForeignID: "OL7468980A"}}},
		authors:              map[string]*models.Author{"OL7468980A": {Name: "Adrian Tchaikovsky", ForeignID: "OL7468980A"}},
	}
	fixture := newRelinkUpstreamFixture(t, primary, fallback)
	author := fixture.createAuthor(t, &models.Author{Name: "Adrian Tchaikovsky", ForeignID: "abs:tchaikovsky"})

	rec := fixture.relink(t, author.ID)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (retry later), body %s", rec.Code, rec.Body.String())
	}

	stored, err := fixture.authors.GetByID(fixture.ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ForeignID != "abs:tchaikovsky" {
		t.Errorf("the author must keep its existing identity rather than be bound to the fallback, got %q", stored.ForeignID)
	}
}

// TestRelinkUpstreamStillLinksWhenPrimaryMerelyMisses is the counterweight: a
// primary that answers and has no such record is #2237, where the fallback
// link is the correct outcome. Refusing here would break relinking for every
// author the primary has never heard of.
func TestRelinkUpstreamStillLinksWhenPrimaryMerelyMisses(t *testing.T) {
	primary := &searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "hardcover"}}
	fallback := &searchableAuthorProvider{
		stubMetaProvider:     stubMetaProvider{name: "openlibrary"},
		searchAuthorsByQuery: map[string][]models.Author{"Adrian Tchaikovsky": {{Name: "Adrian Tchaikovsky", ForeignID: "OL7468980A"}}},
		authors:              map[string]*models.Author{"OL7468980A": {Name: "Adrian Tchaikovsky", ForeignID: "OL7468980A"}},
	}
	fixture := newRelinkUpstreamFixture(t, primary, fallback)
	author := fixture.createAuthor(t, &models.Author{Name: "Adrian Tchaikovsky", ForeignID: "abs:tchaikovsky"})

	rec := fixture.relink(t, author.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	stored, err := fixture.authors.GetByID(fixture.ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ForeignID != "OL7468980A" {
		t.Errorf("foreignID = %q, want the fallback link to have been written", stored.ForeignID)
	}
}

// TestRelinkCalibreAuthorStoresProviderOfMatchedID: the Calibre relink wrote
// metadata_provider=openlibrary after every match, so a dnb: or hc: link, and
// even the primary's own match on a Hardcover or DNB primary, claimed to be
// OpenLibrary's. The label must be the provider the stored id belongs to.
func TestRelinkCalibreAuthorStoresProviderOfMatchedID(t *testing.T) {
	const name = "Andy Weir"
	answering := func(provider, id string) *searchableAuthorProvider {
		return &searchableAuthorProvider{
			stubMetaProvider:     stubMetaProvider{name: provider},
			searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name, ForeignID: id}}},
			authors:              map[string]*models.Author{id: {Name: name, ForeignID: id}},
		}
	}
	for _, tc := range []struct {
		name         string
		primary      *searchableAuthorProvider
		enricher     *searchableAuthorProvider
		wantID       string
		wantProvider string
	}{
		{"openlibrary primary misses, dnb answers",
			&searchableAuthorProvider{stubMetaProvider: stubMetaProvider{name: "openlibrary"}},
			answering("dnb", "dnb:gnd:1052464211"), "dnb:gnd:1052464211", "dnb"},
		{"hardcover primary answers", answering("hardcover", "hc:andy-weir"), nil, "hc:andy-weir", "hardcover"},
		{"dnb primary answers", answering("dnb", "dnb:gnd:1052464211"), nil, "dnb:gnd:1052464211", "dnb"},
		{"openlibrary primary answers", answering("openlibrary", "OL7373385A"), nil, "OL7373385A", "openlibrary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRelinkUpstreamFixture(t, tc.primary)
			if tc.enricher != nil {
				fixture = newRelinkUpstreamFixture(t, tc.primary, tc.enricher)
			}
			author := fixture.createAuthor(t, &models.Author{Name: name, ForeignID: "calibre:author:1", MetadataProvider: "calibre"})

			if err := fixture.handler.relinkCalibreAuthor(fixture.ctx, author); err != nil {
				t.Fatalf("relinkCalibreAuthor: %v", err)
			}
			stored, err := fixture.authors.GetByID(fixture.ctx, author.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ForeignID != tc.wantID || stored.MetadataProvider != tc.wantProvider {
				t.Errorf("stored (%s, %s), want (%s, %s)", stored.ForeignID, stored.MetadataProvider, tc.wantID, tc.wantProvider)
			}
		})
	}
}

// TestRelinkCalibreAuthorRefusesNameOnlyMatchWhilePrimaryDown: SafeToBind now
// refuses an empty foreign id when the primary failed. For the Calibre relink
// that turns a misleading "no metadata match" (GetAuthor("") is routed to the
// primary that just failed) into the retry later refusal. Nothing was bound
// either way.
func TestRelinkCalibreAuthorRefusesNameOnlyMatchWhilePrimaryDown(t *testing.T) {
	const name = "Andy Weir"
	primary := &searchableAuthorProvider{
		stubMetaProvider: stubMetaProvider{name: "openlibrary"},
		searchAuthorsErr: errors.New("openlibrary: context deadline exceeded"),
	}
	nameOnly := &searchableAuthorProvider{
		stubMetaProvider:     stubMetaProvider{name: "googlebooks"},
		searchAuthorsByQuery: map[string][]models.Author{name: {{Name: name}}},
	}
	fixture := newRelinkUpstreamFixture(t, primary, nameOnly)
	author := fixture.createAuthor(t, &models.Author{Name: name, ForeignID: "calibre:author:1", MetadataProvider: "calibre"})

	if err := fixture.handler.relinkCalibreAuthor(fixture.ctx, author); !errors.Is(err, errPrimaryProviderUnavailable) {
		t.Errorf("relinkCalibreAuthor error = %v, want errPrimaryProviderUnavailable", err)
	}
	stored, err := fixture.authors.GetByID(fixture.ctx, author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ForeignID != "calibre:author:1" {
		t.Errorf("foreignID = %q, want the calibre id kept", stored.ForeignID)
	}
}
