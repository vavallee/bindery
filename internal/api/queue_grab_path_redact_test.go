package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Private trackers put the passkey or RSS key in the URL path, which query
// parameter stripping never touched. The fake keys are built at run time so
// no high entropy literal sits in the source for a secret scanner to flag.
var (
	pathPasskey = strings.Repeat("0a1b", 8) // 32 hex, a passkey
	pathRSSKey  = strings.Repeat("aB3d", 8) // 32 base62, a UNIT3D rsskey
)

func assertNoPathSecret(t *testing.T, what, body string) {
	t.Helper()
	for _, s := range []string{pathPasskey, pathRSSKey} {
		if strings.Contains(body, s) {
			t.Fatalf("%s carries a path secret %q: %s", what, s, body)
		}
	}
}

// A Torznab result whose download URL and GUID carry a passkey in the path is
// shown without it to a user and to an admin, in the search, grab and queue
// responses, and grabbing it by the GUID the search returned still sends the
// real URL. The GUID the response shows is the registry key, so the two must
// come from the same redaction.
func TestSearchAndGrab_PathPasskeyNeverLeavesTheServer(t *testing.T) {
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			f := newGrabSecFixture(t)
			guid := f.indexerURL + "/tracker/torrent/download/12345." + pathRSSKey
			dlPath := "/tracker/download/12345/" + pathPasskey + "/One.Shot.torrent"
			f.searcher.results = []newznab.SearchResult{{
				GUID: guid, InfoURL: guid, IndexerID: f.indexerID, Title: "Lee Child - One Shot (epub)",
				NZBURL: f.indexerURL + dlPath, Protocol: "usenet",
			}}

			rec := httptest.NewRecorder()
			f.search.SearchQuery(rec, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/indexer/search?q=one+shot", nil), f.bob, role))
			if rec.Code != http.StatusOK {
				t.Fatalf("search: got %d: %s", rec.Code, rec.Body.String())
			}
			assertNoPathSecret(t, "search response", rec.Body.String())
			var results []newznab.SearchResult
			if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil || len(results) != 1 {
				t.Fatalf("decode search response: %v (%s)", err, rec.Body.String())
			}
			if !strings.Contains(results[0].NZBURL, "/tracker/download/12345/") || !strings.HasSuffix(results[0].NZBURL, "/One.Shot.torrent") {
				t.Fatalf("redaction removed more than the passkey: %q", results[0].NZBURL)
			}

			grab := f.grabAs(f.bob, role, map[string]any{
				"guid": results[0].GUID, "title": results[0].Title, "nzbUrl": results[0].NZBURL, "indexerId": f.indexerID,
			})
			if grab.Code != http.StatusAccepted {
				t.Fatalf("grab: got %d: %s", grab.Code, grab.Body.String())
			}
			assertNoPathSecret(t, "grab response", grab.Body.String())
			select {
			case p := <-f.trackerPaths:
				if p != dlPath {
					t.Errorf("the download client must be sent the real URL, got path %q want %q", p, dlPath)
				}
			default:
				t.Fatal("the recorded download URL was never fetched")
			}
			if dl, err := f.downloads.GetByGUID(f.ctx, guid); err != nil || dl == nil {
				t.Fatalf("the download row must carry the raw GUID: %v", err)
			}

			list := httptest.NewRecorder()
			f.queue.List(list, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), f.bob, role))
			if list.Code != http.StatusOK {
				t.Fatalf("queue list: got %d: %s", list.Code, list.Body.String())
			}
			if !strings.Contains(list.Body.String(), "/tracker/download/12345/") {
				t.Fatalf("queue list does not show the grabbed release: %s", list.Body.String())
			}
			assertNoPathSecret(t, "queue response", list.Body.String())
		})
	}
}

// Releases from one tracker whose GUIDs differ only in a redacted segment (an
// info hash) must stay apart: the registry and the web UI both key on the
// GUID the response shows, so a shared placeholder would send every grab to
// whichever release was recorded last.
func TestSearchAndGrab_PathRedactedGUIDsStayDistinct(t *testing.T) {
	f := newGrabSecFixture(t)
	hashes := []string{strings.Repeat("c12f", 10), strings.Repeat("d34e", 10)} // two 40 hex info hashes
	f.searcher.results = nil
	for i, h := range hashes {
		f.searcher.results = append(f.searcher.results, newznab.SearchResult{
			GUID: f.indexerURL + "/tracker/torrent/" + h, IndexerID: f.indexerID, Title: "Lee Child - One Shot " + h[:2],
			NZBURL: f.indexerURL + "/tracker/dl/" + string(rune('1'+i)) + "/" + pathPasskey, Protocol: "usenet",
		})
	}
	rec := httptest.NewRecorder()
	f.search.SearchQuery(rec, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/indexer/search?q=one+shot", nil), f.bob, auth.RoleUser))
	var results []newznab.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil || len(results) != 2 {
		t.Fatalf("decode search response: %v (%s)", err, rec.Body.String())
	}
	if results[0].GUID == results[1].GUID {
		t.Fatalf("two releases share the GUID %q", results[0].GUID)
	}
	// Grab each result by the GUID it was shown with. With a shared key one of
	// them would resolve to the release recorded last.
	for _, res := range results {
		want := "/tracker/dl/1/" + pathPasskey
		if strings.HasSuffix(res.Title, hashes[1][:2]) {
			want = "/tracker/dl/2/" + pathPasskey
		}
		grab := f.grabAs(f.bob, auth.RoleUser, map[string]any{"guid": res.GUID, "title": res.Title, "nzbUrl": res.NZBURL})
		if grab.Code != http.StatusAccepted {
			t.Fatalf("grab %q: got %d: %s", res.Title, grab.Code, grab.Body.String())
		}
		select {
		case p := <-f.trackerPaths:
			if p != want {
				t.Fatalf("grab of %q fetched %q, want %q", res.Title, p, want)
			}
		default:
			t.Fatalf("grab of %q fetched nothing", res.Title)
		}
	}
}

// History, pending and the log export show the same releases.
func TestHistoryList_RedactsPathPasskeys(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	raw := "https://tracker.example/download/12345/" + pathPasskey + "/One.Shot.torrent"
	data, _ := json.Marshal(map[string]any{"guid": raw, "message": `fetch torrent: Get "` + raw + `": EOF`})
	if err := history.Create(ctx, &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "x", Data: string(data)}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d: %s", rec.Code, rec.Body.String())
	}
	assertNoPathSecret(t, "history response", rec.Body.String())
	if !strings.Contains(rec.Body.String(), "/One.Shot.torrent") {
		t.Fatalf("redaction removed more than the passkey: %s", rec.Body.String())
	}
}

func TestRedactReleaseJSON_PathPasskeys(t *testing.T) {
	blob := `{"guid":"https://t.example/torrent/download/1.` + pathRSSKey + `","nzbUrl":"https://t.example/download/1/` + pathPasskey + `/f.torrent","title":"x"}`
	assertNoPathSecret(t, "pending blob", redactReleaseJSON(blob))
}

func TestExportValue_PathPasskeys(t *testing.T) {
	assertNoPathSecret(t, "log export", exportValue(`grab failed url=https://t.example/rss/download/1/`+pathPasskey+` err="EOF"`))
}

// A release URL with credentials in its userinfo (user:pass@host) is shown
// without them, in search, grab, queue and history, and the grab still sends
// them to the download URL from the server's record.
func TestSearchAndGrab_UserinfoNeverLeavesTheServer(t *testing.T) {
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			f := newGrabSecFixture(t)
			withCreds := strings.Replace(f.indexerURL, "://", "://feeduser:TRACKERPW@", 1)
			guid := withCreds + "/tracker/details/77"
			f.searcher.results = []newznab.SearchResult{{
				GUID: guid, InfoURL: guid, IndexerID: f.indexerID, Title: "Lee Child - One Shot (epub)",
				NZBURL: withCreds + "/tracker/dl/77/One.Shot.torrent", Protocol: "usenet",
			}}
			assertNoCreds := func(what, body string) {
				t.Helper()
				if strings.Contains(body, "TRACKERPW") || strings.Contains(body, "feeduser") {
					t.Fatalf("%s carries userinfo credentials: %s", what, body)
				}
			}

			rec := httptest.NewRecorder()
			f.search.SearchQuery(rec, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/indexer/search?q=one+shot", nil), f.bob, role))
			if rec.Code != http.StatusOK {
				t.Fatalf("search: got %d: %s", rec.Code, rec.Body.String())
			}
			assertNoCreds("search response", rec.Body.String())
			var results []newznab.SearchResult
			if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil || len(results) != 1 {
				t.Fatalf("decode search response: %v (%s)", err, rec.Body.String())
			}

			grab := f.grabAs(f.bob, role, map[string]any{
				"guid": results[0].GUID, "title": results[0].Title, "nzbUrl": results[0].NZBURL, "indexerId": f.indexerID,
			})
			if grab.Code != http.StatusAccepted {
				t.Fatalf("grab: got %d: %s", grab.Code, grab.Body.String())
			}
			assertNoCreds("grab response", grab.Body.String())
			select {
			case p := <-f.trackerPaths:
				if want := "/tracker/dl/77/One.Shot.torrent auth=feeduser:TRACKERPW"; p != want {
					t.Errorf("the download must be fetched with the real credentials, got %q want %q", p, want)
				}
			default:
				t.Fatal("the recorded download URL was never fetched")
			}

			list := httptest.NewRecorder()
			f.queue.List(list, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), f.bob, role))
			if !strings.Contains(list.Body.String(), "/tracker/dl/77/") {
				t.Fatalf("queue list does not show the grabbed release: %s", list.Body.String())
			}
			assertNoCreds("queue response", list.Body.String())
		})
	}
}

func TestHistoryList_RedactsUserinfo(t *testing.T) {
	h, history, _, ctx := historyFixture(t)
	raw := "https://feeduser:TRACKERPW@tracker.example/dl/77/One.Shot.torrent"
	data, _ := json.Marshal(map[string]any{"guid": raw, "message": `fetch torrent: Get "` + raw + `": EOF`})
	if err := history.Create(ctx, &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "x", Data: string(data)}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	if strings.Contains(rec.Body.String(), "TRACKERPW") {
		t.Fatalf("history response carries a userinfo password: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tracker.example/dl/77/One.Shot.torrent") {
		t.Fatalf("redaction removed more than the credentials: %s", rec.Body.String())
	}
	if got := exportValue(`grab failed url=` + raw); strings.Contains(got, "TRACKERPW") {
		t.Fatalf("log export carries a userinfo password: %s", got)
	}
}

// An info link is shown as a clickable link, so its path survives unless the
// info link is the download link itself.
func TestRedactSearchResult_InfoURLKeepsDetailPath(t *testing.T) {
	detail := "https://annas-archive.org/md5/" + pathPasskey
	res := newznab.SearchResult{
		GUID:    "https://tracker.example/torrent/download/1." + pathRSSKey,
		NZBURL:  "https://tracker.example/download/1/" + pathPasskey + "/f.torrent",
		InfoURL: detail + "?passkey=SECRETPK",
	}
	redactSearchResult(&res)
	if res.InfoURL != detail {
		t.Errorf("InfoURL = %q, want %q", res.InfoURL, detail)
	}
	assertNoPathSecret(t, "download URL and GUID", res.NZBURL+" "+res.GUID)

	dl := "https://tracker.example/rss/download/1/" + pathPasskey + "/f.torrent"
	res = newznab.SearchResult{GUID: "g", NZBURL: dl, InfoURL: dl}
	redactSearchResult(&res)
	assertNoPathSecret(t, "info link that is the download link", res.InfoURL)

	blob := `{"guid":"g","nzbUrl":"` + dl + `","infoUrl":"` + detail + `"}`
	got := redactReleaseJSON(blob)
	if !strings.Contains(got, `"infoUrl":"`+detail+`"`) {
		t.Errorf("pending blob info link changed: %s", got)
	}
	assertNoPathSecret(t, "pending download URL", strings.ReplaceAll(got, detail, ""))
	blob = `{"guid":"g","nzbUrl":"` + dl + `","infoUrl":"` + dl + `"}`
	assertNoPathSecret(t, "pending info link that is the download link", redactReleaseJSON(blob))
}
