package metadata

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// The review question behind these tests (#2041): a library built with
// OpenLibrary primary carries OpenLibrary foreign IDs on every author and
// book. If the operator then sets metadata.primary_provider=hardcover and
// restarts, does the next author sync come back with Hardcover IDs for the
// same works — matching nothing by foreign ID, relying entirely on title
// dedup, and creating a second row per book when that dedup misses?
//
// It does not, and the reason is that the catalogue source is not chosen by
// which provider is primary. primaryAuthorWorks routes through
// providerForForeignID, which picks the provider named by the AUTHOR's own
// foreign-ID prefix and only falls back to the primary when no prefix matches.
// A bare "OL…" author ID resolves to "openlibrary", and main.go appends every
// non-primary provider as an enricher — so with Hardcover primary, OpenLibrary
// is still in a.providers() and is still the provider that answers for
// OpenLibrary-linked authors.
//
// The flip therefore governs which provider NEW authors are resolved against.
// Authors already in the library keep the provider they were linked to until
// something rewrites author.ForeignID, which is what the per-author relink
// endpoint does and what a global flip deliberately does not.

func flipTestBooks(prefix string, titles ...string) []models.Book {
	books := make([]models.Book, 0, len(titles))
	for _, title := range titles {
		books = append(books, models.Book{
			ForeignID: prefix + title,
			Title:     title,
			// Non-empty so enrichMissingAuthorWorkCovers has no work to do and
			// the test never fans out to the enricher for cover lookups.
			ImageURL: "https://example.invalid/" + title + ".jpg",
		})
	}
	return books
}

// TestPrimaryFlipToHardcover_OpenLibraryAuthorsStayOnOpenLibrary is the
// mechanism proof: with Hardcover wired as primary, an author whose stored
// foreign ID is an OpenLibrary one is still answered by OpenLibrary, so the
// works come back with the same foreign IDs the library already stores and
// reconcile by ID rather than falling through to title dedup.
func TestPrimaryFlipToHardcover_OpenLibraryAuthorsStayOnOpenLibrary(t *testing.T) {
	ol := &mockWorksProvider{
		mockProvider: mockProvider{
			name:        "openlibrary",
			authorWorks: flipTestBooks("OL", "Raise the Titanic", "Poseidon's Arrow"),
		},
	}
	hc := &mockWorksProvider{
		mockProvider: mockProvider{
			name:        "hardcover",
			authorWorks: flipTestBooks("hc:", "Raise the Titanic", "Poseidon's Arrow"),
		},
	}

	// Exactly the post-flip wiring from cmd/bindery/main.go: hardcover primary,
	// every other provider demoted to enricher.
	agg := newTestAggregator(hc, ol)

	got, err := agg.GetAuthorWorks(context.Background(), "OL26320A")
	if err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}

	if hc.authorWorksCalls != 0 {
		t.Errorf("Hardcover answered for an OpenLibrary author (%d calls). "+
			"The flip would re-source an existing catalogue and every work would "+
			"arrive with an hc: id that matches no existing row by foreign id.",
			hc.authorWorksCalls)
	}
	if ol.authorWorksCalls != 1 {
		t.Errorf("OpenLibrary author works calls = %d, want 1", ol.authorWorksCalls)
	}
	for _, b := range got {
		if len(b.ForeignID) < 2 || b.ForeignID[:2] != "OL" {
			t.Errorf("work %q came back with foreign id %q, want an OL id — "+
				"an id change here is what would create a duplicate row", b.Title, b.ForeignID)
		}
	}
}

// TestPrimaryFlipToHardcover_HardcoverAuthorsUseHardcover is the other half of
// the routing contract: the flip is not inert. An author carrying an hc: id —
// one added after the flip, or moved across by the relink endpoint — is
// answered by Hardcover.
func TestPrimaryFlipToHardcover_HardcoverAuthorsUseHardcover(t *testing.T) {
	ol := &mockWorksProvider{
		mockProvider: mockProvider{
			name:        "openlibrary",
			authorWorks: flipTestBooks("OL", "Raise the Titanic"),
		},
	}
	hc := &mockWorksProvider{
		mockProvider: mockProvider{
			name:        "hardcover",
			authorWorks: flipTestBooks("hc:", "Raise the Titanic"),
		},
	}
	agg := newTestAggregator(hc, ol)

	if _, err := agg.GetAuthorWorks(context.Background(), "hc:4321"); err != nil {
		t.Fatalf("GetAuthorWorks: %v", err)
	}
	if hc.authorWorksCalls != 1 {
		t.Errorf("Hardcover author works calls = %d, want 1", hc.authorWorksCalls)
	}
	if ol.authorWorksCalls != 0 {
		t.Errorf("OpenLibrary answered for a Hardcover author (%d calls)", ol.authorWorksCalls)
	}
}

// TestPrimaryFlipDoesNotChangeAuthorRouting states the property directly: for
// any given author foreign ID, the provider that answers is the same before
// and after the flip. This is the invariant that makes a global primary change
// safe for an existing catalogue, and it is the one that would have to break
// for the doubled-library failure mode to become reachable.
func TestPrimaryFlipDoesNotChangeAuthorRouting(t *testing.T) {
	authorIDs := []string{
		"OL26320A",  // OpenLibrary — the shape every pre-flip author has
		"hc:4321",   // Hardcover
		"dnb:11881", // DNB
	}

	newProviders := func() (ol, hc, dnb Provider) {
		return &mockProvider{name: "openlibrary"},
			&mockProvider{name: "hardcover"},
			&mockProvider{name: "dnb"}
	}

	olA, hcA, dnbA := newProviders()
	before := newTestAggregator(olA, hcA, dnbA)

	olB, hcB, dnbB := newProviders()
	after := newTestAggregator(hcB, olB, dnbB)

	for _, id := range authorIDs {
		gotBefore := providerName(before.providerForForeignID(id))
		gotAfter := providerName(after.providerForForeignID(id))
		if gotBefore != gotAfter {
			t.Errorf("author %q is answered by %q before the flip and %q after it; "+
				"re-sourcing an existing catalogue is exactly what duplicates it",
				id, gotBefore, gotAfter)
		}
	}
}
