package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
// whose own tracked file lies inside a folder another live book tracks must
// leave the file, because the folder's owner covers it.
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
