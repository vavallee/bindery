package db

import (
	"context"
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A failure after every write has been made leaves nothing behind: the file
// row, the status, the blocklist entry and the history event all roll back.
func TestRejectImport_RollsBackEverythingOnFailure(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	a := &models.Author{ForeignID: "a-rb", Name: "A", SortName: "A"}
	if err := NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := NewBookRepo(database)
	b := &models.Book{ForeignID: "b-rb", AuthorID: a.ID, Title: "T", SortTitle: "T", Status: models.BookStatusImported, MediaType: models.MediaTypeEbook, Monitored: true}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, b.ID, "ebook", "/lib/t.epub"); err != nil {
		t.Fatal(err)
	}

	in := RejectImportInput{BookID: b.ID, Paths: []string{"/lib/t.epub"}, GUID: "g-rb", Title: "T", Scope: models.BlocklistScopeBook, Reason: "bookImported"}
	count := func(q string) int {
		var n int
		if err := database.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	boom := errors.New("injected")
	rejectImportBeforeCommit = func() error { return boom }
	t.Cleanup(func() { rejectImportBeforeCommit = nil })
	if _, err := books.RejectImport(ctx, in); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if n := count(`SELECT COUNT(*) FROM book_files`); n != 1 {
		t.Errorf("file rows = %d, want 1 (rolled back)", n)
	}
	if n := count(`SELECT COUNT(*) FROM blocklist`); n != 0 {
		t.Errorf("blocklist rows = %d, want 0", n)
	}
	if n := count(`SELECT COUNT(*) FROM history`); n != 0 {
		t.Errorf("history rows = %d, want 0", n)
	}
	got, err := books.GetByID(ctx, b.ID)
	if err != nil || got == nil || got.Status != models.BookStatusImported {
		t.Errorf("book = %+v, %v, want status imported", got, err)
	}

	// Without the failure the same input commits every part.
	rejectImportBeforeCommit = nil
	res, err := books.RejectImport(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.BookStatusWanted || !res.Blocklisted || len(res.DetachedPaths) != 1 {
		t.Errorf("result = %+v", res)
	}
	if count(`SELECT COUNT(*) FROM book_files`) != 0 || count(`SELECT COUNT(*) FROM blocklist`) != 1 || count(`SELECT COUNT(*) FROM history`) != 1 {
		t.Errorf("committed state wrong")
	}
}
