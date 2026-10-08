package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// TestBookToggleExcludedCoverage flips the excluded flag both ways, and under
// tenancy hides another user's book behind a 404 without touching it.
func TestBookToggleExcludedCoverage(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := t.Context()
	books := db.NewBookRepo(database)
	authors := db.NewAuthorRepo(database)
	users := db.NewUserRepo(database)
	h := NewBookHandler(books, nil, db.NewHistoryRepo(database), nil)
	alice, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	owner := alice.ID
	author := &models.Author{ForeignID: "OL-EXCL-A", Name: "Excl Author", SortName: "Author, Excl", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL-EXCL", AuthorID: author.ID, Title: "Excludable", SortTitle: "excludable",
		Status: models.BookStatusWanted, Monitored: true, OwnerUserID: owner}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	toggle := func(uid int64, role, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/exclude", nil)
		req = req.WithContext(auth.WithUserRole(auth.WithUserID(req.Context(), uid), role))
		rec := httptest.NewRecorder()
		h.ToggleExcluded(rec, withURLParam(req, "id", id))
		return rec
	}
	id := strconv.FormatInt(book.ID, 10)

	for _, want := range []bool{true, false} {
		rec := toggle(owner, auth.RoleUser, id)
		if rec.Code != http.StatusOK {
			t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
		}
		var got models.Book
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		stored, _ := books.GetByID(ctx, book.ID)
		if got.Excluded != want || stored.Excluded != want {
			t.Fatalf("excluded response=%v stored=%v, want %v", got.Excluded, stored.Excluded, want)
		}
	}

	if rec := toggle(owner, auth.RoleUser, "nope"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d, want 400", rec.Code)
	}
	if rec := toggle(owner, auth.RoleUser, "999999"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing book: %d, want 404", rec.Code)
	}

	auth.SetEnforceTenancyForTests(t, true)
	if rec := toggle(bob.ID, auth.RoleUser, id); rec.Code != http.StatusNotFound {
		t.Fatalf("other user's book: %d, want 404", rec.Code)
	}
	if stored, _ := books.GetByID(ctx, book.ID); stored.Excluded {
		t.Fatal("a cross-user toggle changed the book")
	}
	// An admin may manage every user's library.
	if rec := toggle(bob.ID, auth.RoleAdmin, id); rec.Code != http.StatusOK {
		t.Fatalf("admin toggle: %d, want 200", rec.Code)
	}
}
