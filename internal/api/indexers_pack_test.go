package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

type packSearchResult struct {
	GUID      string `json:"guid"`
	Approved  bool   `json:"approved"`
	Rejection string `json:"rejection"`
	Pack      string `json:"pack"`
}

// runPackSearch runs an interactive search for wanted, by an author whose
// other library books are siblings, with the indexer answering releases.
func runPackSearch(t *testing.T, wanted string, siblings []string, releases []newznab.SearchResult) []packSearchResult {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	authorRepo := db.NewAuthorRepo(database)
	author := &models.Author{ForeignID: "OL3247A", Name: "Jane Doe", SortName: "Doe, Jane", MetadataProvider: "openlibrary", Monitored: true}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := db.NewBookRepo(database)
	var book *models.Book
	for i, title := range append([]string{wanted}, siblings...) {
		b := &models.Book{Title: title, ForeignID: "OL3247M" + strconv.Itoa(i), AuthorID: author.ID, MediaType: models.MediaTypeEbook, Monitored: true}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if book == nil {
			book = b
		}
	}

	h := NewIndexerHandler(
		db.NewIndexerRepo(database), bookRepo, authorRepo,
		db.NewMetadataProfileRepo(database), &mockIndexerSearcher{ebookResults: releases},
		db.NewSettingsRepo(database), db.NewBlocklistRepo(database),
	)
	rec := httptest.NewRecorder()
	h.SearchBook(rec, withURLParam(
		httptest.NewRequest(http.MethodGet, "/indexer/book/1/search", nil),
		"id", strconv.FormatInt(book.ID, 10),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []packSearchResult `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Results
}

// TestSearchBook_LabelsMultiBookPacks is #3247: interactive search shows a
// pack as a pack, with the words it was judged on, below the single books,
// and still returns it so it can be grabbed by hand.
func TestSearchBook_LabelsMultiBookPacks(t *testing.T) {
	t.Run("range after the wanted title", func(t *testing.T) {
		got := runPackSearch(t, "Dominion of Blades: A LitRPG Adventure (Volume 1)", nil, []newznab.SearchResult{
			{GUID: "pack", Title: "Dominion of Blades 1-2"},
			{GUID: "single", Title: "Dominion of Blades - Jane Doe (epub)"},
		})
		if len(got) != 2 {
			t.Fatalf("expected both releases returned, got %+v", got)
		}
		if got[0].GUID != "single" || got[1].GUID != "pack" {
			t.Errorf("the pack should sort below the single book, got order %s, %s", got[0].GUID, got[1].GUID)
		}
		for _, r := range got {
			switch r.GUID {
			case "pack":
				if r.Pack != "Dominion of Blades 1-2" {
					t.Errorf("pack = %q, want %q", r.Pack, "Dominion of Blades 1-2")
				}
				if r.Approved || !strings.HasPrefix(r.Rejection, decision.RejectionMultiBookPack) {
					t.Errorf("pack should be unapproved with a pack reason, got approved=%v rejection=%q", r.Approved, r.Rejection)
				}
			case "single":
				if r.Pack != "" || !r.Approved {
					t.Errorf("single book flagged: approved=%v pack=%q rejection=%q", r.Approved, r.Pack, r.Rejection)
				}
			}
		}
	})

	t.Run("semicolon list of the author's titles", func(t *testing.T) {
		got := runPackSearch(t, "Chamber", []string{"The Firm", "The Appeal"}, []newznab.SearchResult{
			{GUID: "pack", Title: "Jane Doe - Three Classic Thrillers (The Firm; The Appeal; The Chamber) (v5.0) (epub)"},
			{GUID: "single", Title: "Jane Doe - The Chamber (epub)"},
		})
		var pack *packSearchResult
		for i := range got {
			if got[i].GUID == "pack" {
				pack = &got[i]
			} else if got[i].Pack != "" {
				t.Errorf("%s flagged as a pack: %q", got[i].GUID, got[i].Pack)
			}
		}
		if pack == nil {
			t.Fatalf("the pack was not returned: %+v", got)
		}
		if pack.Pack != "The Firm; The Appeal; The Chamber" {
			t.Errorf("pack = %q, want the title list", pack.Pack)
		}
	})
}
