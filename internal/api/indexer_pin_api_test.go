package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// pinAPIFixture is an indexer handler and a download client handler over one
// database, with a usenet and a torrent client to pin against.
type pinAPIFixture struct {
	ih      *IndexerHandler
	ch      *DownloadClientHandler
	clients *db.DownloadClientRepo
	usenet  *models.DownloadClient
	torrent *models.DownloadClient
	off     *models.DownloadClient
}

func newPinAPIFixture(t *testing.T) *pinAPIFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	clients := db.NewDownloadClientRepo(database)
	indexers := db.NewIndexerRepo(database)
	ih := NewIndexerHandler(indexers, db.NewBookRepo(database), db.NewAuthorRepo(database),
		db.NewMetadataProfileRepo(database), nil, db.NewSettingsRepo(database), db.NewBlocklistRepo(database)).
		WithDownloadClients(clients)
	ch := NewDownloadClientHandler(clients)
	ctx := t.Context()
	f := &pinAPIFixture{ih: ih, ch: ch, clients: clients,
		usenet:  &models.DownloadClient{Name: "sab", Type: "sabnzbd", Host: "192.168.1.70", Port: 8080, Enabled: true},
		torrent: &models.DownloadClient{Name: "qbit-private", Type: "qbittorrent", Host: "192.168.1.71", Port: 8081, Enabled: true},
		off:     &models.DownloadClient{Name: "qbit-off", Type: "qbittorrent", Host: "192.168.1.72", Port: 8082, Enabled: false},
	}
	for _, c := range []*models.DownloadClient{f.usenet, f.torrent, f.off} {
		if err := clients.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *pinAPIFixture) create(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.ih.Create(rec, httptest.NewRequest(http.MethodPost, "/indexer", bytes.NewBufferString(body)))
	return rec
}

func (f *pinAPIFixture) update(id int64, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	idStr := strconv.FormatInt(id, 10)
	f.ih.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/indexer/"+idStr, bytes.NewBufferString(body)), "id", idStr))
	return rec
}

func decodeIndexer(t *testing.T, rec *httptest.ResponseRecorder) models.Indexer {
	t.Helper()
	var out models.Indexer
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestIndexerPin_CreateAndUpdateValidateAndPersist(t *testing.T) {
	f := newPinAPIFixture(t)
	torrentID := strconv.FormatInt(f.torrent.ID, 10)

	// A torznab indexer pinned to a torrent client saves and echoes the pin.
	rec := f.create(`{"name":"Priv","url":"http://192.168.1.50:9117/api","apiKey":"k","type":"torznab","downloadClientId":` + torrentID + `}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	created := decodeIndexer(t, rec)
	if created.DownloadClientID == nil || *created.DownloadClientID != f.torrent.ID {
		t.Fatalf("created pin = %v, want %d", created.DownloadClientID, f.torrent.ID)
	}

	// An update that omits the key keeps the pin.
	rec = f.update(created.ID, `{"name":"Priv renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeIndexer(t, rec); got.DownloadClientID == nil || *got.DownloadClientID != f.torrent.ID {
		t.Errorf("an update that omitted the pin changed it: %v", got.DownloadClientID)
	}

	// An explicit null clears it.
	rec = f.update(created.ID, `{"downloadClientId":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeIndexer(t, rec); got.DownloadClientID != nil {
		t.Errorf("null did not clear the pin: %v", *got.DownloadClientID)
	}

	// And it can be set again on update.
	rec = f.update(created.ID, `{"downloadClientId":`+torrentID+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeIndexer(t, rec); got.DownloadClientID == nil || *got.DownloadClientID != f.torrent.ID {
		t.Errorf("set pin = %v", got.DownloadClientID)
	}
}

func TestIndexerPin_RejectedOnSave(t *testing.T) {
	f := newPinAPIFixture(t)
	usenetID := strconv.FormatInt(f.usenet.ID, 10)
	offID := strconv.FormatInt(f.off.ID, 10)

	cases := []struct {
		name string
		body string
		want []string
	}{
		{"protocol mismatch, torznab to a usenet client", `{"name":"A","url":"http://192.168.1.50:9117/api","type":"torznab","downloadClientId":` + usenetID + `}`, []string{"usenet", "torrent", "sab"}},
		{"unknown client", `{"name":"B","url":"http://192.168.1.51:9117/api","type":"newznab","downloadClientId":9999}`, []string{"does not exist"}},
		{"zero is not a client", `{"name":"C","url":"http://192.168.1.52:9117/api","type":"newznab","downloadClientId":0}`, []string{"does not exist"}},
		{"new pin to a disabled client", `{"name":"D","url":"http://192.168.1.53:9117/api","type":"torznab","downloadClientId":` + offID + `}`, []string{"disabled", "qbit-off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.create(tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("create: status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			for _, w := range tc.want {
				if !strings.Contains(rec.Body.String(), w) {
					t.Errorf("message %q does not mention %q", rec.Body.String(), w)
				}
			}
		})
	}

	// The same checks run on update, and a rejected update changes nothing.
	rec := f.create(`{"name":"Plain","url":"http://192.168.1.60:9117/api","type":"torznab"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", rec.Code, rec.Body.String())
	}
	plain := decodeIndexer(t, rec)
	if rec := f.update(plain.ID, `{"downloadClientId":`+usenetID+`}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("update with a usenet client on a torznab indexer: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.ih.indexers.GetByID(t.Context(), plain.ID)
	if got.DownloadClientID != nil {
		t.Errorf("a rejected update still stored a pin: %d", *got.DownloadClientID)
	}
}

// TestIndexerPin_UnrelatedEditKeepsPinToDisabledClient: disabling a client is
// allowed, so an edit to some other field of the indexer must still save.
func TestIndexerPin_UnrelatedEditKeepsPinToDisabledClient(t *testing.T) {
	f := newPinAPIFixture(t)
	torrentID := strconv.FormatInt(f.torrent.ID, 10)
	created := decodeIndexer(t, f.create(`{"name":"Priv","url":"http://192.168.1.50:9117/api","type":"torznab","downloadClientId":`+torrentID+`}`))
	f.torrent.Enabled = false
	if err := f.clients.Update(t.Context(), f.torrent); err != nil {
		t.Fatal(err)
	}
	rec := f.update(created.ID, `{"name":"Priv renamed","downloadClientId":`+torrentID+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeIndexer(t, rec); got.DownloadClientID == nil || *got.DownloadClientID != f.torrent.ID {
		t.Errorf("pin lost: %v", got.DownloadClientID)
	}
}

func (f *pinAPIFixture) deleteClient(id int64) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	idStr := strconv.FormatInt(id, 10)
	f.ch.Delete(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/downloadclient/"+idStr, nil), "id", idStr))
	return rec
}

// TestDownloadClientDelete_RefusedWhileAnIndexerIsPinned: 409 naming every
// pinned indexer, and the client survives. Unpinning lets the delete through.
func TestDownloadClientDelete_RefusedWhileAnIndexerIsPinned(t *testing.T) {
	f := newPinAPIFixture(t)
	torrentID := strconv.FormatInt(f.torrent.ID, 10)
	a := decodeIndexer(t, f.create(`{"name":"TrackerA","url":"http://192.168.1.50:9117/api","type":"torznab","downloadClientId":`+torrentID+`}`))
	b := decodeIndexer(t, f.create(`{"name":"TrackerB","url":"http://192.168.1.51:9117/api","type":"torznab","downloadClientId":`+torrentID+`}`))
	decodeIndexer(t, f.create(`{"name":"Unpinned","url":"http://192.168.1.52:9117/api","type":"torznab"}`))

	rec := f.deleteClient(f.torrent.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete: status %d, want 409: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"TrackerA", "TrackerB"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("409 message %q does not list %q", rec.Body.String(), want)
		}
	}
	if strings.Contains(rec.Body.String(), "Unpinned") {
		t.Errorf("409 message lists an indexer that is not pinned: %s", rec.Body.String())
	}
	if c, _ := f.clients.GetByID(t.Context(), f.torrent.ID); c == nil {
		t.Fatal("the client was deleted despite the refusal")
	}

	// Unpin both, then the delete goes through.
	for _, id := range []int64{a.ID, b.ID} {
		if rec := f.update(id, `{"downloadClientId":null}`); rec.Code != http.StatusOK {
			t.Fatalf("unpin %d: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	if rec := f.deleteClient(f.torrent.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("delete after unpinning: %d %s", rec.Code, rec.Body.String())
	}
	// A client no indexer uses was never in the way.
	if rec := f.deleteClient(f.usenet.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("delete of an unused client: %d %s", rec.Code, rec.Body.String())
	}
}

// TestDownloadClientDisable_StaysAllowedWhilePinned: disabling is not refused.
func TestDownloadClientDisable_StaysAllowedWhilePinned(t *testing.T) {
	f := newPinAPIFixture(t)
	torrentID := strconv.FormatInt(f.torrent.ID, 10)
	decodeIndexer(t, f.create(`{"name":"TrackerA","url":"http://192.168.1.50:9117/api","type":"torznab","downloadClientId":`+torrentID+`}`))
	rec := httptest.NewRecorder()
	f.ch.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/downloadclient/"+torrentID, bytes.NewBufferString(`{"enabled":false}`)), "id", torrentID))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
}
