package calibre

import (
	"context"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/models"
)

// TestFindAuthorByNameAcrossSpellings covers #1647. This was the only author
// matching site in the tree that skipped textutil entirely — a raw
// `strings.ToLower(a) == strings.ToLower(b)`. Calibre's metadata.db holds
// whatever wrote it, and Calibre desktop on macOS emits NFD, so an author
// already present from ABS or OpenLibrary got a second row on first import and
// a fresh one on every later import, with the books split across two pages.
func TestFindAuthorByNameAcrossSpellings(t *testing.T) {
	imp, _, authorRepo, _, _, _, _ := newImporterFixture(t)
	ctx := context.Background()

	existing := &models.Author{
		ForeignID: "OL1A", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R.",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, existing); err != nil {
		t.Fatalf("seed: %v", err)
	}
	umlaut := &models.Author{
		ForeignID: "OL2A", Name: norm.NFC.String("Jörg Müller"), SortName: "Müller, Jörg",
		MetadataProvider: "openlibrary",
	}
	if err := authorRepo.Create(ctx, umlaut); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name        string
		calibreName string
		wantID      int64
	}{
		{"byte identical", "J. R. R. Tolkien", existing.ID},
		{"compact initials", "J.R.R. Tolkien", existing.ID},
		{"last-first", "Tolkien, J.R.R.", existing.ID},
		{"case only", "j. r. r. tolkien", existing.ID},
		{"decomposed unicode", norm.NFD.String("Jörg Müller"), umlaut.ID},
		{"transliterated", "Joerg Mueller", umlaut.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := imp.findAuthorByName(ctx, tc.calibreName)
			if err != nil {
				t.Fatalf("findAuthorByName(%q): %v", tc.calibreName, err)
			}
			if got == nil {
				t.Fatalf("findAuthorByName(%q) = nil, want author %d", tc.calibreName, tc.wantID)
			}
			if got.ID != tc.wantID {
				t.Fatalf("findAuthorByName(%q) = author %d (%q), want %d", tc.calibreName, got.ID, got.Name, tc.wantID)
			}
		})
	}
}

// TestFindAuthorByNameStillRejectsOthers guards the widening: this decides
// whether a bulk import reuses an author row or creates one, so an over-eager
// match silently reparents somebody else's books.
func TestFindAuthorByNameStillRejectsOthers(t *testing.T) {
	imp, _, authorRepo, _, _, _, _ := newImporterFixture(t)
	ctx := context.Background()

	if err := authorRepo.Create(ctx, &models.Author{
		ForeignID: "OL1B", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R.",
		MetadataProvider: "openlibrary",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, name := range []string{"Christopher Tolkien", "Terry Pratchett", "J. R. Ward", ""} {
		got, err := imp.findAuthorByName(ctx, name)
		if err != nil {
			t.Fatalf("findAuthorByName(%q): %v", name, err)
		}
		if got != nil {
			t.Errorf("findAuthorByName(%q) = %q, want nil", name, got.Name)
		}
	}
}

// TestFindAuthorByNamePrefersExactByte pins tier ordering: when a byte-exact
// row exists it wins, even though a second row would also match via variants.
// Without this the choice between two plausible rows would depend on table
// order.
func TestFindAuthorByNamePrefersExactByte(t *testing.T) {
	imp, _, authorRepo, _, _, _, _ := newImporterFixture(t)
	ctx := context.Background()

	variant := &models.Author{ForeignID: "OL-V", Name: "Tolkien, J.R.R.", SortName: "Tolkien, J.R.R."}
	if err := authorRepo.Create(ctx, variant); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	exact := &models.Author{ForeignID: "OL-E", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R."}
	if err := authorRepo.Create(ctx, exact); err != nil {
		t.Fatalf("seed exact: %v", err)
	}

	got, err := imp.findAuthorByName(ctx, "J. R. R. Tolkien")
	if err != nil {
		t.Fatalf("findAuthorByName: %v", err)
	}
	if got == nil || got.ID != exact.ID {
		t.Fatalf("findAuthorByName picked %+v, want the byte-exact row %d", got, exact.ID)
	}

	// With no byte-exact row for this spelling, the variant tier resolves it,
	// and ties go to the lowest ID so repeated imports converge.
	got, err = imp.findAuthorByName(ctx, "J.R.R.  Tolkien")
	if err != nil {
		t.Fatalf("findAuthorByName(variant): %v", err)
	}
	if got == nil || got.ID != variant.ID {
		t.Fatalf("findAuthorByName(variant) = %+v, want the lowest-ID match %d", got, variant.ID)
	}
}
