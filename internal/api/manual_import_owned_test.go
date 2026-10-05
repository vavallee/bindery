package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// ownedFixture wires a real importer behind the manual import handler, so a
// Fix match or manual import runs the whole pipeline down to book_files.
type ownedFixture struct {
	h         *ManualImportHandler
	books     *db.BookRepo
	downloads *db.DownloadRepo
	history   *db.HistoryRepo
	scanner   *importer.Scanner
	right     *models.Book // Vol 3, the book the file really is
	wrong     *models.Book // Vol 17, the book the file was attached to
	library   string
}

func newOwnedFixture(t *testing.T) *ownedFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	downloads := db.NewDownloadRepo(database)
	history := db.NewHistoryRepo(database)
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, "import.mode", "copy"); err != nil {
		t.Fatal(err)
	}

	author := &models.Author{ForeignID: "OL-FIX-A", Name: "TJF Saga", SortName: "Saga, TJF", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mk := func(foreign, title string) *models.Book {
		b := &models.Book{
			ForeignID: foreign, AuthorID: author.ID, Title: title, SortTitle: strings.ToLower(title),
			Status: models.BookStatusWanted, Monitored: true, MediaType: models.MediaTypeEbook,
			MetadataProvider: "openlibrary", Genres: []string{},
		}
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	right := mk("OL-FIX-3", "Defiance of the Fall 3")
	wrong := mk("OL-FIX-17", "Defiance of the Fall 17")

	library := t.TempDir()
	scanner := importer.NewScanner(downloads, db.NewDownloadClientRepo(database), books, authors, history, library, "", "", "", "")
	scanner.WithSettings(settings)
	h := NewManualImportHandler(scanner, downloads, books)
	return &ownedFixture{
		h: h, books: books, downloads: downloads, history: history, scanner: scanner,
		right: right, wrong: wrong, library: library,
	}
}

func (f *ownedFixture) post(t *testing.T, handler http.HandlerFunc, url string, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
}

// wait polls until the background import the handler started has settled
// the download, and until gone (when not empty) has left the disk: Fix match
// removes the misfiled source once the import has placed the file.
func (f *ownedFixture) wait(t *testing.T, gone string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		all, err := f.downloads.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		settled := false
		if len(all) == 1 {
			switch all[0].Status {
			case models.StateImported, models.StateImportFailed, models.StateImportBlocked:
				settled = true
			}
		}
		if settled && gone != "" {
			if _, err := os.Stat(gone); err == nil {
				settled = false
			}
		}
		if settled {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background import did not settle")
}

func (f *ownedFixture) files(t *testing.T, bookID int64) []models.BookFile {
	t.Helper()
	files, err := f.books.ListBookFiles(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (f *ownedFixture) onlyDownload(t *testing.T) models.Download {
	t.Helper()
	all, err := f.downloads.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("downloads = %d, want 1", len(all))
	}
	return all[0]
}

// writeFile drops an EPUB under the library at rel and returns its path.
func (f *ownedFixture) writeFile(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.library, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestManualImport_PathOwnedByOtherBook_FailsNamingOwner: a manual import of
// a file onto Vol 3 whose destination Vol 17 already tracks (#2937). It used
// to report success and leave Vol 3 Wanted with nothing recorded.
func TestManualImport_PathOwnedByOtherBook_FailsNamingOwner(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	src := f.writeFile(t, "incoming/vol3.epub", "volume three")
	preview, err := f.scanner.PreviewImportDestination(ctx, f.right.ID, src, models.MediaTypeEbook)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, preview.Destination); err != nil {
		t.Fatal(err)
	}

	f.post(t, f.h.Import, "/api/v1/queue/manual-import",
		map[string]any{"path": src, "bookId": f.right.ID, "format": models.MediaTypeEbook})
	f.wait(t, "")

	dl := f.onlyDownload(t)
	if dl.Status != models.StateImportBlocked {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImportBlocked)
	}
	for _, want := range []string{f.wrong.Title, fmt.Sprintf("%d", f.wrong.ID)} {
		if !strings.Contains(dl.ErrorMessage, want) {
			t.Errorf("failure %q does not name the owning book (%s)", dl.ErrorMessage, want)
		}
	}
	if files := f.files(t, f.right.ID); len(files) != 0 {
		t.Errorf("Vol 3 files = %+v, want none", files)
	}
	if files := f.files(t, f.wrong.ID); len(files) != 1 {
		t.Errorf("Vol 17 files = %+v, want its row untouched", files)
	}
}

// TestFixMatch_MovesFileAndRecordsHistory: Fix match is the explicit "this
// file belongs to that book" action, so the row moves from Vol 17 to Vol 3
// and the move is written to history naming both books.
func TestFixMatch_MovesFileAndRecordsHistory(t *testing.T) {
	f := newOwnedFixture(t)
	ctx := context.Background()
	src := f.writeFile(t, "misfiled/vol3.epub", "volume three")
	if err := f.books.AddBookFile(ctx, f.wrong.ID, models.MediaTypeEbook, src); err != nil {
		t.Fatal(err)
	}

	f.post(t, f.h.Reassign, "/api/v1/queue/manual-import/reassign",
		map[string]any{"path": src, "targetBookId": f.right.ID, "format": models.MediaTypeEbook})
	f.wait(t, src)

	if dl := f.onlyDownload(t); dl.Status != models.StateImported {
		t.Fatalf("download status = %q (%s), want %q", dl.Status, dl.ErrorMessage, models.StateImported)
	}
	if files := f.files(t, f.wrong.ID); len(files) != 0 {
		t.Errorf("Vol 17 still has %+v", files)
	}
	files := f.files(t, f.right.ID)
	if len(files) != 1 {
		t.Fatalf("Vol 3 files = %+v, want 1", files)
	}
	moved, err := f.history.ListByType(ctx, "bookFileMoved")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 {
		t.Fatalf("got %d bookFileMoved history rows, want 1", len(moved))
	}
	ev := moved[0]
	if ev.BookID == nil || *ev.BookID != f.right.ID {
		t.Errorf("move row book = %v, want %d", ev.BookID, f.right.ID)
	}
	for _, want := range []string{f.wrong.Title, fmt.Sprintf("%d", f.wrong.ID), files[0].Path} {
		if !strings.Contains(ev.Data, want) {
			t.Errorf("move row data %s does not mention %q", ev.Data, want)
		}
	}
}
