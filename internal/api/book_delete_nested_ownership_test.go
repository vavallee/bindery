package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

func deleteBookWithFiles(t *testing.T, h *BookHandler, id int64) {
	t.Helper()
	req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(id, 10)+"?deleteFiles=true", nil), "id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", rec.Code)
	}
}

// TestBookDelete_KeepsFilesUnderAnotherBooksTrackedFolder is #3336 part B.
// A box set registered as a whole folder contains a subfolder that another
// live book tracks as its own folder. Deleting the box set with its files must
// remove only what nobody else owns: the nested folder's files are not an
// exact book_files path, but they sit under a folder another book tracks.
func TestBookDelete_KeepsFilesUnderAnotherBooksTrackedFolder(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	root := t.TempDir()
	boxDir := filepath.Join(root, "Harry Potter 1-4")
	ownDisc := mustWrite(t, filepath.Join(boxDir, "Book 1", "01.mp3"))
	nestedDir := filepath.Join(boxDir, "Book 2")
	nestedTrack := mustWrite(t, filepath.Join(nestedDir, "01.mp3"))

	box := &models.Book{ForeignID: "B-BOX", AuthorID: author.ID, Title: "Box", SortTitle: "box", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	child := &models.Book{ForeignID: "B-CHILD", AuthorID: author.ID, Title: "Child", SortTitle: "child", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	for _, b := range []*models.Book{box, child} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := books.AddBookFile(ctx, box.ID, models.MediaTypeAudiobook, boxDir); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, child.ID, models.MediaTypeAudiobook, nestedDir); err != nil {
		t.Fatal(err)
	}

	deleteBookWithFiles(t, h, box.ID)

	if !exists(nestedTrack) {
		t.Errorf("a file under another live book's tracked folder must survive: %s", nestedTrack)
	}
	if exists(ownDisc) {
		t.Errorf("the deleted book's own file should be removed: %s", ownDisc)
	}
}

// TestBookDelete_RefusesFileInsideAnotherBooksTrackedFolder: deleting a book
// whose own tracked audio file lies inside a folder another live book tracks
// must leave the file, because the folder's owner covers its tracks.
func TestBookDelete_RefusesFileInsideAnotherBooksTrackedFolder(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	root := t.TempDir()
	boxDir := filepath.Join(root, "Harry Potter 1-4")
	track := mustWrite(t, filepath.Join(boxDir, "Book 2", "01.mp3"))

	box := &models.Book{ForeignID: "B-BOX2", AuthorID: author.ID, Title: "Box", SortTitle: "box", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	child := &models.Book{ForeignID: "B-CHILD2", AuthorID: author.ID, Title: "Child", SortTitle: "child", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	for _, b := range []*models.Book{box, child} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := books.AddBookFile(ctx, box.ID, models.MediaTypeAudiobook, boxDir); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, child.ID, models.MediaTypeAudiobook, track); err != nil {
		t.Fatal(err)
	}

	deleteBookWithFiles(t, h, child.ID)

	if !exists(track) {
		t.Errorf("a file inside another live book's tracked folder must survive: %s", track)
	}
}

// sharedFolderBooks is the #3561 layout (how #3556 gets there): book A's
// audiobook is tracked as a folder holding part1.mp3, and book B's ebook is
// tracked as a file inside that folder.
func sharedFolderBooks(t *testing.T, books *db.BookRepo, authorID int64, ctx context.Context) (a, b *models.Book, track, epub string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Jane Doe", "Shared Title (2020)")
	track = mustWrite(t, filepath.Join(dir, "part1.mp3"))
	epub = mustWrite(t, filepath.Join(dir, "Shared Title - Jane Doe.epub"))
	a = &models.Book{ForeignID: "B-A3561", AuthorID: authorID, Title: "Shared Title", SortTitle: "shared title", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	b = &models.Book{ForeignID: "B-B3561", AuthorID: authorID, Title: "Shared Title", SortTitle: "shared title", Status: "imported", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	for _, book := range []*models.Book{a, b} {
		if err := books.Create(ctx, book); err != nil {
			t.Fatal(err)
		}
	}
	if err := books.AddBookFile(ctx, a.ID, models.MediaTypeAudiobook, dir); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, b.ID, models.MediaTypeEbook, epub); err != nil {
		t.Fatal(err)
	}
	return a, b, track, epub
}

// TestBookDelete_RemovesOwnEbookInsideAnotherBooksFolder is the #3561
// regression: the guard protects the other book's files, not an ebook whose own
// row is the one being deleted.
func TestBookDelete_RemovesOwnEbookInsideAnotherBooksFolder(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	a, b, track, epub := sharedFolderBooks(t, books, author.ID, ctx)
	// A same-named file no row names stays under the folder row's protection.
	sibling := mustWrite(t, strings.TrimSuffix(epub, ".epub")+".m4b")

	deleteBookWithFiles(t, h, b.ID)

	if exists(epub) {
		t.Errorf("the deleted book's own ebook should be removed: %s", epub)
	}
	if !exists(track) {
		t.Errorf("the other book's track must survive: %s", track)
	}
	if !exists(sibling) {
		t.Errorf("a file the deleted book does not track must survive under the other book's folder: %s", sibling)
	}
	files, err := books.ListFiles(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Format != models.MediaTypeAudiobook {
		t.Errorf("the other book's folder row should be untouched, got %+v", files)
	}
}

// TestSafeRemove_OwnEbookInsideAnotherBooksFolder covers both delete helpers
// directly. Each reports the file as removed rather than skipped, which is what
// the single file delete and the format delete tell the user (#3561).
func TestSafeRemove_OwnEbookInsideAnotherBooksFolder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(ctx context.Context, books *db.BookRepo, id int64, p string) (bool, error)
	}{
		{"format scoped", func(ctx context.Context, books *db.BookRepo, id int64, p string) (bool, error) {
			return safeRemoveBookPath(ctx, nil, books, id, p, models.MediaTypeEbook)
		}},
		{"exact", func(ctx context.Context, books *db.BookRepo, id int64, p string) (bool, error) {
			return safeRemoveBookPathExact(ctx, nil, books, id, p, models.MediaTypeEbook)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, books, _, author, ctx := bookFixture(t)
			a, b, track, epub := sharedFolderBooks(t, books, author.ID, ctx)

			// The holder of the folder cannot take B's ebook through its own id.
			if skipped, err := tc.remove(ctx, books, a.ID, epub); err != nil || !skipped {
				t.Errorf("removing another book's ebook = %v, %v, want skipped", skipped, err)
			}
			if !exists(epub) {
				t.Fatalf("another book's ebook was removed: %s", epub)
			}

			if skipped, err := tc.remove(ctx, books, b.ID, epub); err != nil || skipped {
				t.Errorf("removing the book's own ebook = %v, %v, want removed", skipped, err)
			}
			if exists(epub) {
				t.Errorf("own ebook should be removed: %s", epub)
			}
			if !exists(track) {
				t.Errorf("the other book's track must survive: %s", track)
			}
		})
	}
}

// TestBookDelete_StoredTrailingSlashFolderRows: a folder row stored with a
// trailing separator must guard just as a clean one does, in both directions.
func TestBookDelete_StoredTrailingSlashFolderRows(t *testing.T) {
	for _, tc := range []struct {
		name         string
		outer, inner func(dir, sub string) string
		deleteOuter  bool
	}{
		{"outer stored with slash, inner deleted", func(d, _ string) string { return d + "/" }, func(_, s string) string { return s }, false},
		{"inner stored with slash, outer deleted", func(d, _ string) string { return d }, func(_, s string) string { return s + "/" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, books, _, author, ctx := bookFixture(t)
			dir := filepath.Join(t.TempDir(), "F")
			sub := filepath.Join(dir, "sub")
			b1 := mustWrite(t, filepath.Join(sub, "b1.mp3"))
			outer := &models.Book{ForeignID: "B-O", AuthorID: author.ID, Title: "O", SortTitle: "o", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
			inner := &models.Book{ForeignID: "B-I", AuthorID: author.ID, Title: "I", SortTitle: "i", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
			for _, b := range []*models.Book{outer, inner} {
				if err := books.Create(ctx, b); err != nil {
					t.Fatal(err)
				}
			}
			if err := books.AddBookFile(ctx, outer.ID, models.MediaTypeAudiobook, tc.outer(dir, sub)); err != nil {
				t.Fatal(err)
			}
			if err := books.AddBookFile(ctx, inner.ID, models.MediaTypeAudiobook, tc.inner(dir, sub)); err != nil {
				t.Fatal(err)
			}
			victim := inner
			if tc.deleteOuter {
				victim = outer
			}
			deleteBookWithFiles(t, h, victim.ID)
			if !exists(b1) {
				t.Errorf("file removed through a trailing slash folder row: %s", b1)
			}
		})
	}
}

// TestDeleteTrackedFile_UnderAnotherBooksFolder: the single file delete says
// why it refused, and deregister mode (no delete=true) still drops the row.
func TestDeleteTrackedFile_UnderAnotherBooksFolder(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	dir := filepath.Join(t.TempDir(), "F")
	track := mustWrite(t, filepath.Join(dir, "sub", "b1.mp3"))
	outer := &models.Book{ForeignID: "B-O5", AuthorID: author.ID, Title: "O", SortTitle: "o", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	inner := &models.Book{ForeignID: "B-I5", AuthorID: author.ID, Title: "I", SortTitle: "i", Status: "wanted", Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true}
	for _, b := range []*models.Book{outer, inner} {
		if err := books.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := books.AddBookFile(ctx, outer.ID, models.MediaTypeAudiobook, dir); err != nil {
		t.Fatal(err)
	}
	if err := books.AddBookFile(ctx, inner.ID, models.MediaTypeAudiobook, track); err != nil {
		t.Fatal(err)
	}
	call := func(query string) *httptest.ResponseRecorder {
		req := withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+strconv.FormatInt(inner.ID, 10)+"/file?"+query, nil), "id", strconv.FormatInt(inner.ID, 10))
		rec := httptest.NewRecorder()
		h.DeleteFile(rec, req)
		return rec
	}
	rec := call("path=" + url.QueryEscape(track) + "&delete=true")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another book owns it") {
		t.Fatalf("delete = %d %s, want 409 naming another book", rec.Code, rec.Body.String())
	}
	if !exists(track) {
		t.Fatal("file removed")
	}
	if rec := call("path=" + url.QueryEscape(track)); rec.Code != http.StatusOK {
		t.Fatalf("deregister = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !exists(track) {
		t.Fatal("deregister touched the disk")
	}
}
