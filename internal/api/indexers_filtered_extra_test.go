package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The handler files the search's audit trail in its debug log; that trail must
// not keep the dropped releases, whose URLs are raw and carry credentials.
func TestSearchBook_StoredDebugDoesNotKeepDroppedReleases(t *testing.T) {
	f := newGrabSecFixture(t)
	book := regrabOwnedBook(t, f.database, f.books, "dbgclear", f.bob)
	s := droppedSearcher{dropped: map[string][]indexer.DroppedRelease{
		models.MediaTypeEbook: {f.droppedRelease("keep-me-not", "Odd epub", indexer.StageRelevance, "r")},
	}}
	h := NewIndexerHandler(db.NewIndexerRepo(f.database), f.books, db.NewAuthorRepo(f.database),
		nil, s, db.NewSettingsRepo(f.database), db.NewBlocklistRepo(f.database)).WithSearchResults(f.queue.searchResults)
	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/x", nil), "id", strconv.FormatInt(book.ID, 10))
	h.SearchBook(rec, asRole(req, f.bob, auth.RoleUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}

	stored := h.lastDebug.Latest(f.bob, false, func(int64) bool { return true })
	if stored == nil {
		t.Fatal("the search left nothing in the debug log")
	}
	if len(stored.Dropped) != 0 {
		t.Errorf("the stored trail keeps %d dropped release(s) with raw URLs", len(stored.Dropped))
	}
}

// Releases are one release when their credential stripped GUIDs match, the key
// the registry and the UI use, whatever credentials each carries.
func TestCollectFiltered_DedupesOnTheRegistryKey(t *testing.T) {
	kept := []newznab.SearchResult{{GUID: "https://tr.example/dl/1?apikey=AAA"}}
	mk := func(guid string) indexer.DroppedRelease {
		return indexer.DroppedRelease{Result: newznab.SearchResult{GUID: guid}, Stage: indexer.StageRelevance}
	}
	got, total := collectFiltered(kept, nil, "", []indexer.DroppedRelease{
		mk("https://tr.example/dl/1?apikey=BBB"), // same as a kept release
		mk("https://tr.example/dl/2?apikey=CCC"),
		mk("https://tr.example/dl/2?apikey=DDD"), // same as the line above
	})
	if total != 1 || len(got) != 1 || registryKey(got[0].Result.GUID) != registryKey("https://tr.example/dl/2") {
		t.Errorf("got %d (total %d): %+v, want the one distinct release", len(got), total, got)
	}
}

// A dual-format book's cap keeps the best of each format, not all of one.
func TestCollectFiltered_CapAlternatesFormats(t *testing.T) {
	var rel []indexer.DroppedRelease
	for i := 0; i < 80; i++ {
		rel = append(rel, indexer.DroppedRelease{Result: newznab.SearchResult{GUID: "e" + strconv.Itoa(i), MediaType: "ebook"}})
	}
	for i := 0; i < 80; i++ {
		rel = append(rel, indexer.DroppedRelease{Result: newznab.SearchResult{GUID: "a" + strconv.Itoa(i), MediaType: "audiobook"}})
	}
	got, total := collectFiltered(nil, nil, "", rel)
	if total != 160 || len(got) != maxFilteredReleases {
		t.Fatalf("got %d of %d", len(got), total)
	}
	counts := map[string]int{}
	for _, d := range got {
		counts[d.Result.MediaType]++
	}
	if counts["ebook"] != 50 || counts["audiobook"] != 50 {
		t.Errorf("kept %v, want 50 of each format", counts)
	}
	if got[0].Result.GUID != "e0" || got[1].Result.GUID != "a0" {
		t.Errorf("each format's own order must hold: %s, %s", got[0].Result.GUID, got[1].Result.GUID)
	}
}
