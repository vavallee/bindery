package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// TestSearchBook_BlocklistEntryBlocksOnlyItsOwnBook is #3563 for interactive
// search. alice's book has the release blocklisted for it (here, her profile
// refused the format at import). bob's book for the same work must still be
// offered the release as approved, while alice's book still sees it rejected.
// A release scoped entry, the download client's verdict that the release is
// broken, keeps blocking it for both.
func TestSearchBook_BlocklistEntryBlocksOnlyItsOwnBook(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	users := db.NewUserRepo(database)
	alice, err := users.Create(ctx, "alice", "x")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, "bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	authorRepo := db.NewAuthorRepo(database)
	bookRepo := db.NewBookRepo(database)
	mk := func(owner int64, suffix string) *models.Book {
		t.Helper()
		a := &models.Author{ForeignID: "OL3563A" + suffix, Name: "Leigh Bardugo", SortName: "Bardugo, Leigh", MetadataProvider: "openlibrary", Monitored: true, OwnerUserID: owner}
		if err := authorRepo.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		b := &models.Book{Title: "King of Scars", ForeignID: "OL3563B" + suffix, AuthorID: a.ID, MediaType: models.MediaTypeEbook, Monitored: true, OwnerUserID: owner}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	aliceBook := mk(alice.ID, "1")
	bobBook := mk(bob.ID, "2")

	blocklist := db.NewBlocklistRepo(database)
	if err := blocklist.Create(ctx, &models.BlocklistEntry{
		BookID: &aliceBook.ID, GUID: "r-book", Title: "Leigh Bardugo - King of Scars mobi",
		Reason: "format mobi is not allowed", Scope: models.BlocklistScopeBook,
	}); err != nil {
		t.Fatal(err)
	}
	if err := blocklist.Create(ctx, &models.BlocklistEntry{
		BookID: &aliceBook.ID, GUID: "r-broken", Title: "Leigh Bardugo - King of Scars epub",
		Reason: "downloadFailed: FAILURE/PAR", Scope: models.BlocklistScopeRelease,
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockIndexerSearcher{ebookResults: []newznab.SearchResult{
		{GUID: "r-book", Title: "Leigh Bardugo - King of Scars mobi"},
		{GUID: "r-broken", Title: "Leigh Bardugo - King of Scars epub"},
	}}
	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), mock,
		db.NewSettingsRepo(database), blocklist,
	)
	search := func(book *models.Book, uid int64) map[string]searchDecision {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withURLParam(
			httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
			"id", strconv.FormatInt(book.ID, 10),
		)
		req = req.WithContext(auth.WithUserID(req.Context(), uid))
		h.SearchBook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Results []searchDecision `json:"results"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := map[string]searchDecision{}
		for _, d := range body.Results {
			out[d.GUID] = d
		}
		return out
	}

	forBob := search(bobBook, bob.ID)
	if d, ok := forBob["r-book"]; !ok || !d.Approved {
		t.Errorf("#3563: alice's book scoped entry blocked bob's book: %+v", d)
	}
	if d := forBob["r-broken"]; d.Approved || d.Rejection != "release is blocklisted" {
		t.Errorf("a release scoped entry must block every book, bob got %+v", d)
	}
	forAlice := search(aliceBook, alice.ID)
	for _, guid := range []string{"r-book", "r-broken"} {
		if d := forAlice[guid]; d.Approved || d.Rejection != "release is blocklisted" {
			t.Errorf("alice's own entry for %s must still block her book, got %+v", guid, d)
		}
	}
}
