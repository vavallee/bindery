package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// GET /history is open to every user, and grab, import and failure events
// store the release GUID raw. A torznab GUID is often the download URL with
// the Jackett key and passkey in it.
func TestHistoryList_RedactsCredentialsInEventData(t *testing.T) {
	h, history, blocklist, ctx := historyFixture(t)
	raw := "http://jackett:9117/dl/tracker/?jackett_apikey=REALSECRET&passkey=REALSECRET&path=abc"
	data, _ := json.Marshal(map[string]any{
		"guid": raw, "nzbUrl": raw, "message": "fetch torrent: Get \"" + raw + "\": EOF", "size": 12,
	})
	e := &models.HistoryEvent{EventType: models.HistoryEventGrabbed, SourceTitle: "Dune", Data: string(data)}
	if err := history.Create(ctx, e); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "REALSECRET") {
		t.Fatalf("history response carries a credential: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/dl/tracker/") || !strings.Contains(rec.Body.String(), `\"size\":12`) {
		t.Fatalf("redaction removed more than the credentials: %s", rec.Body.String())
	}

	// Blocklisting from history reads the stored row, so the entry keeps the
	// raw GUID that search results are compared against; the response does not.
	id := strconv.FormatInt(e.ID, 10)
	brec := httptest.NewRecorder()
	h.Blocklist(brec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/history/"+id+"/blocklist", nil), "id", id))
	if brec.Code != http.StatusCreated {
		t.Fatalf("blocklist: got %d: %s", brec.Code, brec.Body.String())
	}
	if strings.Contains(brec.Body.String(), "REALSECRET") {
		t.Fatalf("blocklist response carries a credential: %s", brec.Body.String())
	}
	entries, err := blocklist.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].GUID != raw {
		t.Fatalf("blocklist entry must keep the raw GUID, got %+v, %v", entries, err)
	}
}
