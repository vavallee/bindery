package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// The tests in this file cover #2334: the releases the relevance and language
// filters remove come back in a separate "filtered" array, labelled not
// approved with the stage's reason, and can be grabbed through POST
// /queue/grab from the server's record of them.

// droppedSearcher answers a book search with canned results and, per media
// type, canned relevance drops carried in SearchDebug.Dropped.
type droppedSearcher struct {
	results map[string][]newznab.SearchResult
	dropped map[string][]indexer.DroppedRelease
}

func (s droppedSearcher) SearchBookWithDebug(_ context.Context, _ []models.Indexer, c indexer.MatchCriteria) ([]newznab.SearchResult, *indexer.SearchDebug) {
	res := append([]newznab.SearchResult(nil), s.results[c.MediaType]...)
	dbg := &indexer.SearchDebug{
		Filters: []indexer.FilterDebug{},
		Dropped: append([]indexer.DroppedRelease(nil), s.dropped[c.MediaType]...),
	}
	return res, dbg
}

func (droppedSearcher) SearchQuery(context.Context, []models.Indexer, string) []newznab.SearchResult {
	return nil
}

func (droppedSearcher) Cooldown(models.Indexer) (time.Time, string, bool) {
	return time.Time{}, "", false
}

type filteredResp struct {
	Results []struct {
		GUID      string `json:"guid"`
		NZBURL    string `json:"nzbUrl"`
		MediaType string `json:"mediaType"`
		Approved  bool   `json:"approved"`
		Rejection string `json:"rejection"`
	} `json:"results"`
	Filtered []struct {
		GUID      string `json:"guid"`
		Title     string `json:"title"`
		NZBURL    string `json:"nzbUrl"`
		MediaType string `json:"mediaType"`
		Approved  bool   `json:"approved"`
		Rejection string `json:"rejection"`
	} `json:"filtered"`
	FilteredTotal int `json:"filteredTotal"`
}

// searchBookFiltered runs the book search as uid/role against f's registry.
func (f *grabSecFixture) searchBookFiltered(t *testing.T, s droppedSearcher, book *models.Book, uid int64, role string) (filteredResp, string) {
	t.Helper()
	h := NewIndexerHandler(db.NewIndexerRepo(f.database), f.books, db.NewAuthorRepo(f.database),
		nil, s, db.NewSettingsRepo(f.database), db.NewBlocklistRepo(f.database)).WithSearchResults(f.queue.searchResults)
	rec := httptest.NewRecorder()
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/indexer/book/"+strconv.FormatInt(book.ID, 10)+"/search", nil),
		"id", strconv.FormatInt(book.ID, 10))
	h.SearchBook(rec, asRole(req, uid, role))
	if rec.Code != http.StatusOK {
		t.Fatalf("search: got %d: %s", rec.Code, rec.Body.String())
	}
	assertNoIndexerAPIKey(t, "search response", rec.Body.Bytes())
	var out filteredResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The debug panel is read back through its own endpoint too.
	dbgRec := httptest.NewRecorder()
	h.LastSearchDebug(dbgRec, asRole(httptest.NewRequest(http.MethodGet, "/search/last-debug", nil), uid, role))
	assertNoIndexerAPIKey(t, "last-debug response", dbgRec.Body.Bytes())
	return out, dbgRec.Body.String()
}

func (f *grabSecFixture) droppedRelease(guid, title, stage, reason string) indexer.DroppedRelease {
	signed := newznab.SignDownloadURLFor(f.downloadURL, f.indexerURL+"/3/api", leakedAPIKey)
	return indexer.DroppedRelease{
		Stage: stage, Reason: reason,
		Result: newznab.SearchResult{
			GUID: guid, IndexerID: f.indexerID, IndexerName: "prowlarr", Title: title,
			Size: 4321, NZBURL: signed, Protocol: "usenet",
		},
	}
}

// A relevance drop is returned in "filtered", not approved, with the stage's
// reason, redacted like the main list, and absent from the debug payload.
func TestSearchBook_ReturnsFilteredReleases(t *testing.T) {
	f := newGrabSecFixture(t)
	book := regrabOwnedBook(t, f.database, f.books, "filt", f.bob)
	s := droppedSearcher{
		results: map[string][]newznab.SearchResult{models.MediaTypeEbook: {{GUID: "kept-1", Title: "Kept epub", NZBURL: "https://x/kept"}}},
		dropped: map[string][]indexer.DroppedRelease{models.MediaTypeEbook: {f.droppedRelease("filt-1", "Odd Name epub", indexer.StageRelevance, "title/author keywords did not match release name")}},
	}

	resp, dbgBody := f.searchBookFiltered(t, s, book, f.bob, auth.RoleUser)

	if len(resp.Results) != 1 || resp.Results[0].GUID != "kept-1" {
		t.Fatalf("results = %+v, want only kept-1", resp.Results)
	}
	if len(resp.Filtered) != 1 || resp.FilteredTotal != 1 {
		t.Fatalf("filtered = %+v (total %d), want one release", resp.Filtered, resp.FilteredTotal)
	}
	got := resp.Filtered[0]
	if got.GUID != "filt-1" || got.Approved || got.Rejection != "title/author keywords did not match release name" {
		t.Errorf("filtered item = %+v", got)
	}
	if strings.Contains(got.NZBURL, "apikey") {
		t.Errorf("filtered nzbUrl is not redacted: %q", got.NZBURL)
	}
	if strings.Contains(dbgBody, "filt-1") {
		t.Errorf("the debug payload carries the dropped release: %s", dbgBody)
	}
}

// The feature itself: a filtered release is grabbed from the server's record,
// for a user role account and for an admin session alike, and the stored URL is
// the real one even though the response never showed it.
func TestQueueGrab_FilteredReleaseIsGrabbableFromTheRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		role string
	}{{"user", auth.RoleUser}, {"admin", auth.RoleAdmin}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGrabSecFixture(t)
			book := regrabOwnedBook(t, f.database, f.books, "grab-"+tc.name, f.bob)
			s := droppedSearcher{dropped: map[string][]indexer.DroppedRelease{models.MediaTypeEbook: {f.droppedRelease("filt-grab", "Lee Child - One Shot (epub)", indexer.StageRelevance, "r")}}}
			resp, _ := f.searchBookFiltered(t, s, book, f.bob, tc.role)
			if len(resp.Filtered) != 1 {
				t.Fatalf("filtered = %+v", resp.Filtered)
			}

			rec := f.grabAs(f.bob, tc.role, map[string]any{
				"guid": resp.Filtered[0].GUID, "title": resp.Filtered[0].Title, "nzbUrl": resp.Filtered[0].NZBURL,
				"indexerId": f.indexerID, "bookId": book.ID,
			})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("grab of a filtered release: got %d: %s", rec.Code, rec.Body.String())
			}
			assertNoIndexerAPIKey(t, "grab response", rec.Body.Bytes())
			if n := f.downloadHits.Load(); n != 1 {
				t.Errorf("the recorded download URL must be fetched once; got %d", n)
			}
			dl, err := f.downloads.GetByGUID(f.ctx, "filt-grab")
			if err != nil || dl == nil {
				t.Fatalf("download row: %v", err)
			}
			if !strings.HasPrefix(dl.NZBURL, f.indexerURL+"/3/download") {
				t.Errorf("stored URL = %q, want the recorded one", newznab.RedactDownloadURL(dl.NZBURL))
			}
		})
	}
}

// A release the language filter removes is offered too, with its own reason.
func TestSearchBook_LanguageDropsAreFiltered(t *testing.T) {
	f := newGrabSecFixture(t)
	if err := db.NewSettingsRepo(f.database).Set(f.ctx, "search.preferredLanguage", "en"); err != nil {
		t.Fatal(err)
	}
	book := regrabOwnedBook(t, f.database, f.books, "lang", f.bob)
	s := droppedSearcher{results: map[string][]newznab.SearchResult{models.MediaTypeEbook: {
		{GUID: "en-1", Title: "Some Book ENGLISH epub", NZBURL: "https://x/en"},
		{GUID: "de-1", Title: "Some Book GERMAN epub", NZBURL: "https://x/de?apikey=K"},
	}}}

	resp, _ := f.searchBookFiltered(t, s, book, f.bob, auth.RoleUser)

	if len(resp.Results) != 1 || resp.Results[0].GUID != "en-1" {
		t.Fatalf("results = %+v, want only en-1", resp.Results)
	}
	if len(resp.Filtered) != 1 || resp.Filtered[0].GUID != "de-1" {
		t.Fatalf("filtered = %+v, want the German release", resp.Filtered)
	}
	if resp.Filtered[0].Approved || !strings.Contains(resp.Filtered[0].Rejection, "language") {
		t.Errorf("filtered item = %+v, want not approved with a language reason", resp.Filtered[0])
	}
	if strings.Contains(resp.Filtered[0].NZBURL, "apikey") {
		t.Errorf("language drop not redacted: %q", resp.Filtered[0].NZBURL)
	}
	// And it is grabbable: the registry holds it.
	if _, ok := f.queue.searchResults.lookup("de-1"); !ok {
		t.Error("the language-dropped release was not recorded in the registry")
	}
}

// A dual-format book merges both legs' drops, stamps each with its leg's media
// type, and lists a GUID once, never one that is also in the main results.
func TestSearchBook_DualFormatMergesFiltered(t *testing.T) {
	f := newGrabSecFixture(t)
	book := regrabOwnedBook(t, f.database, f.books, "dual", f.bob)
	if _, err := f.database.ExecContext(f.ctx, "UPDATE books SET media_type=? WHERE id=?", models.MediaTypeBoth, book.ID); err != nil {
		t.Fatal(err)
	}
	s := droppedSearcher{
		results: map[string][]newznab.SearchResult{
			models.MediaTypeEbook:     {{GUID: "eb-kept", Title: "Kept epub"}},
			models.MediaTypeAudiobook: {{GUID: "au-kept", Title: "Kept m4b"}},
		},
		dropped: map[string][]indexer.DroppedRelease{
			models.MediaTypeEbook:     {f.droppedRelease("eb-drop", "Odd epub", indexer.StageRelevance, "r1"), f.droppedRelease("au-kept", "Kept m4b", indexer.StageRelevance, "dup of a kept one")},
			models.MediaTypeAudiobook: {f.droppedRelease("au-drop", "Odd m4b", indexer.StageRelevance, "r2")},
		},
	}

	resp, _ := f.searchBookFiltered(t, s, book, f.bob, auth.RoleUser)

	byGUID := map[string]string{}
	for _, r := range resp.Filtered {
		byGUID[r.GUID] = r.MediaType
	}
	if len(resp.Filtered) != 2 || byGUID["eb-drop"] != "ebook" || byGUID["au-drop"] != "audiobook" {
		t.Errorf("filtered = %+v, want eb-drop (ebook) and au-drop (audiobook)", resp.Filtered)
	}
	if _, dup := byGUID["au-kept"]; dup {
		t.Error("a release in the main results is repeated in filtered")
	}
}

// Filtered releases are bounded: a generic title can drop hundreds. The
// response keeps the first maxFilteredReleases (the searcher ranks best first),
// reports the full count, and the main results stay grabbable.
func TestSearchBook_FilteredIsCapped(t *testing.T) {
	f := newGrabSecFixture(t)
	book := regrabOwnedBook(t, f.database, f.books, "cap", f.bob)
	var dropped []indexer.DroppedRelease
	for i := 0; i < maxFilteredReleases+50; i++ {
		dropped = append(dropped, f.droppedRelease("cap-"+strconv.Itoa(i), "Odd "+strconv.Itoa(i), indexer.StageRelevance, "r"))
	}
	s := droppedSearcher{
		results: map[string][]newznab.SearchResult{models.MediaTypeEbook: {{GUID: "kept-cap", Title: "Kept epub"}}},
		dropped: map[string][]indexer.DroppedRelease{models.MediaTypeEbook: dropped},
	}

	resp, _ := f.searchBookFiltered(t, s, book, f.bob, auth.RoleUser)

	if len(resp.Filtered) != maxFilteredReleases || resp.FilteredTotal != maxFilteredReleases+50 {
		t.Errorf("filtered %d / total %d, want %d / %d", len(resp.Filtered), resp.FilteredTotal, maxFilteredReleases, maxFilteredReleases+50)
	}
	if resp.Filtered[0].GUID != "cap-0" {
		t.Errorf("first filtered = %q, want the best ranked (cap-0)", resp.Filtered[0].GUID)
	}
	if _, ok := f.queue.searchResults.lookup("kept-cap"); !ok {
		t.Error("main result missing from the registry")
	}
	if _, ok := f.queue.searchResults.lookup("cap-" + strconv.Itoa(maxFilteredReleases)); ok {
		t.Error("a release past the cap was remembered")
	}
}
