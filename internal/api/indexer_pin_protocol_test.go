package api

import (
	"net/http"
	"strconv"
	"testing"
)

// TestIndexerPin_UnrelatedUpdateWithWrongProtocolPinStillSaves: a client whose
// type was changed to the other protocol after the pin must not block enabling,
// disabling or re-prioritising the indexer. Only a change to the pin or the
// indexer type is held to the protocol rule. Grabs still fail loudly.
func TestIndexerPin_UnrelatedUpdateWithWrongProtocolPinStillSaves(t *testing.T) {
	f := newPinAPIFixture(t)
	torrentID := strconv.FormatInt(f.torrent.ID, 10)
	created := decodeIndexer(t, f.create(`{"name":"Priv","url":"http://192.168.1.50:9117/api","type":"torznab","downloadClientId":`+torrentID+`}`))
	f.torrent.Type = "sabnzbd"
	if err := f.clients.Update(t.Context(), f.torrent); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{`{"enabled":false}`, `{"priority":5}`, `{"name":"Renamed","downloadClientId":` + torrentID + `}`} {
		rec := f.update(created.ID, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("update %s: %d %s", body, rec.Code, rec.Body.String())
		}
		if got := decodeIndexer(t, rec); got.DownloadClientID == nil || *got.DownloadClientID != f.torrent.ID {
			t.Errorf("update %s lost the pin: %v", body, got.DownloadClientID)
		}
	}
	// Changing the indexer type is a change the rule applies to.
	if rec := f.update(created.ID, `{"type":"newznab"}`); rec.Code != http.StatusOK {
		t.Fatalf("type change to the client's own protocol: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.update(created.ID, `{"type":"torznab"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("type change away from the client's protocol: %d, want 400: %s", rec.Code, rec.Body.String())
	}
	// A new pin to a wrong protocol client is a change too.
	other := decodeIndexer(t, f.create(`{"name":"Other","url":"http://192.168.1.51:9117/api","type":"torznab"}`))
	if rec := f.update(other.ID, `{"downloadClientId":`+torrentID+`}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("new wrong protocol pin: %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
