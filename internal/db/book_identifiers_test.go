package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func bookIdentityFixture(t *testing.T) (*BookRepo, *models.Author) {
	t.Helper()
	database, err := OpenMemory()
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	authors := NewAuthorRepo(database)
	a := &models.Author{ForeignID: "OL1A", Name: "Ident Author", SortName: "Author, Ident"}
	if err := authors.Create(context.Background(), a); err != nil {
		t.Fatalf("create author: %v", err)
	}
	return NewBookRepo(database), a
}

func newBook(foreignID, title string, authorID int64) *models.Book {
	return &models.Book{
		ForeignID: foreignID, AuthorID: authorID, Title: title, SortTitle: title,
		Genres: []string{}, Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook,
	}
}

// TestBookIdentifiers_CreateRecordsPrimaryID: every creation path flows through
// Create, so the primary identity is always on record and the new lookup is
// never blind to a book (#1705).
func TestBookIdentifiers_CreateRecordsPrimaryID(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	ids, err := books.ListBookIdentifiers(ctx, b.ID)
	if err != nil {
		t.Fatalf("list identifiers: %v", err)
	}
	if len(ids) != 1 || ids[0].ForeignID != "hc:volume-1" {
		t.Fatalf("identifiers = %+v, want the primary id", ids)
	}
	if ids[0].Provider != "hardcover" {
		t.Errorf("provider = %q, want hardcover", ids[0].Provider)
	}
}

// TestBookIdentifiers_CreateRecordsHardcoverLink is the cross-provider link:
// mergeAuthorWorks joins the two catalogues in memory and sets
// HardcoverForeignID, which used to be dropped at persist because there was
// nowhere to put it.
func TestBookIdentifiers_CreateRecordsHardcoverLink(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("OL1W", "Volume 1", a.ID)
	b.HardcoverForeignID = "hc:volume-1"
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	got, err := books.GetByAnyForeignID(ctx, "hc:volume-1")
	if err != nil {
		t.Fatalf("lookup by hardcover id: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Fatalf("the Hardcover id did not resolve to the book it was merged from: %+v", got)
	}
}

// TestBookIdentifiers_GetByAnyForeignID_FindsCrossProviderRow is #1705 in
// miniature: a row created from Hardcover, then looked up by the OpenLibrary id
// the same work carries.
func TestBookIdentifiers_GetByAnyForeignID_FindsCrossProviderRow(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	if err := books.UpsertBookIdentifier(ctx, b.ID, "OL1W"); err != nil {
		t.Fatalf("attach OL id: %v", err)
	}

	// Both ids resolve to the one row.
	for _, id := range []string{"hc:volume-1", "OL1W"} {
		got, err := books.GetByAnyForeignID(ctx, id)
		if err != nil {
			t.Fatalf("lookup %q: %v", id, err)
		}
		if got == nil || got.ID != b.ID {
			t.Errorf("lookup %q resolved to %+v, want book %d", id, got, b.ID)
		}
	}
	// The primary column is untouched by attaching a second identity.
	fresh, err := books.GetByID(ctx, b.ID)
	if err != nil || fresh == nil {
		t.Fatalf("get book: %v", err)
	}
	if fresh.ForeignID != "hc:volume-1" {
		t.Errorf("primary ForeignID = %q, want it unchanged", fresh.ForeignID)
	}
}

// TestBookIdentifiers_UnknownIDResolvesToNothing keeps the lookup from becoming
// a fuzzy match.
func TestBookIdentifiers_UnknownIDResolvesToNothing(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	if err := books.Create(ctx, newBook("hc:volume-1", "Volume 1", a.ID)); err != nil {
		t.Fatalf("create book: %v", err)
	}
	for _, id := range []string{"OL-not-here", "", "   "} {
		got, err := books.GetByAnyForeignID(ctx, id)
		if err != nil {
			t.Fatalf("lookup %q: %v", id, err)
		}
		if got != nil {
			t.Errorf("lookup %q resolved to %+v, want nothing", id, got)
		}
	}
}

// TestBookIdentifiers_ConflictIsReportedNotForced: an id already claimed by a
// different book must not be silently re-pointed, which would move a book's
// identity out from under whoever is looking at it.
func TestBookIdentifiers_ConflictIsReportedNotForced(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	first := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}
	second := newBook("OL2W", "Volume 2", a.ID)
	if err := books.Create(ctx, second); err != nil {
		t.Fatalf("create second: %v", err)
	}

	err := books.UpsertBookIdentifier(ctx, second.ID, "hc:volume-1")
	if !errors.Is(err, ErrBookIdentifierConflict) {
		t.Fatalf("err = %v, want ErrBookIdentifierConflict", err)
	}
	var conflict *BookIdentifierConflictError
	if !errors.As(err, &conflict) || conflict.BookID != first.ID {
		t.Errorf("conflict = %+v, want it to name book %d", conflict, first.ID)
	}
	// The id still belongs to the original book.
	got, err := books.GetByAnyForeignID(ctx, "hc:volume-1")
	if err != nil || got == nil || got.ID != first.ID {
		t.Errorf("identity moved: got %+v, want book %d", got, first.ID)
	}
}

// TestBookIdentifiers_ReattachingToTheSameBookIsIdempotent: syncs re-record the
// same ids constantly, so a repeat must not be an error.
func TestBookIdentifiers_ReattachingToTheSameBookIsIdempotent(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := books.UpsertBookIdentifier(ctx, b.ID, "OL1W"); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
	}
	ids, err := books.ListBookIdentifiers(ctx, b.ID)
	if err != nil {
		t.Fatalf("list identifiers: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("identifiers = %+v, want 2 (primary plus OL)", ids)
	}
}

// TestBookIdentifiers_DeletingABookClearsItsIdentifiers: the FK cascade is what
// stops a dead id permanently blocking the same book being re-added, which is
// the failure migration 068 had to repair for authors.
func TestBookIdentifiers_DeletingABookClearsItsIdentifiers(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	if err := books.Delete(ctx, b.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}
	got, err := books.GetBookIdentifier(ctx, "hc:volume-1")
	if err != nil {
		t.Fatalf("get identifier: %v", err)
	}
	if got != nil {
		t.Errorf("identifier survived its book: %+v", got)
	}
}

// TestBookProviderFromForeignID covers the classification the table and the
// migration backfill share.
func TestBookProviderFromForeignID(t *testing.T) {
	cases := map[string]string{
		"hc:volume-1":  "hardcover",
		"OL1W":         "openlibrary",
		"calibre:12":   "calibre",
		"abs:lib:item": "audiobookshelf",
		"gb:xyz":       "googlebooks",
		"dnb:123":      "dnb",
		"":             "openlibrary",
	}
	for id, want := range cases {
		if got := models.BookProviderFromForeignID(id); got != want {
			t.Errorf("BookProviderFromForeignID(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestBookIdentifiers_Delete detaches one id without touching the book or its
// other identities.
func TestBookIdentifiers_Delete(t *testing.T) {
	books, a := bookIdentityFixture(t)
	ctx := context.Background()

	b := newBook("hc:volume-1", "Volume 1", a.ID)
	if err := books.Create(ctx, b); err != nil {
		t.Fatalf("create book: %v", err)
	}
	if err := books.UpsertBookIdentifier(ctx, b.ID, "OL1W"); err != nil {
		t.Fatalf("attach OL id: %v", err)
	}

	if err := books.DeleteBookIdentifier(ctx, b.ID, "OL1W"); err != nil {
		t.Fatalf("delete identifier: %v", err)
	}
	if got, _ := books.GetByAnyForeignID(ctx, "OL1W"); got != nil {
		t.Error("the detached id still resolves")
	}
	if got, _ := books.GetByAnyForeignID(ctx, "hc:volume-1"); got == nil || got.ID != b.ID {
		t.Error("detaching one id disturbed the book's primary identity")
	}

	// A no-op delete is not an error: callers should not have to check first.
	if err := books.DeleteBookIdentifier(ctx, b.ID, "OL-never-attached"); err != nil {
		t.Errorf("deleting an unattached id returned %v, want nil", err)
	}
	if err := books.DeleteBookIdentifier(ctx, 0, "OL1W"); err != nil {
		t.Errorf("deleting with no book id returned %v, want nil", err)
	}
}

// TestBookIdentifierConflictError_Message: the conflict names both the id and
// the row that already owns it, because "already belongs to another book" on
// its own is not actionable in a log.
func TestBookIdentifierConflictError_Message(t *testing.T) {
	err := &BookIdentifierConflictError{ForeignID: "hc:volume-1", BookID: 42}
	msg := err.Error()
	for _, want := range []string{"hc:volume-1", "42"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
	var nilErr *BookIdentifierConflictError
	if nilErr.Error() != ErrBookIdentifierConflict.Error() {
		t.Errorf("nil receiver message = %q, want the sentinel's", nilErr.Error())
	}
}
