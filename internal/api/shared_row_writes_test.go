package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// With tenancy enforced, a row with no owner is shared: every user may read
// it, but only an admin may change it. A shared author can hold books from
// several users' libraries, so deleting, merging, editing or refreshing it as
// a user reached into other users' libraries. These tests pin the write rule
// on every route that changes an author, a book or a queue row, and pin that
// reads, tenancy off and admins are unchanged.

// sharedRowFixture: alice and bob are plain users, plus an admin. shared is an
// author with no owner (what Calibre, ABS, Readarr and Goodreads imports and
// pre-tenancy rows look like); aliceBook is alice's book filed under it, with
// a file on disk; unownedBook is a book with no owner under the same author
// (what an admin's Add Book stores).
type sharedRowFixture struct {
	*covAuEnv
	admin       int64
	shared      *models.Author
	aliceBook   *models.Book
	aliceFile   string
	unownedBook *models.Book
}

func newSharedRowFixture(t *testing.T) *sharedRowFixture {
	t.Helper()
	env := covAuSetup(t)
	admin, err := db.NewUserRepo(env.database).Create(env.ctx, "admin", "h3")
	if err != nil {
		t.Fatal(err)
	}
	shared := &models.Author{ForeignID: "OL-SHARED-A", Name: "Shared Writer", SortName: "Writer, Shared", Monitored: true, MetadataProvider: "openlibrary"}
	if err := env.authors.Create(env.ctx, shared); err != nil {
		t.Fatal(err)
	}
	aliceBook := &models.Book{ForeignID: "OL-ALICE-BOOK", AuthorID: shared.ID, Title: "Alice's Copy", SortTitle: "alice",
		Status: models.BookStatusImported, Genres: []string{}, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: env.alice}
	if err := env.books.Create(env.ctx, aliceBook); err != nil {
		t.Fatal(err)
	}
	aliceFile := mustWrite(t, filepath.Join(t.TempDir(), "Shared Writer", "Alice's Copy", "Alice's Copy.epub"))
	if err := env.books.AddBookFile(env.ctx, aliceBook.ID, models.MediaTypeEbook, aliceFile); err != nil {
		t.Fatal(err)
	}
	unowned := &models.Book{ForeignID: "OL-ADMIN-BOOK", AuthorID: shared.ID, Title: "Admin's Copy", SortTitle: "admin",
		Status: models.BookStatusWanted, Genres: []string{}, MediaType: models.MediaTypeEbook,
		MetadataProvider: "openlibrary", Monitored: true}
	if err := env.books.Create(env.ctx, unowned); err != nil {
		t.Fatal(err)
	}
	shared, _ = env.authors.GetByID(env.ctx, shared.ID)
	aliceBook, _ = env.books.GetByID(env.ctx, aliceBook.ID)
	unowned, _ = env.books.GetByID(env.ctx, unowned.ID)
	if shared.OwnerUserID != 0 || aliceBook.OwnerUserID != env.alice || unowned.OwnerUserID != 0 {
		t.Fatalf("fixture owners: shared=%d aliceBook=%d unowned=%d", shared.OwnerUserID, aliceBook.OwnerUserID, unowned.OwnerUserID)
	}
	return &sharedRowFixture{covAuEnv: env, admin: admin.ID, shared: shared, aliceBook: aliceBook, aliceFile: aliceFile, unownedBook: unowned}
}

func idParam(id int64) map[string]string { return map[string]string{"id": strconv.FormatInt(id, 10)} }

func (f *sharedRowFixture) bookHandler() *BookHandler { return NewBookHandler(f.books, nil, nil, nil) }

func (f *sharedRowFixture) assertAliceBookIntact(t *testing.T) {
	t.Helper()
	got, err := f.books.GetByID(f.ctx, f.aliceBook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("alice's book %d was deleted", f.aliceBook.ID)
	}
	if got.AuthorID != f.shared.ID || got.Monitored != f.aliceBook.Monitored {
		t.Fatalf("alice's book changed: author %d -> %d, monitored %v -> %v", f.shared.ID, got.AuthorID, f.aliceBook.Monitored, got.Monitored)
	}
	if _, err := os.Stat(f.aliceFile); err != nil {
		t.Fatalf("alice's file: %v", err)
	}
}

func TestAuthorDelete_UserCannotDeleteSharedAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/?deleteFiles=true", "", idParam(f.shared.ID), f.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob deleting the shared author: %d %s, want 404", rec.Code, rec.Body.String())
	}
	f.assertAliceBookIntact(t)
	if a, _ := f.authors.GetByID(f.ctx, f.shared.ID); a == nil {
		t.Fatal("shared author was deleted")
	}
}

func TestBookDelete_UserCannotDeleteUnownedBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	f.bookHandler().Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.unownedBook.ID), f.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob deleting an unowned book: %d %s, want 404", rec.Code, rec.Body.String())
	}
	if got, _ := f.books.GetByID(f.ctx, f.unownedBook.ID); got == nil {
		t.Fatal("unowned book was deleted")
	}
}

// Every other book write route refuses an unowned book the same way.
func TestBookWrites_UserRefusedOnUnownedBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	h := f.bookHandler()
	for name, call := range map[string]http.HandlerFunc{
		"update":      h.Update,
		"delete file": h.DeleteFile,
		"exclude":     h.ToggleExcluded,
		"rebind":      h.Rebind,
		"enrich":      h.EnrichAudiobook,
		"cover":       h.UploadCover,
	} {
		rec := httptest.NewRecorder()
		call(rec, covAuRequest(http.MethodPut, "/", `{"monitored":false}`, idParam(f.unownedBook.ID), f.bob, "user"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s on an unowned book as a user: %d %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	got, _ := f.books.GetByID(f.ctx, f.unownedBook.ID)
	if got == nil || !got.Monitored || got.Excluded {
		t.Fatalf("unowned book changed: %+v", got)
	}
}

func TestAuthorMerge_UserCannotMergeSharedAuthorIntoOwn(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	body := `{"sourceId":` + strconv.FormatInt(f.shared.ID, 10) + `}`
	NewAuthorAliasHandler(f.authors, f.aliases).Merge(rec, covAuRequest(http.MethodPost, "/", body, idParam(f.bobA.ID), f.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob merging the shared author into his own: %d %s, want 404", rec.Code, rec.Body.String())
	}
	f.assertAliceBookIntact(t)

	// Nor the other way round: his author into the shared one.
	rec = httptest.NewRecorder()
	body = `{"sourceId":` + strconv.FormatInt(f.bobA.ID, 10) + `}`
	NewAuthorAliasHandler(f.authors, f.aliases).Merge(rec, covAuRequest(http.MethodPost, "/", body, idParam(f.shared.ID), f.bob, "user"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob merging into the shared author: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestAuthorUpdate_UserCannotChangeSharedAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	rf, err := db.NewRootFolderRepo(f.database).Create(f.ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	h := covAuHandler(f.covAuEnv, nil)
	for _, body := range []string{
		`{"monitored":false,"applyMonitorModeToExisting":true}`,
		`{"rootFolderId":` + strconv.FormatInt(rf.ID, 10) + `,"audiobookRootFolderId":` + strconv.FormatInt(rf.ID, 10) + `}`,
	} {
		rec := httptest.NewRecorder()
		h.Update(rec, covAuRequest(http.MethodPut, "/", body, idParam(f.shared.ID), f.bob, "user"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("bob updating the shared author with %s: %d %s, want 404", body, rec.Code, rec.Body.String())
		}
	}
	f.assertAliceBookIntact(t)
	after, _ := f.authors.GetByID(f.ctx, f.shared.ID)
	if !after.Monitored || after.RootFolderID != nil || after.AudiobookRootFolderID != nil {
		t.Fatalf("shared author changed: monitored=%v root=%v audiobookRoot=%v", after.Monitored, after.RootFolderID, after.AudiobookRootFolderID)
	}
}

// The remaining author write routes answer the same 404 for a shared author.
func TestAuthorWrites_UserRefusedOnSharedAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	// A stub provider and a real profiles repo, so a refresh the guard failed
	// to stop shows up as a 202 rather than a panic in the background sync.
	h := NewAuthorHandler(f.authors, f.aliases, f.books, f.series, metadata.NewAggregator(&stubMetaProvider{}), nil,
		db.NewMetadataProfileRepo(f.database), nil)
	aliases := NewAuthorAliasHandler(f.authors, f.aliases)
	alias := &models.AuthorAlias{AuthorID: f.shared.ID, Name: "S. Writer"}
	if err := f.aliases.Create(f.ctx, alias); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]http.HandlerFunc{
		"refresh":         h.Refresh,
		"genres":          h.ApplyGenres,
		"relink":          h.RelinkUpstream,
		"photo upload":    h.UploadPhoto,
		"photo clear":     h.ClearPhoto,
		"catalogue prune": h.ApplyCatalogueReconciliation,
		"alias delete":    aliases.Delete,
	} {
		rec := httptest.NewRecorder()
		params := idParam(f.shared.ID)
		params["aliasID"] = strconv.FormatInt(alias.ID, 10)
		call(rec, covAuRequest(http.MethodPost, "/", `{"genres":["x"],"bookIds":[1]}`, params, f.bob, "user"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s on the shared author as a user: %d %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	f.assertAliceBookIntact(t)
	if got, _ := f.aliases.ListByAuthor(f.ctx, f.shared.ID); len(got) != 1 {
		t.Fatalf("shared author's aliases = %+v, want the one alias kept", got)
	}
}

func TestAuthorsBulk_UserRefusedOnSharedAuthor(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	h := NewBulkHandler(f.authors, f.books, nil, nil).WithRefreshFunc(func(a *models.Author) {
		t.Errorf("bulk refresh ran for shared author %d", a.ID)
	})
	for _, action := range []string{"delete", "monitor", "unmonitor", "refresh", "set_media_type", "set_monitor_mode"} {
		body := `{"ids":[` + strconv.FormatInt(f.shared.ID, 10) + `],"action":"` + action + `","mediaType":"audiobook","monitorMode":"none","applyMonitorModeToExisting":true}`
		rec := httptest.NewRecorder()
		h.AuthorsBulk(rec, covAuRequest(http.MethodPost, "/", body, nil, f.bob, "user"))
		var resp bulkResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		res := resp.Results[strconv.FormatInt(f.shared.ID, 10)]
		if res.OK || res.Error != errBulkAuthorNotOwned.Error() {
			t.Errorf("bulk %s on the shared author as a user: %d %s, want %q", action, rec.Code, rec.Body.String(), errBulkAuthorNotOwned.Error())
		}
	}
	f.assertAliceBookIntact(t)
	if got, _ := f.books.GetByID(f.ctx, f.aliceBook.ID); got.MediaType != models.MediaTypeEbook {
		t.Fatalf("alice's book media type changed to %q", got.MediaType)
	}
}

func TestBooksBulk_UserRefusedOnUnownedBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	h := NewBulkHandler(f.authors, f.books, nil, nil)
	for _, action := range []string{"delete", "monitor", "unmonitor", "exclude", "set_media_type"} {
		body := `{"ids":[` + strconv.FormatInt(f.unownedBook.ID, 10) + `],"action":"` + action + `","mediaType":"audiobook"}`
		rec := httptest.NewRecorder()
		h.BooksBulk(rec, covAuRequest(http.MethodPost, "/", body, nil, f.bob, "user"))
		var resp bulkResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		res := resp.Results[strconv.FormatInt(f.unownedBook.ID, 10)]
		if res.OK || res.Error != errBulkBookNotOwned.Error() {
			t.Errorf("bulk %s on an unowned book as a user: %d %s, want %q", action, rec.Code, rec.Body.String(), errBulkBookNotOwned.Error())
		}
	}
	// The conditional exclude takes the same check.
	rec := httptest.NewRecorder()
	h.BooksBulk(rec, covAuRequest(http.MethodPost, "/", `{"ids":[`+strconv.FormatInt(f.unownedBook.ID, 10)+`],"action":"exclude","expectNoFiles":true}`, nil, f.bob, "user"))
	if !strings.Contains(rec.Body.String(), errBulkBookNotOwned.Error()) {
		t.Errorf("conditional exclude of an unowned book as a user: %s", rec.Body.String())
	}
	for _, action := range []string{"unmonitor", "blocklist"} {
		body := `{"ids":[` + strconv.FormatInt(f.unownedBook.ID, 10) + `],"action":"` + action + `"}`
		rec := httptest.NewRecorder()
		h.WantedBulk(rec, covAuRequest(http.MethodPost, "/", body, nil, f.bob, "user"))
		var resp bulkResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if res := resp.Results[strconv.FormatInt(f.unownedBook.ID, 10)]; res.OK {
			t.Errorf("wanted bulk %s on an unowned book as a user succeeded", action)
		}
	}
	got, _ := f.books.GetByID(f.ctx, f.unownedBook.ID)
	if got == nil || !got.Monitored || got.Excluded || got.Status != models.BookStatusWanted || got.MediaType != models.MediaTypeEbook {
		t.Fatalf("unowned book changed: %+v", got)
	}
}

// Reads of shared rows by a user are unchanged: the shared author, the
// unowned book and alice's own book under the shared author all open.
func TestSharedRows_StillReadableByUser(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Get(rec, covAuRequest(http.MethodGet, "/", "", idParam(f.shared.ID), f.bob, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("bob reading the shared author: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.bookHandler().Get(rec, covAuRequest(http.MethodGet, "/", "", idParam(f.unownedBook.ID), f.bob, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("bob reading an unowned book: %d %s", rec.Code, rec.Body.String())
	}
	// Alice still manages her own book under the shared author.
	rec = httptest.NewRecorder()
	f.bookHandler().Update(rec, covAuRequest(http.MethodPut, "/", `{"monitored":false}`, idParam(f.aliceBook.ID), f.alice, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("alice updating her own book under the shared author: %d %s", rec.Code, rec.Body.String())
	}
}

// With tenancy off every user manages the one shared library, as before.
func TestSharedRows_TenancyOffUserCanStillWrite(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, false)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	f.bookHandler().Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.unownedBook.ID), f.bob, "user"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tenancy off, bob deleting an unowned book: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Update(rec, covAuRequest(http.MethodPut, "/", `{"monitored":false}`, idParam(f.shared.ID), f.bob, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("tenancy off, bob updating the shared author: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	body := `{"sourceId":` + strconv.FormatInt(f.shared.ID, 10) + `}`
	NewAuthorAliasHandler(f.authors, f.aliases).Merge(rec, covAuRequest(http.MethodPost, "/", body, idParam(f.bobA.ID), f.bob, "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("tenancy off, bob merging the shared author: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := f.books.GetByID(f.ctx, f.aliceBook.ID); got == nil || got.AuthorID != f.bobA.ID {
		t.Fatalf("tenancy off merge did not reparent: %+v", got)
	}
	rec = httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.bobA.ID), f.alice, "user"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tenancy off, alice deleting bob's author: %d %s", rec.Code, rec.Body.String())
	}
}

// An admin with tenancy on keeps managing shared rows exactly as before.
func TestSharedRows_AdminCanStillWrite(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)

	rec := httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Update(rec, covAuRequest(http.MethodPut, "/", `{"monitored":false}`, idParam(f.shared.ID), f.admin, "admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin updating the shared author: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.bookHandler().Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.unownedBook.ID), f.admin, "admin"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin deleting an unowned book: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	body := `{"sourceId":` + strconv.FormatInt(f.shared.ID, 10) + `}`
	NewAuthorAliasHandler(f.authors, f.aliases).Merge(rec, covAuRequest(http.MethodPost, "/", body, idParam(f.bobA.ID), f.admin, "admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin merging the shared author: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.bobA.ID), f.admin, "admin"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin deleting an author holding several users' books: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := f.books.GetByID(f.ctx, f.aliceBook.ID); got != nil {
		t.Fatalf("admin's author delete kept the cascade off: %+v", got)
	}
}

// A user's own author can still end up holding someone else's book (an admin
// merge, an import). Deleting or merging it would take that book along, so a
// user is refused with 409 while every book under it is not theirs; an
// author whose books are all theirs deletes as before.
func TestAuthorDeleteAndMerge_UserRefusedWhenOwnAuthorHoldsOthersBooks(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := newSharedRowFixture(t)
	if _, err := f.database.Exec(`UPDATE books SET author_id = ? WHERE id = ?`, f.bobA.ID, f.aliceBook.ID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.bobA.ID), f.bob, "user"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errAuthorHoldsOthersBooks.Error()) {
		t.Fatalf("bob deleting his author that holds alice's book: %d %s, want 409", rec.Code, rec.Body.String())
	}
	bobOther := &models.Author{ForeignID: "OL-BOB-B", Name: "Bob Other", SortName: "Other, Bob"}
	if err := f.authors.CreateForUser(f.ctx, bobOther, f.bob); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	body := `{"sourceId":` + strconv.FormatInt(f.bobA.ID, 10) + `}`
	NewAuthorAliasHandler(f.authors, f.aliases).Merge(rec, covAuRequest(http.MethodPost, "/", body, idParam(bobOther.ID), f.bob, "user"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("bob merging his author that holds alice's book: %d %s, want 409", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	NewBulkHandler(f.authors, f.books, nil, nil).AuthorsBulk(rec, covAuRequest(http.MethodPost, "/",
		`{"ids":[`+strconv.FormatInt(f.bobA.ID, 10)+`],"action":"delete"}`, nil, f.bob, "user"))
	if !strings.Contains(rec.Body.String(), errAuthorHoldsOthersBooks.Error()) {
		t.Fatalf("bulk delete of bob's author that holds alice's book: %s", rec.Body.String())
	}
	if got, _ := f.books.GetByID(f.ctx, f.aliceBook.ID); got == nil || got.AuthorID != f.bobA.ID {
		t.Fatalf("alice's book moved or vanished: %+v", got)
	}

	// A shared book with no owner under his author counts the same way.
	if _, err := f.database.Exec(`UPDATE books SET author_id = ? WHERE id = ?`, f.shared.ID, f.aliceBook.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE books SET author_id = ? WHERE id = ?`, f.bobA.ID, f.unownedBook.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(f.bobA.ID), f.bob, "user"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("bob deleting his author that holds an unowned book: %d %s, want 409", rec.Code, rec.Body.String())
	}

	// Control: bob's other author, holding only his own book, deletes.
	own := &models.Book{ForeignID: "OL-BOB-OWN", AuthorID: bobOther.ID, Title: "Bob's Own", SortTitle: "own",
		Status: models.BookStatusWanted, Genres: []string{}, MediaType: models.MediaTypeEbook, OwnerUserID: f.bob}
	if err := f.books.Create(f.ctx, own); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	covAuHandler(f.covAuEnv, nil).Delete(rec, covAuRequest(http.MethodDelete, "/", "", idParam(bobOther.ID), f.bob, "user"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bob deleting his own author with his own book: %d %s", rec.Code, rec.Body.String())
	}
}

// Adding a book under a shared author writes the book, not the author, so a
// user still can, and the new book is theirs.
func TestAddBook_UserCanStillAddUnderSharedAuthor(t *testing.T) {
	f := newTenancyFixture(t, true, duneAudiobookProvider())
	ctx := context.Background()
	shared := &models.Author{ForeignID: "OL-SHARED-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	res, err := f.h.addBookCore(auth.WithUserRole(f.userCtx(f.bob), "user"), duneAddParams)
	if err != nil {
		t.Fatalf("bob's add under the shared author: %v", err)
	}
	if res.Book == nil {
		t.Fatal("no book created")
	}
	got, _ := f.books.GetByID(ctx, res.Book.ID)
	if got == nil || got.AuthorID != shared.ID || got.OwnerUserID != f.bob {
		t.Fatalf("added book = %+v, want under shared author %d owned by bob %d", got, shared.ID, f.bob)
	}
	if a, _ := f.authors.GetByID(ctx, shared.ID); a == nil || a.OwnerUserID != 0 {
		t.Fatalf("shared author changed owner: %+v", a)
	}
}

// Downloads with no owner (the scheduler's automatic grabs for a book with no
// owner) are not in a user's queue list, and a user cannot act on one by id
// either. Admins still can.
func TestQueueWrites_UserRefusedOnOwnerlessDownload(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	h, database, downloads, _, _, ctx := queueFixture(t)
	bob, err := db.NewUserRepo(database).Create(ctx, "bob", "h")
	if err != nil {
		t.Fatal(err)
	}
	dl := &models.Download{GUID: "guid-auto", Title: "Auto grab", Status: models.StateImportFailed, Protocol: "usenet"}
	if err := downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if owner, ok, _ := downloads.GetOwnerByID(ctx, dl.ID); !ok || owner != 0 {
		t.Fatalf("fixture download owner = %d (exists %v), want none", owner, ok)
	}
	id := strconv.FormatInt(dl.ID, 10)

	for name, call := range map[string]http.HandlerFunc{
		"delete":       h.Delete,
		"retry import": h.RetryImport,
		"retry":        h.RetryDownload,
	} {
		rec := httptest.NewRecorder()
		call(rec, withURLParam(reqAsUser(http.MethodPost, "/", bob.ID), "id", id))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s on an ownerless download as a user: %d %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	for name, call := range map[string]http.HandlerFunc{
		"bulk delete": h.BulkDelete,
		"bulk retry":  h.BulkRetry,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ids":[`+id+`]}`))
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), bob.ID), "user"))
		call(rec, req)
		if !strings.Contains(rec.Body.String(), "download not found") {
			t.Errorf("%s on an ownerless download as a user: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got, _ := downloads.GetByID(ctx, dl.ID); got == nil || got.Status != models.StateImportFailed {
		t.Fatalf("ownerless download changed: %+v", got)
	}

	// An admin still removes it.
	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/", nil), "id", id)
	req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), bob.ID+100), "admin"))
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("admin deleting an ownerless download: %d %s", rec.Code, rec.Body.String())
	}
}

// A pending release for a book with no owner is not in a user's pending list,
// and a user cannot dismiss or force grab it by id either.
func TestPendingWrites_UserRefusedOnUnownedBooksRelease(t *testing.T) {
	t.Setenv("BINDERY_ENFORCE_TENANCY", "true")
	f := newSharedRowFixture(t)
	pending := db.NewPendingReleaseRepo(f.database)
	pr := &models.PendingRelease{BookID: f.unownedBook.ID, MediaType: models.MediaTypeEbook, Title: "shared rel", GUID: "guid-shared", Protocol: "usenet", Reason: "delay", ReleaseJSON: `{"guid":"guid-shared","title":"shared rel"}`}
	if err := pending.Upsert(f.ctx, pr); err != nil {
		t.Fatal(err)
	}
	all, err := pending.List(f.ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("pending rows = %+v err=%v", all, err)
	}
	id := strconv.FormatInt(all[0].ID, 10)
	downloads := db.NewDownloadRepo(f.database)
	queue := NewQueueHandler(downloads, db.NewDownloadClientRepo(f.database), f.books, db.NewHistoryRepo(f.database))
	h := NewPendingHandler(pending, queue, downloads, f.books)

	for name, call := range map[string]http.HandlerFunc{"dismiss": h.Delete, "grab": h.Grab} {
		rec := httptest.NewRecorder()
		call(rec, covAuRequest(http.MethodPost, "/", "", map[string]string{"id": id}, f.bob, "user"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s of a pending release for an unowned book as a user: %d %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	if rows, _ := pending.List(f.ctx); len(rows) != 1 {
		t.Fatalf("pending release removed: %+v", rows)
	}
	if rows, _ := downloads.List(f.ctx); len(rows) != 0 {
		t.Fatalf("a download was created: %+v", rows)
	}

	rec := httptest.NewRecorder()
	h.Delete(rec, covAuRequest(http.MethodDelete, "/", "", map[string]string{"id": id}, f.admin, "admin"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin dismissing it: %d %s", rec.Code, rec.Body.String())
	}
}

// POST /author whose name resolves to a shared author (an ABS or Calibre
// import with no provider link) relinks that row in place. A user who is not
// an admin gets the confirm merge conflict naming it instead; an admin still
// relinks.
func TestCreateAuthor_UserDoesNotRelinkSharedAuthor(t *testing.T) {
	for _, tc := range []struct {
		role       string
		wantStatus int
		relinked   bool
	}{
		{"user", http.StatusConflict, false},
		{"admin", http.StatusOK, true},
	} {
		t.Run(tc.role, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			env := covAuSetup(t)
			shared := &models.Author{ForeignID: "abs:author:lib:tolkien", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R.",
				MetadataProvider: "audiobookshelf", Monitored: true, MonitorMode: "all"}
			if err := env.authors.Create(env.ctx, shared); err != nil {
				t.Fatal(err)
			}
			aliceBook := &models.Book{ForeignID: "abs:book:1", AuthorID: shared.ID, Title: "The Hobbit", SortTitle: "hobbit",
				Status: models.BookStatusImported, Genres: []string{}, MediaType: models.MediaTypeAudiobook, OwnerUserID: env.alice}
			if err := env.books.Create(env.ctx, aliceBook); err != nil {
				t.Fatal(err)
			}
			provider := &fixedAuthorProvider{result: &models.Author{ForeignID: "OL26320A", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R.", MetadataProvider: "openlibrary"}}
			h := NewAuthorHandler(env.authors, env.aliases, env.books, nil, metadata.NewAggregator(provider), nil, db.NewMetadataProfileRepo(env.database), nil)

			body := `{"foreignAuthorId":"OL26320A","authorName":"J.R.R. Tolkien","monitored":false,"monitorMode":"none","skipCatalogueSync":true}`
			rec := httptest.NewRecorder()
			h.Create(rec, covAuRequest(http.MethodPost, "/", body, nil, env.bob, tc.role))
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s POST /author matching a shared author: %d %s, want %d", tc.role, rec.Code, rec.Body.String(), tc.wantStatus)
			}
			got, _ := env.authors.GetByID(env.ctx, shared.ID)
			changed := got.ForeignID != shared.ForeignID || got.Monitored != shared.Monitored || got.MonitorMode != shared.MonitorMode
			if changed != tc.relinked {
				t.Fatalf("%s: shared author relinked=%v, want %v (foreignId=%q monitored=%v mode=%q)", tc.role, changed, tc.relinked, got.ForeignID, got.Monitored, got.MonitorMode)
			}
			if !tc.relinked && !strings.Contains(rec.Body.String(), `"id":`+strconv.FormatInt(shared.ID, 10)) {
				t.Errorf("the conflict should name the matched author %d: %s", shared.ID, rec.Body.String())
			}
		})
	}
}

// Add Book whose author resolves to a shared placeholder DNB author upgrades
// that row to the provider id for any caller, so every user keeps adding
// under the one author: a second author under the same provider id would be
// refused, since authors.foreign_id is unique across users. The upgrade only
// replaces identity and descriptive fields, so the shared row's owner,
// monitoring, profiles and root folders stay as they were.
func TestAddBook_UsersShareUpgradedPlaceholderDNBAuthor(t *testing.T) {
	provider := duneAudiobookProvider()
	provider.getBookByID["OL-MESSIAH-W"] = &models.Book{ForeignID: "OL-MESSIAH-W", Title: "Dune Messiah", SortTitle: "Dune Messiah", Language: "eng",
		MediaType: models.MediaTypeEbook, Genres: []string{}, MetadataProvider: "openlibrary"}
	provider.author = &models.Author{ForeignID: "OL-HERBERT-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	f := newTenancyFixture(t, true, provider)
	ctx := context.Background()
	rf, err := db.NewRootFolderRepo(f.db).Create(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	qp, mp := int64(1), int64(models.DefaultMetadataProfileID)
	synthetic := &models.Author{ForeignID: "dnb:author:frank-herbert", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "dnb",
		Monitored: true, MonitorMode: models.AuthorMonitorModeLatest, MonitorLatestCount: 3,
		QualityProfileID: &qp, MetadataProfileID: &mp, RootFolderID: &rf.ID, AudiobookRootFolderID: &rf.ID}
	if err := f.authors.Create(ctx, synthetic); err != nil {
		t.Fatal(err)
	}
	before, _ := f.authors.GetByID(ctx, synthetic.ID)

	params := duneAddParams
	params.ForeignAuthorID = "OL-HERBERT-A"
	bobRes, err := f.h.addBookCore(auth.WithUserRole(f.userCtx(f.bob), "user"), params)
	if err != nil {
		t.Fatalf("bob's add: %v", err)
	}
	after, _ := f.authors.GetByID(ctx, synthetic.ID)
	if after == nil || after.ForeignID != "OL-HERBERT-A" {
		t.Fatalf("placeholder not upgraded to the provider id: %+v", after)
	}
	if after.OwnerUserID != 0 || after.Monitored != before.Monitored || after.MonitorMode != before.MonitorMode ||
		after.MonitorLatestCount != before.MonitorLatestCount ||
		!sameID(after.QualityProfileID, before.QualityProfileID) || !sameID(after.MetadataProfileID, before.MetadataProfileID) ||
		!sameID(after.RootFolderID, before.RootFolderID) || !sameID(after.AudiobookRootFolderID, before.AudiobookRootFolderID) {
		t.Fatalf("upgrade changed more than identity:\n before %+v\n after  %+v", before, after)
	}

	aliceRes, err := f.h.addBookCore(auth.WithUserRole(f.userCtx(f.alice), "user"),
		addBookParams{ForeignBookID: "OL-MESSIAH-W", ForeignAuthorID: "OL-HERBERT-A", AuthorName: "Frank Herbert", SkipCatalogueSync: true})
	if err != nil {
		t.Fatalf("alice's add under the same provider author: %v", err)
	}
	for who, res := range map[int64]addBookResult{f.bob: bobRes, f.alice: aliceRes} {
		if res.Book == nil || res.Book.AuthorID != synthetic.ID || res.Book.OwnerUserID != who {
			t.Errorf("user %d's book = %+v, want under author %d owned by them", who, res.Book, synthetic.ID)
		}
	}
}

func sameID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Add Book whose title matches a shared book with no owner under the author
// (a Calibre stub, an admin's add) adopts that row: rewriting its foreign id,
// widening its format and monitoring it. A user who is not an admin gets a
// row of their own instead and the shared row is untouched.
func TestAddBook_UserDoesNotAdoptSharedTitleMatch(t *testing.T) {
	f := newTenancyFixture(t, true, duneAudiobookProvider())
	ctx := context.Background()
	shared := &models.Author{ForeignID: "OL-SHARED-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	stub := &models.Book{ForeignID: "calibre:42", Title: "Dune", SortTitle: "Dune", AuthorID: shared.ID,
		Status: models.BookStatusImported, Monitored: false, Genres: []string{},
		MediaType: models.MediaTypeEbook, MetadataProvider: "calibre"}
	if err := f.books.Create(ctx, stub); err != nil {
		t.Fatal(err)
	}
	before, _ := f.books.GetByID(ctx, stub.ID)
	res, err := f.h.addBookCore(auth.WithUserRole(f.userCtx(f.bob), "user"), duneAddParams)
	if err != nil {
		t.Fatalf("bob's add: %v", err)
	}
	assertBookUnchanged(t, f.books, before)
	if res.Book == nil || res.Book.ID == stub.ID || res.Book.OwnerUserID != f.bob {
		t.Fatalf("bob's add should create his own row, got %+v", res.Book)
	}
}

// A shared row with the requested id that appears while the add is in flight
// (another account's add or a sync) is answered as already in the library,
// as step 0 answers one that was there before, instead of being monitored.
func TestAddBook_UserDoesNotMonitorSharedRowThatAppearsMidRequest(t *testing.T) {
	provider := duneAudiobookProvider()
	provider.getBookEntered = make(chan struct{}, 1)
	provider.getBookGate = make(chan struct{})
	f := newTenancyFixture(t, true, provider)
	ctx := context.Background()
	shared := &models.Author{ForeignID: "OL-SHARED-A", Name: "Frank Herbert", SortName: "Herbert, Frank", MetadataProvider: "openlibrary"}
	if err := f.authors.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		res addBookResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := f.h.addBookCore(auth.WithUserRole(f.userCtx(f.bob), "user"), duneAddParams)
		done <- outcome{res, err}
	}()
	<-provider.getBookEntered
	appeared := &models.Book{ForeignID: "OL-DUNE-W", Title: "Dune", SortTitle: "Dune", AuthorID: shared.ID,
		Status: models.BookStatusWanted, Monitored: false, Genres: []string{},
		MediaType: models.MediaTypeEbook, MetadataProvider: "openlibrary"}
	if err := f.books.Create(ctx, appeared); err != nil {
		t.Fatal(err)
	}
	before, _ := f.books.GetByID(ctx, appeared.ID)
	close(provider.getBookGate)
	out := <-done

	var inLibrary *bookInLibraryError
	if !errors.As(out.err, &inLibrary) || inLibrary.Book == nil || inLibrary.Book.ID != appeared.ID {
		t.Fatalf("bob's add over a shared row that appeared mid request: res=%+v err=%v, want already in library", out.res.Book, out.err)
	}
	assertBookUnchanged(t, f.books, before)
}

// Removing one's own download with unmonitorBooks leaves a shared book alone
// for a user who is not an admin; an admin still unmonitors it.
func TestQueueBulkDelete_UnmonitorSkipsSharedBookForUser(t *testing.T) {
	for _, tc := range []struct {
		role          string
		wantMonitored bool
	}{
		{"user", true},
		{"admin", false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			auth.SetEnforceTenancyForTests(t, true)
			f := newSharedRowFixture(t)
			downloads := db.NewDownloadRepo(f.database)
			bid := f.unownedBook.ID
			dl := &models.Download{GUID: "guid-bob", Title: "bob grab", Status: models.StateImportFailed, Protocol: "usenet", BookID: &bid, OwnerUserID: f.bob}
			if err := downloads.Create(f.ctx, dl); err != nil {
				t.Fatal(err)
			}
			q := NewQueueHandler(downloads, db.NewDownloadClientRepo(f.database), f.books, db.NewHistoryRepo(f.database))
			rec := httptest.NewRecorder()
			body := `{"ids":[` + strconv.FormatInt(dl.ID, 10) + `],"unmonitorBooks":true,"removeFromClient":false}`
			q.BulkDelete(rec, covAuRequest(http.MethodPost, "/", body, nil, f.bob, tc.role))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
				t.Fatalf("%s removing bob's own download: %d %s", tc.role, rec.Code, rec.Body.String())
			}
			if got, _ := downloads.GetByID(f.ctx, dl.ID); got != nil {
				t.Fatalf("download not removed: %+v", got)
			}
			got, _ := f.books.GetByID(f.ctx, bid)
			if got.Monitored != tc.wantMonitored {
				t.Fatalf("%s: shared book monitored=%v, want %v", tc.role, got.Monitored, tc.wantMonitored)
			}
		})
	}
}
