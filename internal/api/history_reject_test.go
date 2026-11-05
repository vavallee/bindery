package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// rejectFixture is a library root, a book and one bookImported event.
type rejectFixture struct {
	t       *testing.T
	h       *HistoryHandler
	sqlDB   *sql.DB
	books   *db.BookRepo
	root    string
	bookID  int64
	eventID int64
}

// newRejectFixture builds an imported book whose bookImported event carries
// eventData(root).
func newRejectFixture(t *testing.T, mediaType string, eventData func(root string) map[string]any) *rejectFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	root := t.TempDir()

	a := &models.Author{ForeignID: "a-rej", Name: "A", SortName: "A"}
	if err := db.NewAuthorRepo(database).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	books := db.NewBookRepo(database)
	b := &models.Book{ForeignID: "b-rej", AuthorID: a.ID, Title: "Rejected", SortTitle: "Rejected", Status: models.BookStatusImported, MediaType: mediaType, Monitored: true}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	hist := db.NewHistoryRepo(database)
	raw, _ := json.Marshal(eventData(root))
	ev := &models.HistoryEvent{BookID: &b.ID, EventType: models.HistoryEventBookImported, SourceTitle: "Rejected.Release", Data: string(raw)}
	if err := hist.Create(ctx, ev); err != nil {
		t.Fatal(err)
	}
	// In production the file row is written before the import's event. The
	// fixture adds files after creating the event, so date the event ahead of
	// them to keep that order.
	if _, err := database.Exec(`UPDATE history SET created_at = datetime('now','+1 minute') WHERE id = ?`, ev.ID); err != nil {
		t.Fatal(err)
	}
	h := NewHistoryHandler(hist, db.NewBlocklistRepo(database), books).WithLibraryRoots(NewLibraryRoots(nil, root))
	return &rejectFixture{t: t, h: h, sqlDB: database, books: books, root: root, bookID: b.ID, eventID: ev.ID}
}

func (f *rejectFixture) file(format, path string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := f.books.AddBookFile(context.Background(), f.bookID, format, path); err != nil {
		f.t.Fatal(err)
	}
}

func (f *rejectFixture) reject() (*httptest.ResponseRecorder, rejectImportResponse) {
	f.t.Helper()
	id := strconv.FormatInt(f.eventID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+id+"/reject-import", nil), "id", id)
	rec := httptest.NewRecorder()
	f.h.RejectImport(rec, req)
	var resp rejectImportResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			f.t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
	}
	return rec, resp
}

func (f *rejectFixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.sqlDB.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *rejectFixture) status() string {
	f.t.Helper()
	b, err := f.books.GetByID(context.Background(), f.bookID)
	if err != nil || b == nil {
		f.t.Fatalf("book: %v %v", b, err)
	}
	return b.Status
}

// A reject detaches the files the import placed, deletes them (inside the
// library), blocklists the release for this book only and returns the book to
// Wanted. A file from another import of the same book stays tracked.
func TestRejectImport_DetachesBlocklistsAndReturnsToWanted(t *testing.T) {
	var epub, mobi string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		epub, mobi = filepath.Join(root, "A", "Rejected", "r.epub"), filepath.Join(root, "A", "Rejected", "r.mobi")
		return map[string]any{"origin": "download", "guid": "guid-3250", "format": "ebook", "paths": []string{epub, mobi}, "path": filepath.Dir(epub)}
	})
	f.file("ebook", epub)
	f.file("ebook", mobi)

	rec, resp := f.reject()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if resp.Status != models.BookStatusWanted {
		t.Errorf("status = %q, want wanted", resp.Status)
	}
	if got := f.status(); got != models.BookStatusWanted {
		t.Errorf("book status = %q, want wanted", got)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_files WHERE book_id=?`, f.bookID); n != 0 {
		t.Errorf("%d file rows left", n)
	}
	if exists(epub) || exists(mobi) || len(resp.Deleted) != 2 {
		t.Errorf("files should be deleted: %+v", resp)
	}
	var scope string
	var bookID int64
	if err := f.sqlDB.QueryRow(`SELECT scope, book_id FROM blocklist WHERE guid='guid-3250'`).Scan(&scope, &bookID); err != nil {
		t.Fatalf("no blocklist entry: %v", err)
	}
	if scope != models.BlocklistScopeBook || bookID != f.bookID || !resp.Blocklisted {
		t.Errorf("entry scope=%q book=%d resp=%+v", scope, bookID, resp)
	}
	if n := f.count(`SELECT COUNT(*) FROM history WHERE event_type=?`, models.HistoryEventImportRejected); n != 1 {
		t.Errorf("importRejected events = %d, want 1", n)
	}

	// Rejecting it again is refused, and writes nothing.
	rec, _ = f.reject()
	if rec.Code != http.StatusConflict {
		t.Errorf("repeat reject = %d, want 409", rec.Code)
	}
	if n := f.count(`SELECT COUNT(*) FROM blocklist`); n != 1 {
		t.Errorf("blocklist rows = %d, want 1", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM history WHERE event_type=?`, models.HistoryEventImportRejected); n != 1 {
		t.Errorf("importRejected events = %d, want 1", n)
	}
	if !f.listedRejected(f.eventID) {
		t.Errorf("History list should flag the event as rejected")
	}
}

// Only the rejected import's files go. Another file the book tracks stays, and
// so does the book's imported status because its format is still satisfied.
func TestRejectImport_LeavesOtherFilesOfTheBook(t *testing.T) {
	var rejected, other string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		rejected, other = filepath.Join(root, "A", "Rejected", "r.mobi"), filepath.Join(root, "A", "Rejected", "keep.epub")
		return map[string]any{"origin": "download", "guid": "guid-multi", "format": "ebook", "paths": []string{rejected}, "path": rejected}
	})
	f.file("ebook", rejected)
	f.file("ebook", other)

	rec, resp := f.reject()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if exists(rejected) || !exists(other) {
		t.Errorf("rejected exists=%v other exists=%v", exists(rejected), exists(other))
	}
	if n := f.count(`SELECT COUNT(*) FROM book_files WHERE book_id=? AND path=?`, f.bookID, other); n != 1 {
		t.Errorf("other file row gone")
	}
	if resp.Status != models.BookStatusImported {
		t.Errorf("status = %q, want imported (another file remains)", resp.Status)
	}
}

// A file outside every library root is detached but never deleted.
func TestRejectImport_OutsideLibraryRootIsDetachedNotDeleted(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.epub")
	f := newRejectFixture(t, models.MediaTypeEbook, func(string) map[string]any {
		return map[string]any{"origin": "download", "guid": "guid-out", "format": "ebook", "paths": []string{outside}, "path": outside}
	})
	f.file("ebook", outside)

	_, resp := f.reject()
	if !exists(outside) || len(resp.KeptOnDisk) != 1 || len(resp.Deleted) != 0 {
		t.Errorf("outside file must stay on disk: %+v", resp)
	}
	if n := f.count(`SELECT COUNT(*) FROM book_files`); n != 0 {
		t.Errorf("row should be detached")
	}
	if f.status() != models.BookStatusWanted {
		t.Errorf("status = %q", f.status())
	}
}

// An import with no recorded release (manual, or from before #3248) is acted
// on as far as it can be: the file is detached and the book is Wanted, nothing
// is blocklisted, and the response says so rather than failing.
func TestRejectImport_NoOriginDoesWhatItCanAndSaysSo(t *testing.T) {
	var p string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "old.epub")
		return map[string]any{"format": "ebook", "path": p} // a pre #3248 row
	})
	f.file("ebook", p)

	rec, resp := f.reject()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if resp.ReleaseKnown || resp.Blocklisted || resp.AlreadyBlocklisted {
		t.Errorf("no release was known: %+v", resp)
	}
	if n := f.count(`SELECT COUNT(*) FROM blocklist`); n != 0 {
		t.Errorf("blocklist rows = %d, want 0", n)
	}
	if exists(p) || f.status() != models.BookStatusWanted {
		t.Errorf("file/ status wrong: %+v", resp)
	}
}

// A file already deleted by hand: the row is detached, the response reports it
// gone, and the book is Wanted.
func TestRejectImport_FileAlreadyRemovedFromDisk(t *testing.T) {
	var p string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "gone.epub")
		return map[string]any{"origin": "download", "guid": "guid-gone", "format": "ebook", "paths": []string{p}}
	})
	f.file("ebook", p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if len(resp.AlreadyGone) != 1 || len(resp.Deleted) != 0 || !resp.Blocklisted {
		t.Errorf("resp = %+v", resp)
	}
	if f.status() != models.BookStatusWanted {
		t.Errorf("status = %q", f.status())
	}
}

// An audiobook import records its folder; the whole folder is the tracked row.
func TestRejectImport_Audiobook(t *testing.T) {
	var dir string
	f := newRejectFixture(t, models.MediaTypeAudiobook, func(root string) map[string]any {
		dir = filepath.Join(root, "A", "Rejected")
		return map[string]any{"origin": "download", "guid": "guid-audio", "format": "audiobook", "path": dir}
	})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(context.Background(), f.bookID, "audiobook", dir); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if exists(filepath.Join(dir, "01.mp3")) || len(resp.Deleted) != 1 {
		t.Errorf("audiobook files should be deleted: %+v", resp)
	}
	if f.status() != models.BookStatusWanted {
		t.Errorf("status = %q", f.status())
	}
}

// Under tenancy a user may only reject the imports of their own books.
func TestRejectImport_TenancyScopesToTheOwnersBooks(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, database, uAlice, uBob, eAlice, _ := seedTwoUserHistory(t)
	if _, err := database.Exec(`UPDATE history SET event_type=?, data='{"guid":"g-t","format":"ebook"}' WHERE id=?`, models.HistoryEventBookImported, eAlice); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(eAlice, 10)
	call := func(uid int64, role string) int {
		req := withURLParam(httptest.NewRequest(http.MethodPost, "/x", nil), "id", id)
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), uid), role))
		rec := httptest.NewRecorder()
		h.RejectImport(rec, req)
		return rec.Code
	}
	if code := call(uBob, "user"); code != http.StatusNotFound {
		t.Fatalf("bob rejecting alice's import got %d, want 404", code)
	}
	var n int
	_ = database.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&n)
	if n != 0 {
		t.Errorf("bob's refused reject wrote %d blocklist rows", n)
	}
	if code := call(uAlice, "user"); code != http.StatusOK {
		t.Fatalf("alice rejecting her own import got %d, want 200", code)
	}
}

// Only a bookImported event can be rejected.
func TestRejectImport_RefusesOtherEventTypes(t *testing.T) {
	f := newRejectFixture(t, models.MediaTypeEbook, func(string) map[string]any { return map[string]any{"guid": "g"} })
	if _, err := f.sqlDB.Exec(`UPDATE history SET event_type='grabbed' WHERE id=?`, f.eventID); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.reject()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// The importRejected row stores the release GUID raw, like every import row,
// and History redacts it for every viewer: a URL shaped GUID with a Jackett
// key must not reach a non admin.
func TestRejectImport_HistoryDataDoesNotLeakGuidCredentials(t *testing.T) {
	secretGUID := "https://jackett.example/dl/idx/?jackett_apikey=SECRETKEY123&passkey=PASSKEY456&file=Book"
	var p string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "r.epub")
		return map[string]any{"origin": "download", "guid": secretGUID, "format": "ebook", "paths": []string{p}}
	})
	f.file("ebook", p)
	if rec, _ := f.reject(); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	// The raw GUID is what blocklist matching needs, and it is stored.
	var raw string
	if err := f.sqlDB.QueryRow(`SELECT data FROM history WHERE event_type=?`, models.HistoryEventImportRejected).Scan(&raw); err != nil || !strings.Contains(raw, "SECRETKEY123") {
		t.Fatalf("stored data = %q, %v", raw, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history", nil)
	req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), 5), "user"))
	rec := httptest.NewRecorder()
	f.h.List(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, models.HistoryEventImportRejected) {
		t.Fatalf("event missing from list: %s", body)
	}
	for _, leak := range []string{"SECRETKEY123", "PASSKEY456"} {
		if strings.Contains(body, leak) {
			t.Errorf("History leaks %q to a non admin: %s", leak, body)
		}
	}
}

func (f *rejectFixture) listedRejected(id int64) bool {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	var page struct {
		Items []struct {
			ID       int64 `json:"id"`
			Rejected bool  `json:"rejected"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		f.t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.ID == id {
			return it.Rejected
		}
	}
	return false
}

// A later import of the same book to the same path keeps the old book_files
// row, so the older event must not be able to detach or delete the newer
// file. Nothing is written for a reject that does nothing.
func TestRejectImport_NewerImportAtSamePathSurvivesRejectingTheOlderEvent(t *testing.T) {
	var p string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "r.epub")
		return map[string]any{"origin": "download", "guid": "guid-old", "format": "ebook", "paths": []string{p}}
	})
	f.file("ebook", p)
	// The same path is imported again, by a different release.
	newer := &models.HistoryEvent{BookID: &f.bookID, EventType: models.HistoryEventBookImported, SourceTitle: "Newer.Release",
		Data: `{"origin":"download","guid":"guid-new","format":"ebook","paths":["` + p + `"]}`}
	if err := db.NewHistoryRepo(f.sqlDB).Create(context.Background(), newer); err != nil {
		t.Fatal(err)
	}

	rec, resp := f.reject()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if !resp.NoOp || len(resp.SupersededPaths) != 1 || len(resp.Deleted) != 0 || len(resp.Detached) != 0 {
		t.Errorf("resp = %+v", resp)
	}
	if !exists(p) || f.count(`SELECT COUNT(*) FROM book_files WHERE book_id=? AND path=?`, f.bookID, p) != 1 {
		t.Errorf("the newer file or its row was touched")
	}
	if f.count(`SELECT COUNT(*) FROM blocklist`) != 0 || f.count(`SELECT COUNT(*) FROM history WHERE event_type=?`, models.HistoryEventImportRejected) != 0 {
		t.Errorf("a no-op reject wrote a blocklist entry or history row")
	}
	if f.status() != models.BookStatusImported {
		t.Errorf("status = %q, want imported", f.status())
	}
}

// A tracked row created after the event is a newer file even with no newer
// import event naming it.
func TestRejectImport_RowCreatedAfterTheEventIsLeftAlone(t *testing.T) {
	var p string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "r.epub")
		return map[string]any{"origin": "download", "guid": "g-late", "format": "ebook", "paths": []string{p}}
	})
	f.file("ebook", p)
	if _, err := f.sqlDB.Exec(`UPDATE book_files SET created_at = datetime('now','+1 hour')`); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if !resp.NoOp || len(resp.SupersededPaths) != 1 || !exists(p) {
		t.Errorf("resp = %+v exists=%v", resp, exists(p))
	}
}

// A path under a folder another book tracks is detached but not deleted.
func TestRejectImport_PathUnderAnotherBooksFolderIsKept(t *testing.T) {
	var p, folder string
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		folder = filepath.Join(root, "Box")
		p = filepath.Join(folder, "r.epub")
		return map[string]any{"origin": "download", "guid": "g-own", "format": "ebook", "paths": []string{p}}
	})
	f.file("ebook", p)
	a := &models.Author{ForeignID: "a-o", Name: "O", SortName: "O"}
	if err := db.NewAuthorRepo(f.sqlDB).Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	other := &models.Book{ForeignID: "b-o", AuthorID: a.ID, Title: "Other", SortTitle: "Other", MediaType: models.MediaTypeAudiobook}
	if err := f.books.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := f.books.AddBookFile(context.Background(), other.ID, "audiobook", folder); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if !exists(p) || len(resp.KeptOnDisk) != 1 || len(resp.Deleted) != 0 || len(resp.Detached) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

// Removing the library name must not touch a hardlink to the same data.
func TestRejectImport_HardlinkedSeedCopySurvives(t *testing.T) {
	var p string
	seed := filepath.Join(t.TempDir(), "seed.epub")
	f := newRejectFixture(t, models.MediaTypeEbook, func(root string) map[string]any {
		p = filepath.Join(root, "A", "Rejected", "r.epub")
		return map[string]any{"origin": "download", "guid": "g-hl", "format": "ebook", "paths": []string{p}}
	})
	if err := os.WriteFile(seed, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(seed, p); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}
	if err := f.books.AddBookFile(context.Background(), f.bookID, "ebook", p); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if exists(p) || len(resp.Deleted) != 1 {
		t.Errorf("library link should be gone: %+v", resp)
	}
	if b, err := os.ReadFile(seed); err != nil || string(b) != "payload" {
		t.Errorf("seed copy damaged: %q %v", b, err)
	}
}

// A book that also holds ebook and audiobook files: rejecting the ebook
// import returns it to Wanted (the ebook is needed again) and keeps the
// audiobook row.
func TestRejectImport_EbookAndAudiobookBook(t *testing.T) {
	var epub, dir string
	f := newRejectFixture(t, models.MediaTypeBoth, func(root string) map[string]any {
		epub = filepath.Join(root, "A", "Rejected", "r.epub")
		dir = filepath.Join(root, "A", "Rejected-audio")
		return map[string]any{"origin": "download", "guid": "g-both", "format": "ebook", "paths": []string{epub}}
	})
	f.file("ebook", epub)
	f.file("audiobook", filepath.Join(dir, "01.mp3"))
	if _, err := f.sqlDB.Exec(`UPDATE book_files SET path=? WHERE format='audiobook'`, dir); err != nil {
		t.Fatal(err)
	}
	_, resp := f.reject()
	if resp.Status != models.BookStatusWanted || f.status() != models.BookStatusWanted {
		t.Errorf("status = %q / %q, want wanted", resp.Status, f.status())
	}
	if f.count(`SELECT COUNT(*) FROM book_files WHERE format='audiobook'`) != 1 || !exists(filepath.Join(dir, "01.mp3")) {
		t.Errorf("audiobook must be untouched")
	}
}
