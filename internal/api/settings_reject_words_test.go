package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The rejected words list is admin only both ways (#3416): a non admin can
// neither write it nor see it.
func TestSettings_RejectWordsAdminOnly(t *testing.T) {
	h, repo, ctx := settingsFixture(t)
	if !IsKnownSettingKey(indexer.SettingRejectWords) {
		t.Fatal("search.rejectWords has no registry descriptor")
	}
	if err := repo.Set(ctx, indexer.SettingRejectWords, "Graphic Audio"); err != nil {
		t.Fatal(err)
	}

	gated := auth.RequireAdmin(http.HandlerFunc(h.Set))
	req := userReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+indexer.SettingRejectWords,
		bytes.NewBufferString(`{"value":"anything"}`)), indexer.SettingRejectWords))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT as non-admin = %d, want 403", rec.Code)
	}
	if got, _ := repo.Get(ctx, indexer.SettingRejectWords); got == nil || got.Value != "Graphic Audio" {
		t.Fatalf("non-admin PUT changed the value: %+v", got)
	}

	rec = httptest.NewRecorder()
	h.List(rec, userReq(httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil)))
	if strings.Contains(rec.Body.String(), indexer.SettingRejectWords) || strings.Contains(rec.Body.String(), "Graphic Audio") {
		t.Errorf("List leaked the list to a non-admin: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Get(rec, userReq(withKey(httptest.NewRequest(http.MethodGet, "/api/v1/setting/"+indexer.SettingRejectWords, nil), indexer.SettingRejectWords)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("Get as non-admin = %d, want 404", rec.Code)
	}

	// The admin can write it and read it back.
	req = adminReq(withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+indexer.SettingRejectWords,
		bytes.NewBufferString(`{"value":"Full Cast, abridged"}`)), indexer.SettingRejectWords))
	rec = httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT as admin = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := repo.Get(ctx, indexer.SettingRejectWords); got == nil || got.Value != "Full Cast, abridged" {
		t.Fatalf("admin PUT not stored: %+v", got)
	}
}

// critSearcher records the criteria the interactive search built.
type critSearcher struct {
	droppedSearcher
	got *indexer.MatchCriteria
}

func (s critSearcher) SearchBookWithDebug(ctx context.Context, idx []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug) {
	*s.got = c
	return s.droppedSearcher.SearchBookWithDebug(ctx, idx, c)
}

// The interactive search hands the stored list to the searcher, and an unset
// setting hands it nothing.
func TestSearchBook_PassesRejectWords(t *testing.T) {
	f := newGrabSecFixture(t)
	book := regrabOwnedBook(t, f.database, f.books, "rw", f.bob)
	run := func() indexer.MatchCriteria {
		var got indexer.MatchCriteria
		h := NewIndexerHandler(db.NewIndexerRepo(f.database), f.books, db.NewAuthorRepo(f.database),
			nil, critSearcher{got: &got}, db.NewSettingsRepo(f.database), db.NewBlocklistRepo(f.database)).WithSearchResults(f.queue.searchResults)
		rec := httptest.NewRecorder()
		req := withURLParam(httptest.NewRequest(http.MethodGet, "/indexer/book/"+strconv.FormatInt(book.ID, 10)+"/search", nil),
			"id", strconv.FormatInt(book.ID, 10))
		h.SearchBook(rec, asRole(req, f.bob, auth.RoleUser))
		if rec.Code != http.StatusOK {
			t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
		}
		return got
	}
	if c := run(); len(c.RejectWords) != 0 {
		t.Fatalf("unset setting produced %v", c.RejectWords)
	}
	if err := db.NewSettingsRepo(f.database).Set(f.ctx, indexer.SettingRejectWords, "Graphic Audio\nFull Cast"); err != nil {
		t.Fatal(err)
	}
	if c := run(); len(c.RejectWords) != 2 || c.RejectWords[0] != "Graphic Audio" || c.RejectWords[1] != "Full Cast" {
		t.Fatalf("RejectWords = %v", c.RejectWords)
	}
}
