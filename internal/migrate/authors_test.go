package migrate

import (
	"context"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// Fixtures for the #2332 guard tests. The CSV, Readarr and Goodreads importer
// tests share them because all three bind an author through the same
// SafeToBind check and the same provider stamp (boundProvider, authors.go).

// errOpenLibraryTimeout is what an HTTP provider client hands back when its
// request deadline expires, which is what #2332's repro produces by pointing
// OpenLibrary at a black hole.
var errOpenLibraryTimeout = fmt.Errorf("openlibrary: %w", context.DeadlineExceeded)

const (
	guardAuthorName = "Andy Weir"
	// dnbAuthorID is the record only the fallback provider returns.
	dnbAuthorID = "dnb:gnd:1052464211"
	// hcAuthorID is the same person on a Hardcover primary.
	hcAuthorID = "hc:andy-weir"
)

func guardAuthor(foreignID string) models.Author {
	return models.Author{ForeignID: foreignID, Name: guardAuthorName, SortName: "Weir, Andy"}
}

// answeringProvider knows guardAuthorName under foreignID for author search,
// author fetch, ISBN lookup and book search. The records leave
// MetadataProvider empty, so any label the importer stores is its own doing.
// Every call returns fresh values: the importers mutate what they are handed.
func answeringProvider(name, foreignID string) *stubProvider {
	book := func() *models.Book {
		a := guardAuthor(foreignID)
		return &models.Book{ForeignID: foreignID + ":book", Title: "Project Hail Mary", Author: &a}
	}
	return &stubProvider{
		name: name,
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) {
			return []models.Author{guardAuthor(foreignID)}, nil
		},
		getAuthorFn: func(_ context.Context, id string) (*models.Author, error) {
			a := guardAuthor(id)
			return &a, nil
		},
		getBookByISBNFn: func(context.Context, string) (*models.Book, error) { return book(), nil },
		searchBooksFn: func(context.Context, string) ([]models.Book, error) {
			return []models.Book{*book()}, nil
		},
	}
}

// failingProvider fails every lookup with err, or, when err is nil, answers
// every lookup with nothing: a genuine miss, which is #2237's case and not a
// failure.
func failingProvider(name string, err error) *stubProvider {
	return &stubProvider{
		name:            name,
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) { return nil, err },
		getBookByISBNFn: func(context.Context, string) (*models.Book, error) { return nil, err },
		searchBooksFn:   func(context.Context, string) ([]models.Book, error) { return nil, err },
	}
}

// olTimesOutDNBAnswers is #2332's repro: OpenLibrary is the primary and times
// out, DNB answers with a record for the same person.
func olTimesOutDNBAnswers() *metadata.Aggregator {
	return metadata.NewAggregator(failingProvider("openlibrary", errOpenLibraryTimeout), answeringProvider("dnb", dnbAuthorID))
}

// allowedBindCases are setups where binding IS allowed, with the provider the
// stored author must then carry.
var allowedBindCases = []struct {
	name         string
	agg          func() *metadata.Aggregator
	wantID       string
	wantProvider string
}{
	{
		// The primary answered and has no record. The DNB record may be
		// bound, and must say it is DNB's.
		name: "openlibrary primary misses and dnb answers",
		agg: func() *metadata.Aggregator {
			return metadata.NewAggregator(failingProvider("openlibrary", nil), answeringProvider("dnb", dnbAuthorID))
		},
		wantID:       dnbAuthorID,
		wantProvider: "dnb",
	},
	{
		// A Hardcover primary answering: the record is the primary's own and
		// must not be labelled OpenLibrary's.
		name: "hardcover primary answers",
		agg: func() *metadata.Aggregator {
			return metadata.NewAggregator(answeringProvider("hardcover", hcAuthorID))
		},
		wantID:       hcAuthorID,
		wantProvider: "hardcover",
	},
}

// assertNoAuthorBound fails if the import created any author, and names the
// fallback binding when that is what it created.
func assertNoAuthorBound(t *testing.T, repo *db.AuthorRepo, foreignID string) {
	t.Helper()
	ctx := context.Background()
	got, err := repo.GetByAnyForeignID(ctx, foreignID)
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if got != nil {
		t.Errorf("author %q was bound to %s (metadata_provider=%s) while the primary provider was down",
			got.Name, got.ForeignID, got.MetadataProvider)
	}
	all, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("authors created = %d, want 0", len(all))
	}
}

// assertAuthorBound checks the author was created under foreignID and stores
// the provider that id belongs to.
func assertAuthorBound(t *testing.T, repo *db.AuthorRepo, foreignID, wantProvider string) {
	t.Helper()
	got, err := repo.GetByAnyForeignID(context.Background(), foreignID)
	if err != nil {
		t.Fatalf("GetByAnyForeignID: %v", err)
	}
	if got == nil {
		t.Fatalf("no author created under %s", foreignID)
	}
	if got.MetadataProvider != wantProvider {
		t.Errorf("metadata_provider = %q for %s, want %q", got.MetadataProvider, got.ForeignID, wantProvider)
	}
}

// googleBooksNameOnly answers author search the way the real Google Books
// client does: it has no author endpoint, so its records carry the name and
// no foreign id (googlebooks/client.go, volumeToBook). The aggregator stamps
// them "googlebooks".
func googleBooksNameOnly() *stubProvider {
	return &stubProvider{
		name: "googlebooks",
		searchAuthorsFn: func(context.Context, string) ([]models.Author, error) {
			return []models.Author{{Name: guardAuthorName, SortName: "Weir, Andy"}}, nil
		},
	}
}

// olTimesOutGoogleBooksAndDNBAnswer is the scenario the review of #2610
// found: a Google Books key configured, OpenLibrary black holed, DNB
// answering. Google Books is registered ahead of DNB (cmd/bindery/main.go),
// so its name only record wins the same name tie and DNB's is collapsed into
// it. An empty foreign id used to classify as openlibrary and pass the guard.
func olTimesOutGoogleBooksAndDNBAnswer() *metadata.Aggregator {
	return metadata.NewAggregator(failingProvider("openlibrary", errOpenLibraryTimeout),
		googleBooksNameOnly(), answeringProvider("dnb", dnbAuthorID))
}

// wantPrimaryDownReason is the row reason every importer gives when the primary
// did not answer and nothing usable came back from the others.
const wantPrimaryDownReason = "primary metadata provider openlibrary did not answer, run the import again once it responds"
