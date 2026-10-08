package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestFixMatch_CorrectMatchOnlyLeavesFileInPlace is #2055: Fix match ran the
// full import, so correcting which book a file belongs to also moved it into
// the target's folder and renamed it, replacing the user's layout. With
// relocate=false the association moves and the file stays exactly where it
// was, with nothing imported, renamed or deleted.
func TestFixMatch_CorrectMatchOnlyLeavesFileInPlace(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	src := f.writeFile(t, "My Layout/Saga/vol3 by hand.epub", "volume three")
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, src); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"path": src, "targetBookId": f.right.ID, "relocate": false})
	rec := httptest.NewRecorder()
	f.h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (done, nothing in the background); body = %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the file was moved off its path: %v", err)
	}
	files := f.files(t, f.right.ID)
	if len(files) != 1 || files[0].Path != src || files[0].Format != models.MediaTypeEbook {
		t.Fatalf("Vol 3 files = %+v, want the one row at %s recorded as ebook", files, src)
	}
	if files := f.files(t, f.wrong.ID); len(files) != 0 {
		t.Errorf("Vol 17 still has %+v", files)
	}
	all, err := f.downloads.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("an import record was created (%d downloads): correct match only must not run an import", len(all))
	}

	right, err := f.books.GetByID(ctx, f.right.ID)
	if err != nil {
		t.Fatal(err)
	}
	if right.Status != models.BookStatusImported || right.EbookFilePath != src {
		t.Errorf("Vol 3 status=%q ebook=%q, want imported at %s", right.Status, right.EbookFilePath, src)
	}
	wrong, err := f.books.GetByID(ctx, f.wrong.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Status != models.BookStatusWanted || wrong.EbookFilePath != "" {
		t.Errorf("Vol 17 status=%q ebook=%q, want wanted with no file", wrong.Status, wrong.EbookFilePath)
	}

	moved, err := f.history.ListByType(ctx, "bookFileMoved")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || !strings.Contains(moved[0].Data, f.wrong.Title) {
		t.Fatalf("bookFileMoved history = %+v, want one row naming %q", moved, f.wrong.Title)
	}
}

// TestFixMatch_CorrectMatchOnlyUntrackedFileIsRecorded: relinking a file no
// book tracked still attaches it, and still leaves a history row, so the
// change is not invisible on the book's History.
func TestFixMatch_CorrectMatchOnlyUntrackedFileIsRecorded(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	src := f.writeFile(t, "Loose/vol3.epub", "volume three")

	raw, _ := json.Marshal(map[string]any{"path": src, "targetBookId": f.right.ID, "relocate": false})
	rec := httptest.NewRecorder()
	f.h.Reassign(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/manual-import/reassign", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	if files := f.files(t, f.right.ID); len(files) != 1 || files[0].Path != src {
		t.Fatalf("Vol 3 files = %+v, want the one row at %s", files, src)
	}
	moved, err := f.history.ListByType(ctx, "bookFileMoved")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].BookID == nil || *moved[0].BookID != f.right.ID || !strings.Contains(moved[0].Data, src) {
		t.Fatalf("bookFileMoved history = %+v, want one row on Vol 3 naming %s", moved, src)
	}
}
