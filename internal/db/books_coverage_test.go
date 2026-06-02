package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// helper: create an author and return it.
func mkAuthor(t *testing.T, repo *AuthorRepo, ctx context.Context, fid string) *models.Author {
	t.Helper()
	a := &models.Author{ForeignID: fid, Name: "A " + fid, SortName: "A " + fid, MetadataProvider: "openlibrary", Monitored: true}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	return a
}

// helper: create a book and return it.
func mkBook(t *testing.T, repo *BookRepo, ctx context.Context, authorID int64, fid, title, status string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: fid, AuthorID: authorID, Title: title, SortTitle: title,
		Status: status, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBookRepo_ExcludedFlagAndListVariants(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-EX-A")
	kept := mkBook(t, bookRepo, ctx, a.ID, "OL-EX-K", "Kept", "wanted")
	hidden := mkBook(t, bookRepo, ctx, a.ID, "OL-EX-H", "Hidden", "wanted")

	// Exclude one book.
	if err := bookRepo.SetExcluded(ctx, hidden.ID, true); err != nil {
		t.Fatalf("SetExcluded: %v", err)
	}

	// Confirm Excluded round-trips.
	got, _ := bookRepo.GetByID(ctx, hidden.ID)
	if !got.Excluded {
		t.Error("expected Excluded=true after SetExcluded")
	}

	// List() hides excluded rows; ListIncludingExcluded() shows them.
	visible, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != kept.ID {
		t.Errorf("List should hide excluded, got %+v", visible)
	}

	all, err := bookRepo.ListIncludingExcluded(ctx)
	if err != nil {
		t.Fatalf("ListIncludingExcluded: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListIncludingExcluded: want 2, got %d", len(all))
	}

	// ListByAuthor / IncludingExcluded
	byAuthor, _ := bookRepo.ListByAuthor(ctx, a.ID)
	if len(byAuthor) != 1 {
		t.Errorf("ListByAuthor: want 1, got %d", len(byAuthor))
	}
	byAuthorAll, _ := bookRepo.ListByAuthorIncludingExcluded(ctx, a.ID)
	if len(byAuthorAll) != 2 {
		t.Errorf("ListByAuthorIncludingExcluded: want 2, got %d", len(byAuthorAll))
	}

	// ListByStatus / IncludingExcluded
	byStatus, _ := bookRepo.ListByStatus(ctx, "wanted")
	if len(byStatus) != 1 {
		t.Errorf("ListByStatus: want 1, got %d", len(byStatus))
	}
	byStatusAll, _ := bookRepo.ListByStatusIncludingExcluded(ctx, "wanted")
	if len(byStatusAll) != 2 {
		t.Errorf("ListByStatusIncludingExcluded: want 2, got %d", len(byStatusAll))
	}

	// Unexclude.
	if err := bookRepo.SetExcluded(ctx, hidden.ID, false); err != nil {
		t.Fatalf("SetExcluded(false): %v", err)
	}
	got, _ = bookRepo.GetByID(ctx, hidden.ID)
	if got.Excluded {
		t.Error("expected Excluded=false after clearing")
	}
	visible, _ = bookRepo.List(ctx)
	if len(visible) != 2 {
		t.Errorf("after unexclude, List: want 2, got %d", len(visible))
	}
}

func TestBookRepo_CalibreIDRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-CL-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-CL-B", "Calibre Book", "wanted")

	// Before setting, GetByCalibreID returns nil.
	missing, err := bookRepo.GetByCalibreID(ctx, 12345)
	if err != nil {
		t.Fatalf("GetByCalibreID missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for unset calibre id")
	}

	// Set and look up.
	if err := bookRepo.SetCalibreID(ctx, b.ID, 12345); err != nil {
		t.Fatalf("SetCalibreID: %v", err)
	}
	got, err := bookRepo.GetByCalibreID(ctx, 12345)
	if err != nil {
		t.Fatalf("GetByCalibreID: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("unexpected book: %+v", got)
	}
	if got.CalibreID == nil || *got.CalibreID != 12345 {
		t.Errorf("CalibreID not populated: %v", got.CalibreID)
	}
}

func TestBookRepo_FindByAuthorAndTitleCaseInsensitive(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)

	a := mkAuthor(t, authorRepo, ctx, "OL-FT-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-FT-B", "The Great Novel", "wanted")

	// Exact match.
	got, err := bookRepo.FindByAuthorAndTitle(ctx, a.ID, "The Great Novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle exact: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("exact match failed: %+v", got)
	}

	// Case-insensitive match.
	got, err = bookRepo.FindByAuthorAndTitle(ctx, a.ID, "the great novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle lower: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Errorf("case-insensitive match failed: %+v", got)
	}

	// Wrong author — no match.
	got, err = bookRepo.FindByAuthorAndTitle(ctx, 999, "The Great Novel")
	if err != nil {
		t.Fatalf("FindByAuthorAndTitle other author: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for wrong author, got %+v", got)
	}

	// No match on title.
	got, _ = bookRepo.FindByAuthorAndTitle(ctx, a.ID, "Unrelated")
	if got != nil {
		t.Error("expected nil for unrelated title")
	}
}

// TestBookRepo_ListPopulatesAuthor is the regression test for #882. The
// Books page and book detail page in the frontend both read
// book.author.authorName; before the LEFT JOIN to authors was added, that
// field was nil on every row and the UI rendered empty author columns.
func TestBookRepo_ListPopulatesAuthor(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bookRepo := NewBookRepo(database)
	authorRepo := NewAuthorRepo(database)
	ctx := context.Background()

	a := mkAuthor(t, authorRepo, ctx, "OL-LIST-AUTHOR")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-LIST-BOOK", "The Book", models.BookStatusWanted)

	// List
	all, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 book, got %d", len(all))
	}
	if all[0].Author == nil {
		t.Fatal("List: book.Author is nil; expected joined author projection")
	}
	if all[0].Author.ID != a.ID {
		t.Errorf("List: Author.ID = %d, want %d", all[0].Author.ID, a.ID)
	}
	if all[0].Author.Name != a.Name {
		t.Errorf("List: Author.Name = %q, want %q", all[0].Author.Name, a.Name)
	}
	if all[0].Author.ForeignID != a.ForeignID {
		t.Errorf("List: Author.ForeignID = %q, want %q", all[0].Author.ForeignID, a.ForeignID)
	}

	// GetByID
	gotByID, err := bookRepo.GetByID(ctx, b.ID)
	if err != nil || gotByID == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if gotByID.Author == nil || gotByID.Author.Name != a.Name {
		t.Errorf("GetByID: expected Author.Name = %q; got %+v", a.Name, gotByID.Author)
	}

	// GetByForeignID
	gotByFID, err := bookRepo.GetByForeignID(ctx, b.ForeignID)
	if err != nil || gotByFID == nil {
		t.Fatalf("GetByForeignID: %v", err)
	}
	if gotByFID.Author == nil || gotByFID.Author.Name != a.Name {
		t.Errorf("GetByForeignID: expected Author.Name = %q; got %+v", a.Name, gotByFID.Author)
	}

	// ListByAuthor
	byAuthor, err := bookRepo.ListByAuthor(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAuthor) != 1 || byAuthor[0].Author == nil {
		t.Errorf("ListByAuthor: expected 1 book with Author populated; got %+v", byAuthor)
	}

	// ListByStatus
	byStatus, err := bookRepo.ListByStatus(ctx, models.BookStatusWanted)
	if err != nil {
		t.Fatal(err)
	}
	if len(byStatus) != 1 || byStatus[0].Author == nil {
		t.Errorf("ListByStatus: expected 1 book with Author populated; got %+v", byStatus)
	}
}

// TestBookRepo_ListHandlesOrphanAuthorID pins down the LEFT JOIN choice
// against an orphan author_id. Production has FOREIGN KEY=ON so this
// shouldn't happen naturally, but the defensive code path matters if FKs
// ever get bypassed during migration. We disable the FK constraint for
// the duration of this test to set up the impossible-in-prod state.
func TestBookRepo_ListHandlesOrphanAuthorID(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bookRepo := NewBookRepo(database)
	authorRepo := NewAuthorRepo(database)
	ctx := context.Background()

	a := mkAuthor(t, authorRepo, ctx, "OL-ORPHAN-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-ORPHAN-B", "Orphan Book", models.BookStatusWanted)

	// Bypass the FK constraint just long enough to set up the orphan row.
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE books SET author_id = 99999 WHERE id = ?", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	_ = a

	all, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected book to still appear despite orphan author_id; got %d rows", len(all))
	}
	if all[0].Author != nil {
		t.Errorf("expected Author == nil for orphan author_id; got %+v", all[0].Author)
	}
	if all[0].AuthorID != 99999 {
		t.Errorf("AuthorID should be preserved; got %d", all[0].AuthorID)
	}
}

// TestBook_ScansLegacyGoStringFormat pins down the #914 regression: rows
// whose release_date was written as Go's default time.String() shape
// (`2006-01-02 15:04:05 +0000 UTC`) must still load via the Scan helper.
// Before the parseFlexibleTime fallback, modernc.org/sqlite returned
// "unsupported Scan" and the Books page rendered blank.
func TestBook_ScansLegacyGoStringFormat(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-LEGACY-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-LEGACY-B", "Legacy Book", models.BookStatusWanted)

	// Overwrite release_date with the Go-default shape that modernc rejects.
	const legacy = "2024-01-15 12:34:56 +0000 UTC"
	if _, err := database.ExecContext(ctx,
		"UPDATE books SET release_date = ? WHERE id = ?", legacy, b.ID); err != nil {
		t.Fatalf("seed legacy date: %v", err)
	}

	got, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List should tolerate legacy date format, got: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 book, got %d", len(got))
	}
	if got[0].ReleaseDate == nil {
		t.Fatal("expected ReleaseDate populated from legacy format, got nil")
	}
	if got[0].ReleaseDate.Year() != 2024 || got[0].ReleaseDate.Month() != time.January || got[0].ReleaseDate.Day() != 15 {
		t.Errorf("ReleaseDate mis-parsed: got %v", got[0].ReleaseDate)
	}
}

// TestBook_ScansCalibrePubdateFormat covers the format Calibre itself
// emits into metadata.db for pubdate (`+00:00` offset, space separator,
// no `T`). When that value flows through a Calibre import and gets
// stored back in books.release_date, the Scan path must still load it.
func TestBook_ScansCalibrePubdateFormat(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-CAL-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-CAL-B", "Calibre Book", models.BookStatusWanted)

	const calibreShape = "2024-01-15 12:34:56+00:00"
	if _, err := database.ExecContext(ctx,
		"UPDATE books SET release_date = ? WHERE id = ?", calibreShape, b.ID); err != nil {
		t.Fatalf("seed calibre date: %v", err)
	}

	got, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List should tolerate calibre date format, got: %v", err)
	}
	if len(got) != 1 || got[0].ReleaseDate == nil {
		t.Fatalf("expected book with non-nil ReleaseDate, got %+v", got)
	}
	if got[0].ReleaseDate.Year() != 2024 {
		t.Errorf("ReleaseDate mis-parsed: got %v", got[0].ReleaseDate)
	}
}

// TestBook_WriteRoundTripUsesRFC3339Nano guarantees the write side stores
// release_date in a shape modernc.org/sqlite can Scan back without help.
// If a future change reintroduces the raw *time.Time argument, the stored
// string will revert to Go's default shape and this test fails.
func TestBook_WriteRoundTripUsesRFC3339Nano(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-RT-A")

	when := time.Date(2024, 1, 15, 12, 34, 56, 0, time.UTC)
	b := &models.Book{
		ForeignID:        "OL-RT-B",
		AuthorID:         a.ID,
		Title:            "Round Trip",
		SortTitle:        "Round Trip",
		Status:           models.BookStatusWanted,
		Genres:           []string{},
		MetadataProvider: "openlibrary",
		Monitored:        true,
		ReleaseDate:      &when,
	}
	if err := bookRepo.Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var stored string
	if err := database.QueryRowContext(ctx,
		"SELECT release_date FROM books WHERE id = ?", b.ID).Scan(&stored); err != nil {
		t.Fatalf("read raw release_date: %v", err)
	}
	if strings.Contains(stored, "+0000 UTC") || strings.Contains(stored, " UTC") {
		t.Errorf("stored release_date used Go default shape %q; want RFC3339Nano", stored)
	}
	if _, perr := time.Parse(time.RFC3339Nano, stored); perr != nil {
		t.Errorf("stored release_date %q does not parse as RFC3339Nano: %v", stored, perr)
	}

	// Also confirm created_at / updated_at went through the same path.
	var createdAt, updatedAt string
	if err := database.QueryRowContext(ctx,
		"SELECT created_at, updated_at FROM books WHERE id = ?", b.ID).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read created_at/updated_at: %v", err)
	}
	for name, v := range map[string]string{"created_at": createdAt, "updated_at": updatedAt} {
		if strings.Contains(v, " UTC") {
			t.Errorf("%s stored Go default shape %q; want RFC3339Nano", name, v)
		}
	}
}

// TestBook_GarbageInTimeColumnDoesNotKillScan checks the
// log-and-substitute behaviour: a single unparseable time string must
// not take out the whole list query. The row still shows up; the bad
// column resolves to nil (for *time.Time) or zero (for time.Time).
func TestBook_GarbageInTimeColumnDoesNotKillScan(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authorRepo := NewAuthorRepo(database)
	bookRepo := NewBookRepo(database)
	a := mkAuthor(t, authorRepo, ctx, "OL-GARBAGE-A")
	b := mkBook(t, bookRepo, ctx, a.ID, "OL-GARBAGE-B", "Garbage Book", models.BookStatusWanted)

	if _, err := database.ExecContext(ctx,
		"UPDATE books SET release_date = ? WHERE id = ?", "not a date", b.ID); err != nil {
		t.Fatalf("seed garbage date: %v", err)
	}

	got, err := bookRepo.List(ctx)
	if err != nil {
		t.Fatalf("List should tolerate garbage time, got: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 book despite garbage release_date, got %d", len(got))
	}
	if got[0].ReleaseDate != nil {
		t.Errorf("expected nil ReleaseDate for garbage value, got %v", got[0].ReleaseDate)
	}
}
