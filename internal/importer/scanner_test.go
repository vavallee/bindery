package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func TestTitleMatch(t *testing.T) {
	tests := []struct {
		bookTitle   string
		parsedTitle string
		want        bool
	}{
		// Standard matches — parsed titles use spaces, not dots
		{"The Name of the Wind", "The Name of the Wind", true},
		{"Project Hail Mary", "Project Hail Mary", true},
		{"The Way of Kings", "Brandon Sanderson The Way of Kings", true},

		// Partial overlap — at least 2 significant words required
		{"Dune Messiah", "Frank Herbert Dune Messiah", true},
		{"The Road", "Cormac McCarthy The Road 2006", true},

		// Single significant book-title word: minOverlap follows ptWords length
		// "Dune" → btWords=["dune"]; ptWords=["frank","herbert","dune"] len=3 → minOverlap=2
		// overlap=1 → false (single-word titles need a 1-word parsed title to match)
		{"Dune", "Frank Herbert Dune", false},
		// When parsed title is also short, minOverlap=1 and overlap=1 → true
		{"Dune", "Dune 2021", true},
		{"The Sparrow", "The Sparrow Russell", true},

		// Empty / degenerate cases
		{"", "The Name of the Wind", false},
		{"The Name of the Wind", "", false},
		// Dots in parsed title are not split — "project.hail.mary" becomes one big token
		{"Project Hail Mary", "Project.Hail.Mary", false},

		// Noise titles with no overlap
		{"Project Hail Mary", "The Lord of the Rings", false},
		{"Dune", "Foundation Asimov", false},
	}

	for _, tt := range tests {
		got := titleMatch(tt.bookTitle, tt.parsedTitle)
		if got != tt.want {
			t.Errorf("titleMatch(%q, %q) = %v, want %v", tt.bookTitle, tt.parsedTitle, got, tt.want)
		}
	}
}

// fakeCalibre is a stub calibreClient recording every Add invocation. Tests
// check both the call path and the book-id persistence so a broken wiring
// change surfaces here rather than in a live import.
type fakeCalibre struct {
	enabled bool
	calls   []string
	nextID  int64
	err     error
}

func (f *fakeCalibre) Enabled() bool { return f.enabled }
func (f *fakeCalibre) Add(_ context.Context, path string) (int64, error) {
	f.calls = append(f.calls, path)
	return f.nextID, f.err
}

func importScannerFixture(t *testing.T) (*Scanner, *db.BookRepo, *models.Book, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)

	a := &models.Author{ForeignID: "OLA1", Name: "A", SortName: "A", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OLB1", AuthorID: a.ID, Title: "T", SortTitle: "T",
		Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
		MetadataProvider: "openlibrary",
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(
		db.NewDownloadRepo(database), db.NewDownloadClientRepo(database),
		bookRepo, authorRepo, db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "",
	)
	return s, bookRepo, b, ctx
}

// TestPushToCalibre_Disabled is the regression guard for "integration off"
// — a user who never touched the Calibre settings must see identical import
// behaviour to v0.7.2, which means no client call and no calibre_id write.
func TestPushToCalibre_Disabled(t *testing.T) {
	s, bookRepo, book, ctx := importScannerFixture(t)
	fc := &fakeCalibre{enabled: false, nextID: 99}
	s.WithCalibre(fc)

	s.pushToCalibre(ctx, book, "/library/book.epub")

	if len(fc.calls) != 0 {
		t.Errorf("Add must not be called when disabled, got %v", fc.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil when disabled, got %v", got.CalibreID)
	}
}

func TestPushToCalibre_HappyPath(t *testing.T) {
	s, bookRepo, book, ctx := importScannerFixture(t)
	fc := &fakeCalibre{enabled: true, nextID: 1234}
	s.WithCalibre(fc)

	s.pushToCalibre(ctx, book, "/library/book.epub")

	if len(fc.calls) != 1 || fc.calls[0] != "/library/book.epub" {
		t.Errorf("Add calls = %v", fc.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 1234 {
		t.Errorf("calibre_id = %v, want 1234", got.CalibreID)
	}
}

// TestPushToCalibre_AddFailsDoesNotPoison: a failed calibredb call must not
// roll back the import or stamp a bogus id. The book row stays file-pathed
// but calibre_id is still nil, matching the semantics that Calibre sync is
// a best-effort mirror.
func TestPushToCalibre_AddFailsDoesNotPoison(t *testing.T) {
	s, bookRepo, book, ctx := importScannerFixture(t)
	fc := &fakeCalibre{enabled: true, err: errors.New("exec: calibredb: not found")}
	s.WithCalibre(fc)

	s.pushToCalibre(ctx, book, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must remain nil on add failure, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ErrDisabledSilent guards that a mismatched state (client
// reports Enabled() true but Add returns ErrDisabled) is treated the same as
// disabled — no warning, no id, no panic.
func TestPushToCalibre_ErrDisabledSilent(t *testing.T) {
	s, bookRepo, book, ctx := importScannerFixture(t)
	fc := &fakeCalibre{enabled: true, err: calibre.ErrDisabled}
	s.WithCalibre(fc)

	s.pushToCalibre(ctx, book, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil on ErrDisabled, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_NilClient covers the default path — a scanner built
// without WithCalibre() (i.e. calibre not configured at all) must not
// panic on a nil interface dereference.
func TestPushToCalibre_NilClient(t *testing.T) {
	s, _, book, ctx := importScannerFixture(t)
	// No WithCalibre call.
	s.pushToCalibre(ctx, book, "/library/book.epub") // must not panic
}
