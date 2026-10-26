package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

type editionFixture struct {
	database *sql.DB
	h        *BookHandler
	books    *db.BookRepo
	editions *db.EditionRepo
	users    *db.UserRepo
	author   *models.Author
	ctx      context.Context
}

func newEditionFixture(t *testing.T) editionFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	editions := db.NewEditionRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-ED-H", Name: "Test Author", SortName: "Author, Test", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil).WithEditionHydration(editions)
	return editionFixture{database: database, h: h, books: books, editions: editions, users: db.NewUserRepo(database), author: author, ctx: ctx}
}

func (f editionFixture) seedBookWithEdition(t *testing.T, foreignID string, ownerUserID int64) (*models.Book, *models.Edition) {
	t.Helper()
	b := &models.Book{ForeignID: foreignID, AuthorID: f.author.ID, Title: "Book " + foreignID, SortTitle: "Book " + foreignID,
		Status: models.BookStatusWanted, MediaType: models.MediaTypeAudiobook, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: ownerUserID}
	if err := f.books.Create(f.ctx, b); err != nil {
		t.Fatal(err)
	}
	e := &models.Edition{ForeignID: "abs:edition:lib:" + foreignID + ":audiobook", BookID: b.ID, Title: "Recording", Format: "AUDIOBOOK", Monitored: true}
	if err := f.editions.Upsert(f.ctx, e); err != nil {
		t.Fatal(err)
	}
	return b, e
}

func putEdition(t *testing.T, h *BookHandler, ctx context.Context, bookID, editionID int64, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	bid, eid := strconv.FormatInt(bookID, 10), strconv.FormatInt(editionID, 10)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/book/"+bid+"/edition/"+eid, bytes.NewReader(raw)).WithContext(ctx)
	req = withURLParams(req, map[string]string{"id": bid, "editionId": eid})
	rec := httptest.NewRecorder()
	h.UpdateEdition(rec, req)
	return rec
}

// TestUpdateEdition_SetsAndLocksFields pins the success path: each field set
// is applied and locked, the date can be cleared, ISBNs are check-digit
// validated, the response is the stored row, and lockedFields replaces.
func TestUpdateEdition_SetsAndLocksFields(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED1", 0)
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)

	rec := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{
		"publisher":   "  Example Audio ",
		"publishDate": "2024-06-18",
		"isbn13":      "978-0-306-40615-7",
		"isbn10":      "0-306-40615-2",
		"asin":        "b0d2xyz123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Edition
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Publisher != "Example Audio" || got.ISBN13 == nil || *got.ISBN13 != "9780306406157" || got.ISBN10 == nil || *got.ISBN10 != "0306406152" || got.ASIN == nil || *got.ASIN != "B0D2XYZ123" {
		t.Fatalf("response not normalized/stored: %+v", got)
	}
	if got.PublishDate == nil || got.PublishDate.Format("2006-01-02") != "2024-06-18" {
		t.Fatalf("publishDate = %v", got.PublishDate)
	}
	for _, field := range models.LockableEditionFields {
		if !got.IsFieldLocked(field) {
			t.Fatalf("field %q should be locked: %v", field, got.LockedFields)
		}
	}

	// Clearing the date is a value, locked like any other.
	rec = putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"publishDate": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear date: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ := f.editions.GetByID(f.ctx, e.ID)
	if stored.PublishDate != nil || !stored.IsFieldLocked(models.EditionFieldPublishDate) {
		t.Fatalf("cleared date should be nil and locked: %+v", stored)
	}

	// Explicit lockedFields replaces the set; values stay.
	rec = putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"lockedFields": []string{"publisher"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace locks: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, _ = f.editions.GetByID(f.ctx, e.ID)
	if len(stored.LockedFields) != 1 || stored.LockedFields[0] != "publisher" || stored.Publisher != "Example Audio" {
		t.Fatalf("lock replace: %+v", stored)
	}
}

// An empty ISBN or ASIN clears the column and locks it empty, as an empty
// publishDate does, so the next import or hydration does not fill it again.
func TestUpdateEdition_EmptyValueClearsAndLocks(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED-CLEAR", 0)
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)

	rec := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"isbn13": "978-0-306-40615-7", "isbn10": "0-306-40615-2", "asin": "B0D2XYZ123"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"isbn13": "", "isbn10": "", "asin": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := f.editions.GetByID(f.ctx, e.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.ISBN13 != nil || stored.ISBN10 != nil || stored.ASIN != nil {
		t.Fatalf("empty values should clear: isbn13=%v isbn10=%v asin=%v", stored.ISBN13, stored.ISBN10, stored.ASIN)
	}
	for _, field := range []string{models.EditionFieldISBN13, models.EditionFieldISBN10, models.EditionFieldASIN} {
		if !stored.IsFieldLocked(field) {
			t.Fatalf("cleared %q should stay locked: %v", field, stored.LockedFields)
		}
	}
}

// A name repeated in lockedFields is stored once.
func TestUpdateEdition_LockedFieldsDropsRepeats(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED-REPEAT", 0)
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)

	rec := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"lockedFields": []string{"publisher", "asin", "publisher"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := f.editions.GetByID(f.ctx, e.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	if len(stored.LockedFields) != 2 || stored.LockedFields[0] != "publisher" || stored.LockedFields[1] != "asin" {
		t.Fatalf("lockedFields = %v, want [publisher asin]", stored.LockedFields)
	}
}

// TestUpdateEdition_Rejects pins the 400s and the two 404s, which must be the
// same message so the route is not a cross-user catalogue oracle.
func TestUpdateEdition_Rejects(t *testing.T) {
	// Ownership is only enforced under tenancy, which is off in tests unless
	// switched on, as the authorization tests do.
	auth.SetEnforceTenancyForTests(t, true)
	f := newEditionFixture(t)
	// owner_user_id is a foreign key, so the owner and the stranger must exist.
	owner, err := f.users.Create(f.ctx, "owner", "hash-owner")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := f.users.Create(f.ctx, "stranger", "hash-stranger")
	if err != nil {
		t.Fatal(err)
	}
	b, e := f.seedBookWithEdition(t, "ED2", owner.ID)
	_, otherEdition := f.seedBookWithEdition(t, "ED3", owner.ID)
	ownerCtx := withAuthCtx(f.ctx, owner.ID, auth.RoleUser)
	strangerCtx := withAuthCtx(f.ctx, stranger.ID, auth.RoleUser)

	for _, body := range []map[string]any{
		{"isbn13": "9780306406158"},
		{"isbn10": "0306406158"},
		{"asin": "nope"},
		{"publishDate": "18/06/2024"},
		{"lockedFields": []string{"title"}},
	} {
		rec := putEdition(t, f.h, ownerCtx, b.ID, e.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: expected 400, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}

	wrongBook := putEdition(t, f.h, ownerCtx, b.ID, otherEdition.ID, map[string]any{"publisher": "X"})
	foreign := putEdition(t, f.h, strangerCtx, b.ID, e.ID, map[string]any{"publisher": "X"})
	if wrongBook.Code != http.StatusNotFound || foreign.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for both, got %d and %d", wrongBook.Code, foreign.Code)
	}
	if wrongBook.Body.String() != foreign.Body.String() {
		t.Fatalf("the two 404s must be indistinguishable: %q vs %q", wrongBook.Body.String(), foreign.Body.String())
	}
	untouched, _ := f.editions.GetByID(f.ctx, otherEdition.ID)
	if untouched.Publisher != "" {
		t.Fatalf("another book's edition was written: %+v", untouched)
	}
}

// TestUpdateEdition_MissingEditionMatchesOtherNotFounds pins that an edition
// id that does not exist answers with the same 404 body as another book's
// edition, so the route does not reveal which edition ids exist.
func TestUpdateEdition_MissingEditionMatchesOtherNotFounds(t *testing.T) {
	f := newEditionFixture(t)
	b, _ := f.seedBookWithEdition(t, "ED4", 0)
	_, otherEdition := f.seedBookWithEdition(t, "ED5", 0)
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)

	missing := putEdition(t, f.h, adminCtx, b.ID, 999999, map[string]any{"publisher": "X"})
	wrongBook := putEdition(t, f.h, adminCtx, b.ID, otherEdition.ID, map[string]any{"publisher": "X"})
	if missing.Code != http.StatusNotFound || wrongBook.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for both, got %d and %d", missing.Code, wrongBook.Code)
	}
	if missing.Body.String() != wrongBook.Body.String() {
		t.Fatalf("the two 404s must be indistinguishable: %q vs %q", missing.Body.String(), wrongBook.Body.String())
	}
}

// TestBookGet_ReturnsEditionLocks pins that GET /book/{id} lists the book's
// editions with their lockedFields, which is how a client finds the
// editionId the edition PUT needs and reads the locks back.
func TestBookGet_ReturnsEditionLocks(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED6", 0)
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)
	if rec := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"publisher": "Example Audio"}); rec.Code != http.StatusOK {
		t.Fatalf("seed lock: %d %s", rec.Code, rec.Body.String())
	}

	id := strconv.FormatInt(b.ID, 10)
	req := withURLParams(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil).WithContext(adminCtx), map[string]string{"id": id})
	rec := httptest.NewRecorder()
	f.h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Editions) != 1 || got.Editions[0].ID != e.ID {
		t.Fatalf("editions = %+v, want the one seeded edition", got.Editions)
	}
	if got.Editions[0].Publisher != "Example Audio" || !got.Editions[0].IsFieldLocked(models.EditionFieldPublisher) {
		t.Fatalf("edition lock not returned: %+v", got.Editions[0])
	}
}

// TestEditionImagesGoThroughTheProxy pins that the editions a book response
// carries, and the edition PUT's answer, send each cover as a proxy URL like
// the book's own, and that proxying a response leaves the stored value alone.
func TestEditionImagesGoThroughTheProxy(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED7", 0)
	raw := "https://covers.example.com/edition.jpg"
	e.ImageURL = raw
	if err := f.editions.Upsert(f.ctx, e); err != nil {
		t.Fatal(err)
	}
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)
	want := ProxyImageURL(raw)
	if want == raw {
		t.Fatalf("test needs a URL the proxy rewrites; got %q unchanged", raw)
	}

	id := strconv.FormatInt(b.ID, 10)
	req := withURLParams(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil).WithContext(adminCtx), map[string]string{"id": id})
	rec := httptest.NewRecorder()
	f.h.Get(rec, req)
	var got models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Editions) != 1 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if got.Editions[0].ImageURL != want {
		t.Fatalf("book GET edition imageUrl = %q, want %q", got.Editions[0].ImageURL, want)
	}

	put := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"publisher": "Example Audio"})
	var edited models.Edition
	if err := json.Unmarshal(put.Body.Bytes(), &edited); err != nil {
		t.Fatalf("PUT: %d %s", put.Code, put.Body.String())
	}
	if edited.ImageURL != want {
		t.Fatalf("edition PUT imageUrl = %q, want %q", edited.ImageURL, want)
	}

	book := &models.Book{ImageURL: raw, Editions: []models.Edition{{ImageURL: raw}}}
	_ = proxiedBook(book)
	if book.ImageURL != raw || book.Editions[0].ImageURL != raw {
		t.Fatalf("proxiedBook changed the original: book=%q edition=%q", book.ImageURL, book.Editions[0].ImageURL)
	}
}

// TestUpdateEdition_StaleWriteAnswers409 pins the handler's answer when the
// repository refuses the write because the row changed since the request
// read it. A BEFORE UPDATE trigger that ignores the row stands in for the
// concurrent import or second editor the updated_at guard exists for: the
// UPDATE matches nothing, the handler re-reads, finds the edition still on
// this book, and answers 409 with reason "changed" instead of 404.
func TestUpdateEdition_StaleWriteAnswers409(t *testing.T) {
	f := newEditionFixture(t)
	b, e := f.seedBookWithEdition(t, "ED-STALE", 0)
	if _, err := f.database.Exec(`CREATE TRIGGER stale_edit BEFORE UPDATE ON editions BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	adminCtx := withAuthCtx(f.ctx, 1, auth.RoleAdmin)

	rec := putEdition(t, f.h, adminCtx, b.ID, e.ID, map[string]any{"publisher": "Example Audio"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != conflictReasonChanged || body["error"] == "" {
		t.Fatalf("409 body = %v, want reason %q and a message", body, conflictReasonChanged)
	}
	stored, err := f.editions.GetByID(f.ctx, e.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if stored.Publisher == "Example Audio" || stored.IsFieldLocked(models.EditionFieldPublisher) {
		t.Fatalf("refused write still landed: %+v", stored)
	}
}
