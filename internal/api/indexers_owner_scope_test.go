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

// Interactive search scopes the author's other titles to the signed in user: a
// second user's books by the same author never reach the relevance filter
// (#3268).
func TestSearchBook_AuthorTitlesAreScopedToTheCaller(t *testing.T) {
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

	mock := &mockIndexerSearcher{audioResults: []newznab.SearchResult{{GUID: "r1", Title: "Leigh Bardugo - King of Scars MP3"}}}
	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), mock,
		db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
	)
	rec := httptest.NewRecorder()
	req := withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(wanted.ID, 10),
	)
	req = req.WithContext(auth.WithUserID(req.Context(), u1.ID))
	h.SearchBook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := mock.criteria().AuthorTitles; !slices.Equal(got, []string{"Rule of Wolves"}) {
		t.Errorf("AuthorTitles = %v, want only the caller's other title", got)
	}
}
