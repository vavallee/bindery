package importer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// Reject import (#3250) judges "a newer import owns this file" by comparing the
// file row's created_at with the event's. The API tests hand-date the event, so
// this one drives the production import write path and rejects with the event
// read back from the database: a file and its own import event land in the same
// second and must be detached and blocklisted, not treated as superseded.
func TestRejectImport_AfterRealImportDetachesAndBlocklists(t *testing.T) {
	f := newPathOwnedFixture(t, models.MediaTypeEbook)
	ctx := context.Background()
	dest := f.ebookDest(t)
	f.importDownload(t)

	events, err := f.history.ListByBook(ctx, f.right.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ev *models.HistoryEvent
	for i := range events {
		if events[i].EventType == models.HistoryEventBookImported {
			ev = &events[i]
		}
	}
	if ev == nil {
		t.Fatal("import wrote no bookImported event")
	}
	var data struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		t.Fatal(err)
	}
	paths := data.Paths
	if len(paths) == 0 && data.Path != "" {
		paths = []string{data.Path}
	}
	if len(paths) == 0 {
		t.Fatalf("event names no path: %s", ev.Data)
	}

	res, err := f.books.RejectImport(ctx, db.RejectImportInput{
		BookID: f.right.ID, EventID: ev.ID, EventCreatedAt: ev.CreatedAt,
		Paths: paths, GUID: f.dl.GUID, Title: ev.SourceTitle,
		Scope: models.BlocklistScopeBook, Reason: models.HistoryEventBookImported,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.NoOp || len(res.SupersededPaths) != 0 {
		t.Fatalf("a fresh import was treated as superseded or a no-op: %+v (dest %s)", res, dest)
	}
	if len(res.DetachedPaths) == 0 || !res.Blocklisted {
		t.Fatalf("want detach and blocklist, got %+v", res)
	}
	var left int
	if err := f.database.QueryRow(`SELECT COUNT(*) FROM book_files WHERE book_id = ?`, f.right.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d book_files rows left", left)
	}
	if res.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want wanted", res.Status)
	}
}
