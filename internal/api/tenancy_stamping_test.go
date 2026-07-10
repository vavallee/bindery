package api

// Cross-user regression tests for #1457 (owner stamping on create paths) and
// #1416 (author-detail vs book-list scope divergence), following the v1.1.1
// audit convention: two users, assert isolation.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestAddBook_StampsOwnerOnDirectInsert pins the #1457 fix on the AddBook
// direct-insert path: the created book inherits the author's owner, so a
// per-user library sees its own adds.
func TestAddBook_StampsOwnerOnDirectInsert(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OL-ALICE", Name: "Alice Author", SortName: "Author, Alice",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authorRepo.CreateForUser(ctx, author, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := authorRepo.UpsertAuthorIdentifier(ctx, author.ID, "hc:alice-author"); err != nil {
		t.Fatal(err)
	}
	primary := &models.Book{
		ForeignID: "hc:book-one", Title: "Book One", SortTitle: "Book One",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "hardcover",
	}
	provider := &stubMetaProvider{
		name:        "hardcover",
		getBookByID: map[string]*models.Book{"hc:book-one": primary},
	}
	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   "hc:book-one",
		"foreignAuthorId": "hc:alice-author",
		"authorName":      "Alice Author",
	})
	parent, cancel := context.WithTimeout(auth.WithUserID(context.Background(), alice.ID), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)).WithContext(parent)
	rec := httptest.NewRecorder()

	h.AddBook(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := bookRepo.GetByForeignID(ctx, "hc:book-one")
	if err != nil || got == nil {
		t.Fatalf("book after AddBook = %+v err=%v", got, err)
	}
	if got.OwnerUserID != alice.ID {
		t.Fatalf("book owner = %d, want alice (%d)", got.OwnerUserID, alice.ID)
	}
}

// TestSeriesList_ScopesBooksByOwner pins #1457's series fix: a non-admin
// browsing the series view must not see another user's books, while legacy
// NULL-owned books stay visible to everyone.
func TestSeriesList_ScopesBooksByOwner(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "h1")
	bob, _ := users.Create(ctx, "bob", "h2")

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	seriesRepo := db.NewSeriesRepo(database)

	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	mk := func(fid, title string, owner int64) *models.Book {
		b := &models.Book{
			ForeignID: fid, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
			OwnerUserID: owner,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	aliceBook := mk("S-A", "Alice Secret Book", alice.ID)
	legacyBook := mk("S-L", "Legacy Shared Book", 0)

	series, err := seriesRepo.CreateManual(ctx, "Shared Series")
	if err != nil {
		t.Fatal(err)
	}
	_ = seriesRepo.LinkBook(ctx, series.ID, aliceBook.ID, "1", true)
	_ = seriesRepo.LinkBook(ctx, series.ID, legacyBook.ID, "2", false)

	h := NewSeriesHandler(seriesRepo, bookRepo, authorRepo, nil, nil)

	// Bob (role user) lists series: alice's book must be absent, the legacy
	// NULL-owned book present.
	bobCtx := auth.WithUserRole(auth.WithUserID(context.Background(), bob.ID), "user")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/series", nil).WithContext(bobCtx)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var listed []models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("series count = %d, want 1", len(listed))
	}
	titles := map[string]bool{}
	for _, sb := range listed[0].Books {
		if sb.Book != nil {
			titles[sb.Book.Title] = true
		}
	}
	if titles["Alice Secret Book"] {
		t.Fatalf("bob can see alice's book through the series list: %v", titles)
	}
	if !titles["Legacy Shared Book"] {
		t.Fatalf("legacy NULL-owned book missing from bob's series view: %v", titles)
	}

	// Single-series GET applies the same scope.
	req = withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/"+strconv.FormatInt(series.ID, 10), nil).WithContext(bobCtx), "id", strconv.FormatInt(series.ID, 10))
	rec = httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("series get: expected 200, got %d", rec.Code)
	}
	var one models.Series
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	for _, sb := range one.Books {
		if sb.Book != nil && sb.Book.Title == "Alice Secret Book" {
			t.Fatalf("bob can see alice's book through series get")
		}
	}

	// Admin still sees everything.
	adminCtx := auth.WithUserRole(auth.WithUserID(context.Background(), alice.ID), "admin")
	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil).WithContext(adminCtx))
	var adminListed []models.Series
	_ = json.Unmarshal(rec.Body.Bytes(), &adminListed)
	if len(adminListed) != 1 || len(adminListed[0].Books) != 2 {
		t.Fatalf("admin view lost books: %+v", adminListed)
	}
}

// TestAuthorGet_ScopesEmbeddedBooks pins the #1416 divergence fix: the
// author-detail payload's embedded books apply the same owner scope as the
// book list, so the two views agree.
func TestAuthorGet_ScopesEmbeddedBooks(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "h1")
	bob, _ := users.Create(ctx, "bob", "h2")

	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	profileRepo := db.NewMetadataProfileRepo(database)

	// Shared (NULL-owned) author with one book per user.
	author := &models.Author{ForeignID: "OL1A", Name: "Shared Author", SortName: "Author, Shared", MetadataProvider: "openlibrary"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fid, title string
		owner      int64
	}{
		{"B-A", "Alice Book", alice.ID},
		{"B-B", "Bob Book", bob.ID},
	} {
		b := &models.Book{
			ForeignID: tc.fid, AuthorID: author.ID, Title: tc.title, SortTitle: tc.title,
			Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary",
			OwnerUserID: tc.owner,
		}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, nil, nil, profileRepo, nil)
	bobCtx := auth.WithUserRole(auth.WithUserID(context.Background(), bob.ID), "user")
	id := strconv.FormatInt(author.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/author/"+id, nil).WithContext(bobCtx), "id", id)
	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, b := range got.Books {
		if b.Title == "Alice Book" {
			t.Fatalf("bob's author-detail payload leaks alice's book")
		}
	}
	found := false
	for _, b := range got.Books {
		if b.Title == "Bob Book" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bob's own book missing from author detail: %+v", got.Books)
	}
}

// TestDownloadCreate_PersistsOwner pins the repo-level stamping (#1457):
// downloads use the strict owner scope, so the row must round-trip its owner.
func TestDownloadCreate_PersistsOwner(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	users := db.NewUserRepo(database)
	alice, _ := users.Create(ctx, "alice", "h1")
	dlRepo := db.NewDownloadRepo(database)

	dl := &models.Download{GUID: "g-1", Title: "T", Status: models.StateGrabbed, Protocol: "usenet", OwnerUserID: alice.ID}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	mine, err := dlRepo.ListByUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].OwnerUserID != alice.ID {
		t.Fatalf("alice's downloads = %+v, want her stamped row", mine)
	}

	// Unowned rows keep NULL (legacy semantics): invisible under the strict
	// user scope, visible unscoped.
	unowned := &models.Download{GUID: "g-2", Title: "T2", Status: models.StateGrabbed, Protocol: "usenet"}
	if err := dlRepo.Create(ctx, unowned); err != nil {
		t.Fatal(err)
	}
	mine, _ = dlRepo.ListByUser(ctx, alice.ID)
	if len(mine) != 1 {
		t.Fatalf("strict scope must hide NULL-owned rows, got %d", len(mine))
	}
	all, _ := dlRepo.ListByUser(ctx, 0)
	if len(all) != 2 {
		t.Fatalf("unscoped list = %d rows, want 2", len(all))
	}
}
