package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Interactive search scopes the author's other titles to the book's owner: a
// second user's books by the same author never reach the relevance filter
// (#3268), whoever runs the search.
func TestSearchBook_AuthorTitlesAreScopedToTheOwner(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	u1, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL3268OA", Name: "Leigh Bardugo", SortName: "Bardugo, Leigh", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := db.NewBookRepo(database)
	mk := func(fid, title string, owner int64) *models.Book {
		b := &models.Book{Title: title, ForeignID: fid, AuthorID: author.ID, MediaType: models.MediaTypeAudiobook, Monitored: true, OwnerUserID: owner}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	wanted := mk("OL3268OM1", "King of Scars", u1.ID)
	mk("OL3268OM2", "Rule of Wolves", u1.ID)
	mk("OL3268OM3", "Bobs Private Title", u2.ID)
	unowned := mk("OL3268OM4", "Shadow and Bone", 0)

	// The owner searching sees only their own titles. Bob, as an admin,
	// searching Alice's book gets Alice's titles too, not his own, so the
	// verdict matches the automatic search and the importer, which both scope
	// by the book's owner (#3247). A book with no owner falls back to the
	// caller: owner 0 would list every user's books.
	for _, tc := range []struct {
		name string
		book *models.Book
		uid  int64
		role string
		want []string
	}{
		{"owner", wanted, u1.ID, "", []string{"Rule of Wolves", "Shadow and Bone"}},
		{"admin searching another user's book", wanted, u2.ID, "admin", []string{"Rule of Wolves", "Shadow and Bone"}},
		{"user searching an unowned book", unowned, u1.ID, "", []string{"King of Scars", "Rule of Wolves"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockIndexerSearcher{audioResults: []newznab.SearchResult{{GUID: "r1", Title: "Leigh Bardugo - King of Scars MP3"}}}
			h := NewIndexerHandler(
				db.NewIndexerRepo(database), bookRepo, authorRepo,
				db.NewMetadataProfileRepo(database), mock,
				db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
			)
			rec := httptest.NewRecorder()
			req := withURLParam(
				httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
				"id", strconv.FormatInt(tc.book.ID, 10),
			)
			ctx := auth.WithUserID(req.Context(), tc.uid)
			if tc.role != "" {
				ctx = auth.WithUserRole(ctx, tc.role)
			}
			h.SearchBook(rec, req.WithContext(ctx))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			got := slices.Clone(mock.criteria().AuthorTitles)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("AuthorTitles = %v, want %v", got, tc.want)
			}
		})
	}
}
