package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// olAuthorID is guardAuthorName's OpenLibrary record, once OpenLibrary
// answers again.
const olAuthorID = "OL7234434A"

// seedAuthor puts an author in the library the way an earlier import or add
// left it.
func seedAuthor(t *testing.T, repo *db.AuthorRepo, foreignID, name, provider string) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: foreignID, Name: name, SortName: name, MetadataProvider: provider, Monitored: true}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	return a
}

func authorCount(t *testing.T, repo *db.AuthorRepo) int {
	t.Helper()
	all, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(all)
}

// TestImportCSVAuthors_ExistingAuthorUnderAnotherProviderIsSkipped is #2117's
// second import. The first run bound the author to DNB while OpenLibrary was
// timing out (before #2332 stopped that), or a Hardcover primary added them.
// OpenLibrary now answers with its own id for the same person. The importer
// only looked the row up by that id, missed the DNB row, and created a second
// author.
func TestImportCSVAuthors_ExistingAuthorUnderAnotherProviderIsSkipped(t *testing.T) {
	cases := []struct {
		name, seededID, seededName, provider, row string
	}{
		{"dnb row from an outage", dnbAuthorID, guardAuthorName, "dnb", guardAuthorName},
		{"hardcover row", hcAuthorID, guardAuthorName, "hardcover", guardAuthorName},
		{"same name spelled differently", dnbAuthorID, "J.R.R. Tolkien", "dnb", "JRR Tolkien"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := db.NewAuthorRepo(newTestDB(t))
			seeded := seedAuthor(t, repo, tc.seededID, tc.seededName, tc.provider)
			agg := metadata.NewAggregator(answeringProvider("openlibrary", olAuthorID))

			res, err := ImportCSVAuthors(context.Background(), strings.NewReader(tc.row+"\n"), repo, nil, agg, nil)
			if err != nil {
				t.Fatalf("ImportCSVAuthors: %v", err)
			}
			if res.Added != 0 || res.Skipped != 1 || res.Errors != 0 {
				t.Errorf("result = %+v, want the row skipped as already in the library", res)
			}
			if n := authorCount(t, repo); n != 1 {
				t.Errorf("authors = %d, want 1: a second author was created for the same person", n)
			}
			// Nothing about the existing row changes: no merge, no relink.
			got, _ := repo.GetByID(context.Background(), seeded.ID)
			if got == nil || got.ForeignID != tc.seededID || got.MetadataProvider != tc.provider {
				t.Errorf("existing author = %+v, want it left as it was", got)
			}
		})
	}
}

// TestImportCSVAuthors_SimilarNameIsNotSkipped is the control: only the same
// name spelled differently counts as already there. A different person with a
// similar name is still added.
func TestImportCSVAuthors_SimilarNameIsNotSkipped(t *testing.T) {
	for _, seeded := range []string{"Andrew Weir", "Andy Weird", "A. B. Weir"} {
		t.Run(seeded, func(t *testing.T) {
			repo := db.NewAuthorRepo(newTestDB(t))
			seedAuthor(t, repo, dnbAuthorID, seeded, "dnb")
			agg := metadata.NewAggregator(answeringProvider("openlibrary", olAuthorID))

			res, err := ImportCSVAuthors(context.Background(), strings.NewReader(guardAuthorName+"\n"), repo, nil, agg, nil)
			if err != nil {
				t.Fatalf("ImportCSVAuthors: %v", err)
			}
			if res.Added != 1 {
				t.Errorf("result = %+v, want %q added beside %q", res, guardAuthorName, seeded)
			}
		})
	}
}

// TestImportReadarr_ExistingAuthorUnderAnotherProviderIsSkipped is the
// Readarr side, which shares the resolution step.
func TestImportReadarr_ExistingAuthorUnderAnotherProviderIsSkipped(t *testing.T) {
	path := newReadarrDBWithAuthor(t, guardAuthorName)
	database := newTestDB(t)
	repo := db.NewAuthorRepo(database)
	seedAuthor(t, repo, dnbAuthorID, guardAuthorName, "dnb")
	agg := metadata.NewAggregator(answeringProvider("openlibrary", olAuthorID))

	res, err := ImportReadarr(context.Background(), path, repo, db.NewIndexerRepo(database),
		db.NewDownloadClientRepo(database), db.NewBlocklistRepo(database), nil, agg, nil)
	if err != nil {
		t.Fatalf("ImportReadarr: %v", err)
	}
	if res.Authors.Added != 0 || res.Authors.Skipped != 1 {
		t.Errorf("authors result = %+v, want the row skipped as already in the library", res.Authors)
	}
	if n := authorCount(t, repo); n != 1 {
		t.Errorf("authors = %d, want 1", n)
	}
}
