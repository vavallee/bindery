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
		// Standard matches
		{"The Name of the Wind", "The Name of the Wind", true},
		{"Project Hail Mary", "Project Hail Mary", true},
		{"The Way of Kings", "Brandon Sanderson The Way of Kings", true},

		// Partial overlap — at least 2 significant (non-stopword) words required
		{"Dune Messiah", "Frank Herbert Dune Messiah", true},
		{"The Road", "Cormac McCarthy The Road 2006", true},

		// Single-token book title: minLen=1 → required=1; one matching token is enough
		{"Dune", "Frank Herbert Dune", true},
		{"Dune", "Dune 2021", true},
		{"The Sparrow", "The Sparrow Russell", true},

		// Numeric titles preserved (digits are kept as tokens)
		{"1984", "1984", true},
		{"1984", "George Orwell 1984", true},

		// Article inversion: "Lord of the Rings, The" normalises to same as "The Lord of the Rings"
		{"The Lord of the Rings", "Lord of the Rings, The", true},

		// Dots in parsed title are split on non-alnum — "Project.Hail.Mary" yields 3 tokens
		{"Project Hail Mary", "Project.Hail.Mary", true},

		// Empty / degenerate cases
		{"", "The Name of the Wind", false},
		{"The Name of the Wind", "", false},

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

// fakeCalibreAdder is a stub calibreAdder recording every Add invocation.
// Tests check both the call path and the book-id persistence so a broken
// wiring change surfaces here rather than in a live import.
type fakeCalibreAdder struct {
	calls  []string
	nextID int64
	err    error
}

func (f *fakeCalibreAdder) Add(_ context.Context, path string) (int64, error) {
	f.calls = append(f.calls, path)
	return f.nextID, f.err
}

// fakeDropFolderWriter is a stub calibreDropFolderWriter capturing the
// title/author/source triple for assertion. Tests inject it via WithCalibre
// so the real sanitize + polling code is only exercised in its own unit
// tests.
type fakeDropFolderWriter struct {
	calls []dropCall
	res   calibre.IngestResult
	err   error
}

type dropCall struct{ src, title, author string }

func (f *fakeDropFolderWriter) Ingest(_ context.Context, src, title, author string) (calibre.IngestResult, error) {
	f.calls = append(f.calls, dropCall{src, title, author})
	return f.res, f.err
}

func modeFn(m calibre.Mode) func() calibre.Mode { return func() calibre.Mode { return m } }

func importScannerFixture(t *testing.T) (*Scanner, *db.BookRepo, *models.Book, *models.Author, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)

	a := &models.Author{ForeignID: "OLA1", Name: "Author A", SortName: "A, Author", Monitored: true, MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{
		ForeignID: "OLB1", AuthorID: a.ID, Title: "Title T", SortTitle: "T, Title",
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
	return s, bookRepo, b, a, ctx
}

// TestPushToCalibre_ModeOff: regression guard for "integration off" —
// mode=off must mean zero client calls and no calibre_id mutation, matching
// the v0.7.2 / v0.8.0-with-toggle-off baseline.
func TestPushToCalibre_ModeOff(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{nextID: 99}
	fd := &fakeDropFolderWriter{}
	s.WithCalibre(modeFn(calibre.ModeOff), fc, fd)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	if len(fc.calls) != 0 {
		t.Errorf("Add must not be called when mode=off, got %v", fc.calls)
	}
	if len(fd.calls) != 0 {
		t.Errorf("Ingest must not be called when mode=off, got %v", fd.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil when mode=off, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ModeCalibredbHappyPath: the v0.8.0 regression test —
// mode=calibredb calls the adder and persists the returned id. Locks in
// that the new mode-dispatch didn't break Path A.
func TestPushToCalibre_ModeCalibredbHappyPath(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{nextID: 1234}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc, nil)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	if len(fc.calls) != 1 || fc.calls[0] != "/library/book.epub" {
		t.Errorf("Add calls = %v", fc.calls)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 1234 {
		t.Errorf("calibre_id = %v, want 1234", got.CalibreID)
	}
}

// TestPushToCalibre_CalibredbFailDoesNotPoison: a failed calibredb call
// must leave calibre_id at nil (best-effort mirror semantics).
func TestPushToCalibre_CalibredbFailDoesNotPoison(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{err: errors.New("exec: calibredb: not found")}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc, nil)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must remain nil on add failure, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ErrDisabledSilent — the adder may return ErrDisabled
// when the client's own config is off; we treat it the same as mode=off.
func TestPushToCalibre_ErrDisabledSilent(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{err: calibre.ErrDisabled}
	s.WithCalibre(modeFn(calibre.ModeCalibredb), fc, nil)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil on ErrDisabled, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_NilResolver covers the default path — a scanner built
// without WithCalibre() (i.e. calibre not configured at all) must not
// panic on a nil interface dereference.
func TestPushToCalibre_NilResolver(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	// No WithCalibre call.
	s.pushToCalibre(ctx, book, author, "/library/book.epub") // must not panic
}

// TestPushToCalibre_ModeDropFolderHappyPath — mode=drop_folder routes to
// the drop writer with title + author propagated, and the returned id
// lands on the book row.
func TestPushToCalibre_ModeDropFolderHappyPath(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fc := &fakeCalibreAdder{}
	fd := &fakeDropFolderWriter{res: calibre.IngestResult{DroppedPath: "/drop/A/T.epub", CalibreID: 77, Found: true}}
	s.WithCalibre(modeFn(calibre.ModeDropFolder), fc, fd)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	if len(fc.calls) != 0 {
		t.Errorf("adder must not be called in drop_folder mode, got %v", fc.calls)
	}
	if len(fd.calls) != 1 {
		t.Fatalf("expected 1 Ingest call, got %d", len(fd.calls))
	}
	call := fd.calls[0]
	if call.src != "/library/book.epub" || call.title != "Title T" || call.author != "Author A" {
		t.Errorf("Ingest called with %+v, want src=/library/book.epub title=Title T author=Author A", call)
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 77 {
		t.Errorf("calibre_id = %v, want 77", got.CalibreID)
	}
}

// TestPushToCalibre_ModeDropFolderPollTimeout — Calibre didn't pick up the
// file within the budget. Drop-folder mode must NOT stamp a calibre_id
// but also MUST NOT surface as an import failure (already logged as warn).
func TestPushToCalibre_ModeDropFolderPollTimeout(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fd := &fakeDropFolderWriter{res: calibre.IngestResult{DroppedPath: "/drop/A/T.epub", Found: false}}
	s.WithCalibre(modeFn(calibre.ModeDropFolder), nil, fd)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil on poll timeout, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ModeDropFolderIngestError — a hard Ingest error (e.g.
// disk full writing into the drop folder) is logged but swallowed so the
// rest of the import pipeline completes.
func TestPushToCalibre_ModeDropFolderIngestError(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fd := &fakeDropFolderWriter{err: errors.New("disk full")}
	s.WithCalibre(modeFn(calibre.ModeDropFolder), nil, fd)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil on ingest error, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ModeDropFolderUnconfigured — ErrDropFolderNotConfigured
// is treated as a silent skip (same as ErrDisabled on the calibredb side).
func TestPushToCalibre_ModeDropFolderUnconfigured(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	fd := &fakeDropFolderWriter{err: calibre.ErrDropFolderNotConfigured}
	s.WithCalibre(modeFn(calibre.ModeDropFolder), nil, fd)

	s.pushToCalibre(ctx, book, author, "/library/book.epub")

	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID != nil {
		t.Errorf("calibre_id must stay nil when drop folder unconfigured, got %v", got.CalibreID)
	}
}

// TestPushToCalibre_ModeDropFolderFallbackAuthor — if the author lookup
// failed upstream we still attempt the ingest with a placeholder name so
// Calibre at least receives the file. The lookup will likely miss but that
// surfaces cleanly as a poll timeout instead of a nil-deref crash.
func TestPushToCalibre_ModeDropFolderFallbackAuthor(t *testing.T) {
	s, _, book, _, ctx := importScannerFixture(t)
	fd := &fakeDropFolderWriter{res: calibre.IngestResult{Found: false}}
	s.WithCalibre(modeFn(calibre.ModeDropFolder), nil, fd)

	s.pushToCalibre(ctx, book, nil, "/library/book.epub")

	if len(fd.calls) != 1 || fd.calls[0].author != "Unknown Author" {
		t.Errorf("fallback author not applied: %+v", fd.calls)
	}
}
