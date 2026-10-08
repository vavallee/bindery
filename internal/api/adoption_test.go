package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/jobs"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

type adoptionFixture struct {
	h       *AdoptionHandler
	router  chi.Router
	units   *db.UnmatchedUnitRepo
	books   *db.BookRepo
	authors *db.AuthorRepo
	lib     string
	db      *sql.DB
}

// newAdoptionFixture wires the handler over an in memory database, a real
// AuthorHandler on the given provider (T1: the stubMetaProvider doubles), and
// a temporary library root.
func newAdoptionFixture(t *testing.T, provider metadata.Provider) adoptionFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	group := jobs.NewGroup(context.Background())
	t.Cleanup(func() { group.Shutdown(5 * time.Second) })
	ah := NewAuthorHandler(authors, nil, books, nil, metadata.NewAggregator(provider), nil,
		db.NewMetadataProfileRepo(database), nil).WithJobs(group)
	lib := t.TempDir()
	if real, err := filepath.EvalSymlinks(lib); err == nil {
		lib = real
	}
	units := db.NewUnmatchedUnitRepo(database)
	h := NewAdoptionHandler(units, books, authors, ah, NewLibraryRoots(nil, lib), nil, db.NewSettingsRepo(database))
	r := chi.NewRouter()
	r.Get("/library/unmatched", h.List)
	r.Get("/library/unmatched/summary", h.Summary)
	r.Post("/library/unmatched/ignore", h.IgnoreMany)
	r.Post("/library/unmatched/{id}/adopt", h.Adopt)
	r.Post("/library/unmatched/{id}/undo", h.Undo)
	r.Post("/library/unmatched/{id}/ignore", h.Ignore)
	r.Post("/library/unmatched/{id}/unignore", h.Unignore)
	return adoptionFixture{h: h, router: r, units: units, books: books, authors: authors, lib: lib, db: database}
}

func (f adoptionFixture) write(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(f.lib, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// Book sized: adoption refuses an ebook too small to be a book (#2944).
	if err := os.WriteFile(p, bytes.Repeat([]byte("book "), 2<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedUnit stores one unit the way a scan would and returns its id.
func (f adoptionFixture) seedUnit(t *testing.T, u db.UnmatchedUnitScan) int64 {
	t.Helper()
	if u.UnitKind == "" {
		u.UnitKind = db.UnmatchedKindFile
	}
	if u.Format == "" {
		u.Format = models.MediaTypeEbook
	}
	u.FileCount = len(u.MemberPaths)
	u.RootPath = f.lib
	if _, err := f.units.ReconcileScan(context.Background(), []db.UnmatchedUnitScan{u}, db.ReconcileScanOptions{SkipDeletion: true}); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.units.List(context.Background(), db.UnmatchedListQuery{Limit: 250})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.UnitPath == u.UnitPath {
			return it.ID
		}
	}
	t.Fatalf("seeded unit %s not listed", u.UnitPath)
	return 0
}

func (f adoptionFixture) post(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, &buf))
	return rec
}

func (f adoptionFixture) seedBook(t *testing.T, title string) *models.Book {
	t.Helper()
	ctx := context.Background()
	author := &models.Author{ForeignID: "ol:" + title, Name: "Ann Leckie", SortName: "Leckie, Ann", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	b := &models.Book{ForeignID: "ol:b:" + title, AuthorID: author.ID, Title: title, Status: models.BookStatusWanted,
		Monitored: true, MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeItem(t *testing.T, rec *httptest.ResponseRecorder) adoptionItem {
	t.Helper()
	var it adoptionItem
	if err := json.Unmarshal(rec.Body.Bytes(), &it); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return it
}

func filePaths(t *testing.T, books *db.BookRepo, bookID int64) []string {
	t.Helper()
	files, err := books.ListFiles(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestAdopt_ExistingBookRegistersInPlaceAndUndoIsExact: adopting into a book
// already in the library registers the file where it is, and undo removes
// exactly that registration and leaves the book.
func TestAdopt_ExistingBookRegistersInPlaceAndUndoIsExact(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Ancillary Justice")
	path := f.write(t, "Ann Leckie/Ancillary Justice.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	it := decodeItem(t, rec)
	if it.State != db.UnmatchedStateAdopted || it.Book == nil || it.Book.ID != book.ID || it.BookCreated || it.AuthorCreated {
		t.Fatalf("adopted item = %+v", it)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 || got[0] != path {
		t.Fatalf("book files = %v, want the file in place", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file moved or removed: %v", err)
	}
	after, _ := f.books.GetByID(context.Background(), book.ID)
	if after.Status != models.BookStatusImported || !after.Monitored {
		t.Fatalf("book after adopt = status %s monitored %v, want imported and monitoring untouched", after.Status, after.Monitored)
	}

	rec = f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
	if rec.Code != http.StatusOK || decodeItem(t, rec).State != db.UnmatchedStatePending {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("book files after undo = %v, want none", got)
	}
	if b, _ := f.books.GetByID(context.Background(), book.ID); b == nil || b.Status != models.BookStatusWanted {
		t.Fatalf("existing book after undo = %+v, want kept and wanted again", b)
	}
}

// TestAdopt_CreatesUnmonitoredBookInTheAdoptedFormat: a metadata result is
// added unmonitored with media type set to the unit's format, so nothing is
// left Wanted to be grabbed. An audiobook folder registers as its folder.
// Undo removes the created book and author again.
func TestAdopt_CreatesUnmonitoredBookInTheAdoptedFormat(t *testing.T) {
	f := newAdoptionFixture(t, addBookBackCatalogueStub(false))
	var tracks []string
	for i := 1; i <= 3; i++ {
		tracks = append(tracks, f.write(t, fmt.Sprintf("H. G. Wells/War of the Worlds/%02d.mp3", i)))
	}
	folder := filepath.Dir(tracks[0])
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: folder, UnitKind: db.UnmatchedKindFolder,
		Format: models.MediaTypeAudiobook, MemberPaths: tracks})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{
		"foreignBookId": "OL27482W", "foreignAuthorId": "OL39307A", "authorName": "H. G. Wells",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	it := decodeItem(t, rec)
	if !it.BookCreated || !it.AuthorCreated || it.Book == nil {
		t.Fatalf("item = %+v, want a created book and author", it)
	}
	book, _ := f.books.GetByID(context.Background(), it.Book.ID)
	if book.Monitored || book.MediaType != models.MediaTypeAudiobook || book.Status != models.BookStatusImported {
		t.Fatalf("created book = monitored %v, media %s, status %s; want unmonitored audiobook, imported",
			book.Monitored, book.MediaType, book.Status)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 || got[0] != folder {
		t.Fatalf("registered = %v, want the folder %s", got, folder)
	}

	authorID := book.AuthorID
	rec = f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := f.books.GetByID(context.Background(), book.ID); b != nil {
		t.Fatalf("created book survived undo: %+v", b)
	}
	if a, _ := f.authors.GetByID(context.Background(), authorID); a != nil {
		t.Fatalf("created author survived undo: %+v", a)
	}
}

// TestAdopt_InjectedFailureStrandsNothing: registration failing part way
// leaves no file row, no created book or author, and the row pending.
func TestAdopt_InjectedFailureStrandsNothing(t *testing.T) {
	f := newAdoptionFixture(t, addBookBackCatalogueStub(false))
	epub := f.write(t, "H. G. Wells/War of the Worlds.epub")
	mobi := f.write(t, "H. G. Wells/War of the Worlds.mobi")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: epub, MemberPaths: []string{epub, mobi}})

	real := f.h.registerFile
	calls := 0
	f.h.registerFile = func(ctx context.Context, bookID int64, format, path string) (bool, error) {
		calls++
		if calls == 2 {
			return false, errors.New("disk on fire")
		}
		return real(ctx, bookID, format, path)
	}
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{
		"foreignBookId": "OL27482W", "foreignAuthorId": "OL39307A", "authorName": "H. G. Wells",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("adopt = %d %s, want 500", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if b, _ := f.books.GetByForeignID(ctx, "OL27482W"); b != nil {
		t.Fatalf("created book stranded: %+v", b)
	}
	if a, _ := f.authors.GetByForeignID(ctx, "OL39307A"); a != nil {
		t.Fatalf("created author stranded: %+v", a)
	}
	for _, p := range []string{epub, mobi} {
		if owned, _ := f.books.PathOwnedByOtherBook(ctx, p, 0); owned {
			t.Fatalf("%s still registered", p)
		}
	}
	if u, _ := f.units.Get(ctx, id); u.State != db.UnmatchedStatePending {
		t.Fatalf("unit state = %s, want pending", u.State)
	}
}

// TestAdopt_RefusesAFileAnotherBookOwns is the 409.
func TestAdopt_RefusesAFileAnotherBookOwns(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	owner := f.seedBook(t, "Provenance")
	target := f.seedBook(t, "Translation State")
	path := f.write(t, "Ann Leckie/Provenance.epub")
	if err := f.books.AddBookFile(context.Background(), owner.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": target.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("adopt = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, target.ID); len(got) != 0 {
		t.Fatalf("target gained %v", got)
	}
}

// TestAdopt_FileTakenAfterTheCheckIs409: another book takes the file between
// the ownership check and the write (#2937). The write used to be an OR
// IGNORE that reported "not inserted" and the adopt succeeded with nothing
// registered; it is now the same 409 as the check, with nothing left behind.
func TestAdopt_FileTakenAfterTheCheckIs409(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	thief := f.seedBook(t, "Provenance")
	target := f.seedBook(t, "Translation State")
	path := f.write(t, "Ann Leckie/Translation State.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	real := f.h.registerFile
	f.h.registerFile = func(ctx context.Context, bookID int64, format, p string) (bool, error) {
		if err := f.books.AddBookFile(ctx, thief.ID, format, p); err != nil {
			t.Errorf("concurrent claim: %v", err)
		}
		return real(ctx, bookID, format, p)
	}
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": target.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("adopt = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, target.ID); len(got) != 0 {
		t.Fatalf("target gained %v", got)
	}
	if got := filePaths(t, f.books, thief.ID); len(got) != 1 {
		t.Fatalf("concurrent owner's row = %v, want it kept", got)
	}
	if u, _ := f.units.Get(context.Background(), id); u.State != db.UnmatchedStatePending {
		t.Fatalf("unit state = %s, want pending", u.State)
	}
}

// TestAdopt_FileLeftByADeletedBookIsAdopted: a book_files row whose book no
// longer exists (foreign keys lost, #1727) belongs to nobody, and download and
// manual import take such a row over since #2937. Adoption refused it with a
// 409 that named no book, because its pre-check counted the dead row as an
// owner. It now adopts and the row is the target's.
func TestAdopt_FileLeftByADeletedBookIsAdopted(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	gone := f.seedBook(t, "Provenance (old)")
	target := f.seedBook(t, "Provenance")
	path := f.write(t, "Ann Leckie/Provenance.epub")
	if err := f.books.AddBookFile(context.Background(), gone.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	orphanBook(t, f.db, gone.ID)
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": target.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, target.ID); len(got) != 1 || got[0] != path {
		t.Fatalf("target files = %v, want the dead book's row taken over", got)
	}
}

// TestAdopt_FileOfALiveBookIsStill409 guards the other side: a row another
// existing book holds is still refused.
func TestAdopt_FileOfALiveBookIsStill409(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	owner := f.seedBook(t, "Provenance (other)")
	target := f.seedBook(t, "Provenance")
	path := f.write(t, "Ann Leckie/Provenance.epub")
	if err := f.books.AddBookFile(context.Background(), owner.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": target.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("adopt = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, owner.ID); len(got) != 1 {
		t.Fatalf("owner's row = %v, want it kept", got)
	}
}

// TestAdopt_RejectsSymlinkEscape is S10 at adopt time: the row may be hours
// old, so a member swapped for a symlink, or reached through a symlinked
// folder that leaves the library, is refused and nothing is written.
func TestAdopt_RejectsSymlinkEscape(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Ancillary Sword")
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.epub")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	swapped := f.write(t, "Ann Leckie/Swapped.epub")
	if err := os.Remove(swapped); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, swapped); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	escapeDir := filepath.Join(f.lib, "Escape")
	if err := os.Symlink(outside, escapeDir); err != nil {
		t.Fatal(err)
	}
	throughDir := filepath.Join(escapeDir, "secret.epub")

	for _, p := range []string{swapped, throughDir} {
		id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: p, MemberPaths: []string{p}})
		rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("adopt %s = %d %s, want 422", p, rec.Code, rec.Body.String())
		}
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("registered through a symlink: %v", got)
	}
}

// TestAdopt_DoubleAdoptAndDoubleUndo is T3: concurrent requests for one row,
// exactly one wins each time.
func TestAdopt_DoubleAdoptAndDoubleUndo(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Ancillary Mercy")
	path := f.write(t, "Ann Leckie/Ancillary Mercy.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})

	race := func(action string, body any) (ok, conflict int32) {
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/%s", id, action), body)
				switch rec.Code {
				case http.StatusOK:
					atomic.AddInt32(&ok, 1)
				case http.StatusConflict:
					atomic.AddInt32(&conflict, 1)
				default:
					t.Errorf("%s = %d %s", action, rec.Code, rec.Body.String())
				}
			}()
		}
		wg.Wait()
		return ok, conflict
	}
	if ok, conflict := race("adopt", map[string]any{"bookId": book.ID}); ok != 1 || conflict != 5 {
		t.Fatalf("adopt race: %d ok, %d conflict; want 1 and 5", ok, conflict)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 {
		t.Fatalf("files after adopt race = %v", got)
	}
	if ok, conflict := race("undo", nil); ok != 1 || conflict != 5 {
		t.Fatalf("undo race: %d ok, %d conflict; want 1 and 5", ok, conflict)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 0 {
		t.Fatalf("files after undo race = %v", got)
	}
}

// TestAdopt_BadRequests: no path field exists, and the body must name one
// target.
func TestAdopt_BadRequests(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	path := f.write(t, "X/y.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
	for name, body := range map[string]any{
		"neither":    map[string]any{},
		"both":       map[string]any{"bookId": 1, "foreignBookId": "OL1W"},
		"bad format": map[string]any{"bookId": 1, "format": "both"},
	} {
		if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := f.post(t, "/library/unmatched/999/adopt", map[string]any{"bookId": 1}); rec.Code != http.StatusConflict && rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}
}

// TestIgnore_SingleBulkAndUnignore covers the ignore routes and their state
// guards.
func TestIgnore_SingleBulkAndUnignore(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	a := f.write(t, "Folder A/one.epub")
	b := f.write(t, "Folder A/two.epub")
	c := f.write(t, "Folder B/three.epub")
	ida := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: a, MemberPaths: []string{a}, AuthorFolder: "Folder A"})
	f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: b, MemberPaths: []string{b}, AuthorFolder: "Folder A"})
	idc := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: c, MemberPaths: []string{c}, AuthorFolder: "Folder B"})

	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/ignore", idc), nil); rec.Code != http.StatusOK {
		t.Fatalf("ignore = %d", rec.Code)
	}
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/ignore", idc), nil); rec.Code != http.StatusConflict {
		t.Fatalf("second ignore = %d, want 409", rec.Code)
	}
	rec := f.post(t, "/library/unmatched/ignore", map[string]any{"authorFolder": "Folder A"})
	var bulk map[string]int64
	_ = json.Unmarshal(rec.Body.Bytes(), &bulk)
	if rec.Code != http.StatusOK || bulk["ignored"] != 2 {
		t.Fatalf("bulk ignore = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.post(t, "/library/unmatched/ignore", map[string]any{"ids": []int64{ida}, "authorFolder": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bulk with both selectors = %d, want 400", rec.Code)
	}
	if rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/unignore", ida), nil); rec.Code != http.StatusOK || decodeItem(t, rec).State != db.UnmatchedStatePending {
		t.Fatalf("unignore = %d %s", rec.Code, rec.Body.String())
	}
}

// countingUnitStore counts the book hydration queries a page costs.
type countingUnitStore struct {
	*db.UnmatchedUnitRepo
	bookRefs atomic.Int32
}

func (c *countingUnitStore) BookRefs(ctx context.Context, ids []int64) (map[int64]db.UnmatchedBookRef, error) {
	c.bookRefs.Add(1)
	return c.UnmatchedUnitRepo.BookRefs(ctx, ids)
}

// countingProvider counts every metadata provider call.
type countingProvider struct {
	metadata.Provider
	calls atomic.Int32
}

func (p *countingProvider) SearchAuthors(ctx context.Context, q string) ([]models.Author, error) {
	p.calls.Add(1)
	return p.Provider.SearchAuthors(ctx, q)
}
func (p *countingProvider) SearchBooks(ctx context.Context, q string) ([]models.Book, error) {
	p.calls.Add(1)
	return p.Provider.SearchBooks(ctx, q)
}
func (p *countingProvider) GetAuthor(ctx context.Context, id string) (*models.Author, error) {
	p.calls.Add(1)
	return p.Provider.GetAuthor(ctx, id)
}
func (p *countingProvider) GetBook(ctx context.Context, id string) (*models.Book, error) {
	p.calls.Add(1)
	return p.Provider.GetBook(ctx, id)
}
func (p *countingProvider) GetEditions(ctx context.Context, id string) ([]models.Edition, error) {
	p.calls.Add(1)
	return p.Provider.GetEditions(ctx, id)
}
func (p *countingProvider) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	p.calls.Add(1)
	return p.Provider.GetBookByISBN(ctx, isbn)
}

// TestAdoptionList_NoProviderCallsAndOneHydrationQuery is T5: a page with
// several rows, each with suggestions, costs one book query and no provider
// call, with and without facets.
func TestAdoptionList_NoProviderCallsAndOneHydrationQuery(t *testing.T) {
	provider := &countingProvider{Provider: &stubMetaProvider{name: "openlibrary"}}
	f := newAdoptionFixture(t, provider)
	counting := &countingUnitStore{UnmatchedUnitRepo: f.units}
	f.h.units = counting

	b1 := f.seedBook(t, "Ancillary Justice")
	b2 := f.seedBook(t, "Ancillary Sword")
	for i := range 5 {
		p := f.write(t, fmt.Sprintf("Ann Leckie/Book %d.epub", i))
		f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: p, MemberPaths: []string{p}, AuthorFolder: "Ann Leckie", ParsedTitle: fmt.Sprintf("Book %d", i),
			Candidates: []db.UnmatchedCandidate{{BookID: b1.ID, Score: 0.7}, {BookID: b2.ID, Score: 0.65}}})
	}

	for _, q := range []string{"", "?facets=1", "?facets=1&search=book&sort=title"} {
		counting.bookRefs.Store(0)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/unmatched"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list%s = %d %s", q, rec.Code, rec.Body.String())
		}
		var resp adoptionListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Total != 5 || len(resp.Items) != 5 || len(resp.Items[0].Candidates) != 2 || resp.Items[0].Candidates[0].Book.Title != "Ancillary Justice" {
			t.Fatalf("list%s = %+v", q, resp)
		}
		if n := counting.bookRefs.Load(); n != 1 {
			t.Errorf("list%s hydration queries = %d, want 1", q, n)
		}
		if (q != "") != (resp.Facets != nil) {
			t.Errorf("list%s facets present = %v", q, resp.Facets != nil)
		}
	}
	if n := provider.calls.Load(); n != 0 {
		t.Fatalf("provider calls during list = %d, want 0", n)
	}
}

// TestAdopt_IntoABookThatAlreadyHasItsFile is the outcome #2879 makes
// reachable from a suggestion: the scan now offers a book already Imported
// with a file, because an untracked copy of it is common. Adopting there adds
// the copy alongside the existing file, keeps the book Imported and still
// showing that file, says so in the response, and Undo removes only the copy.
func TestAdopt_IntoABookThatAlreadyHasItsFile(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	ctx := context.Background()
	book := f.seedBook(t, "The Matsumoto")
	existing := f.write(t, "Sarah K.L. Wilson/The Matsumoto (13110)/The Matsumoto - Sarah K.L. Wilson.epub")
	if err := f.books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, existing); err != nil {
		t.Fatal(err)
	}
	copyPath := f.write(t, "Sarah K. L. Wilson/The Matsumoto (13110)/The Matsumoto - Sarah K. L. Wilson.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: copyPath, MemberPaths: []string{copyPath}})

	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	it := decodeItem(t, rec)
	if it.State != db.UnmatchedStateAdopted || it.Message != alreadyHasFileMessage {
		t.Fatalf("adopted item = state %s message %q, want adopted with the already had a file message", it.State, it.Message)
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 2 || got[0] != existing || got[1] != copyPath {
		t.Fatalf("book files = %v, want the existing file then the adopted copy", got)
	}
	after, _ := f.books.GetByID(ctx, book.ID)
	if after.Status != models.BookStatusImported || after.EbookFilePath != existing {
		t.Fatalf("book after adopt = status %s showing %q, want imported still showing %s", after.Status, after.EbookFilePath, existing)
	}

	rec = f.post(t, fmt.Sprintf("/library/unmatched/%d/undo", id), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if got := filePaths(t, f.books, book.ID); len(got) != 1 || got[0] != existing {
		t.Fatalf("book files after undo = %v, want only the existing file", got)
	}
	if b, _ := f.books.GetByID(ctx, book.ID); b.Status != models.BookStatusImported {
		t.Fatalf("book after undo = status %s, want imported", b.Status)
	}
	for _, p := range []string{existing, copyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s touched on disk: %v", p, err)
		}
	}
}

// TestAdopt_IntoAWantedBookSaysNothingExtra: the message is only for a book
// that already had a file, so an ordinary adoption stays quiet.
func TestAdopt_IntoAWantedBookSaysNothingExtra(t *testing.T) {
	f := newAdoptionFixture(t, &stubMetaProvider{name: "openlibrary"})
	book := f.seedBook(t, "Ancillary Sword")
	path := f.write(t, "Ann Leckie/Ancillary Sword.epub")
	id := f.seedUnit(t, db.UnmatchedUnitScan{UnitPath: path, MemberPaths: []string{path}})
	rec := f.post(t, fmt.Sprintf("/library/unmatched/%d/adopt", id), map[string]any{"bookId": book.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	if it := decodeItem(t, rec); it.Message != "" {
		t.Fatalf("message = %q, want none", it.Message)
	}
}
