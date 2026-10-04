package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// A Jackett release whose download link and GUID both carry the Jackett key
// and the tracker passkey. Neither value may reach any caller, admin or not,
// in the search, grab or queue response, and a grab of what the search
// returned must still fetch the link with both real values.
func TestSearchAndGrab_JackettCredentialsNeverLeaveTheServer(t *testing.T) {
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			f := newGrabSecFixture(t)
			raw := f.indexerURL + "/dl/jackett?jackett_apikey=REALSECRET&passkey=REALSECRET&path=abc&file=One+Shot"
			f.searcher.results = []newznab.SearchResult{{
				GUID: raw, InfoURL: raw, IndexerID: f.indexerID, Title: "Lee Child - One Shot (epub)",
				NZBURL: raw, Protocol: "usenet",
			}}

			rec := httptest.NewRecorder()
			f.search.SearchQuery(rec, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/indexer/search?q=one+shot", nil), f.bob, role))
			if rec.Code != http.StatusOK {
				t.Fatalf("search: got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "REALSECRET") {
				t.Fatalf("search response carries a credential: %s", rec.Body.String())
			}
			var results []newznab.SearchResult
			if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil || len(results) != 1 {
				t.Fatalf("decode search response: %v (%s)", err, rec.Body.String())
			}

			// Grab exactly what the search handed back, as the web UI does.
			grab := f.grabAs(f.bob, role, map[string]any{
				"guid": results[0].GUID, "title": results[0].Title, "nzbUrl": results[0].NZBURL, "indexerId": f.indexerID,
			})
			if grab.Code != http.StatusAccepted {
				t.Fatalf("grab: got %d: %s", grab.Code, grab.Body.String())
			}
			if strings.Contains(grab.Body.String(), "REALSECRET") {
				t.Fatalf("grab response carries a credential: %s", grab.Body.String())
			}
			select {
			case q := <-f.jackettQueries:
				if !strings.Contains(q, "jackett_apikey=REALSECRET") || !strings.Contains(q, "passkey=REALSECRET") {
					t.Errorf("the download must be fetched with the real credentials, got query %q", q)
				}
			default:
				t.Fatal("the recorded download URL was never fetched")
			}

			// The row keeps the raw GUID, the one a scheduler grab of the same
			// release would store, so de-duplication and the blocklist agree.
			if dl, err := f.downloads.GetByGUID(f.ctx, raw); err != nil || dl == nil {
				t.Fatalf("the download row must carry the raw GUID: %v", err)
			}

			list := httptest.NewRecorder()
			f.queue.List(list, asRole(httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), f.bob, role))
			if list.Code != http.StatusOK {
				t.Fatalf("queue list: got %d: %s", list.Code, list.Body.String())
			}
			if !strings.Contains(list.Body.String(), "/dl/jackett") {
				t.Fatalf("queue list does not show the grabbed release: %s", list.Body.String())
			}
			if strings.Contains(list.Body.String(), "REALSECRET") {
				t.Fatalf("queue response carries a credential: %s", list.Body.String())
			}
		})
	}
}

// The registry is keyed by the GUID as responses show it, so a grab posting
// the redacted GUID and one posting the raw GUID (an API client that read it
// from the indexer) find the same entry, and either gets the raw GUID back.
func TestSearchResultRegistry_FindsRedactedAndRawGUID(t *testing.T) {
	raw := "https://tracker.example/details.php?id=9&passkey=REALSECRET"
	reg := NewSearchResultRegistry()
	reg.remember([]newznab.SearchResult{{GUID: raw, NZBURL: raw, Title: "t"}})
	for _, posted := range []string{raw, newznab.RedactDownloadURL(raw)} {
		e, ok := reg.lookup(posted)
		if !ok {
			t.Fatalf("lookup(%q) missed", posted)
		}
		var req grabRequest
		e.apply(&req)
		if req.GUID != raw || req.NZBURL != raw {
			t.Fatalf("apply gave guid %q url %q, want the raw values", req.GUID, req.NZBURL)
		}
	}
}

// Pending responses strip credentials from the row GUID and from every URL
// field of the stored release.
func TestRedactReleaseJSON_StripsEveryURLField(t *testing.T) {
	blob := `{"guid":"https://t.example/d?id=1&passkey=REALSECRET","nzbUrl":"https://t.example/dl?id=1&passkey=REALSECRET","infoUrl":"https://t.example/i?id=1&torrent_pass=REALSECRET","title":"x"}`
	got := redactReleaseJSON(blob)
	if strings.Contains(got, "REALSECRET") {
		t.Fatalf("pending blob carries a credential: %s", got)
	}
	if !strings.Contains(got, `"title":"x"`) {
		t.Fatalf("other fields lost: %s", got)
	}
}

// A browser admin's grab of a GUID the registry no longer holds (a restart, an
// eviction) used to fall back to the posted URL, which search responses now
// strip: the grab went out without its Jackett key or passkey and stored the
// redacted GUID. Only an API key caller, which holds its own raw URL, may post
// one.
func TestQueueGrab_RegistryMissIsExpiredForSessions(t *testing.T) {
	f := newGrabSecFixture(t)
	stripped := f.indexerURL + "/dl/jackett?path=abc&file=One+Shot"
	for _, role := range []string{auth.RoleAdmin, auth.RoleUser} {
		rec := f.grabAs(f.alice, role, map[string]any{"guid": "guid-not-recorded", "title": "x", "nzbUrl": stripped})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s grab of an unrecorded GUID: got %d: %s", role, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "search again") {
			t.Fatalf("%s: error should tell the user to search again: %s", role, rec.Body.String())
		}
	}
	select {
	case q := <-f.jackettQueries:
		t.Fatalf("nothing should have been fetched, got %q", q)
	default:
	}
}

func TestQueueGrab_APIKeyCallerMayPostAURL(t *testing.T) {
	f := newGrabSecFixture(t)
	b, _ := json.Marshal(map[string]any{"guid": "guid-api-key", "title": "x", "nzbUrl": f.downloadURL})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", strings.NewReader(string(b)))
	ctx := auth.WithAPIKeyAuth(auth.WithUserRole(auth.WithUserID(req.Context(), f.alice), auth.RoleAdmin))
	rec := httptest.NewRecorder()
	f.queue.Grab(rec, req.WithContext(ctx))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("API key grab of a posted URL: got %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.downloadHits.Load(); n != 1 {
		t.Errorf("expected the posted URL to be fetched once, got %d", n)
	}
}
