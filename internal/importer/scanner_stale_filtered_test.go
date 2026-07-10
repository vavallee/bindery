package importer

// Regression tests for issue #1461: the stale-failure blocker must not treat a
// category-filtered (or degraded) torrent listing as a complete enumeration.
//
// A StateImportFailed download whose torrent was moved to another download
// directory (Transmission) or is only visible outside the polled categories
// while the unfiltered listing is unavailable (qBittorrent) is absent from the
// filtered view, but the torrent is still healthy in the client. Terminally
// blocking it with "download source no longer available" was wrong — only the
// retry-exhaustion case may block on a filtered view.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// staleFilteredFixture creates the shared DB scaffolding: a scanner, a
// download client of the given type/category pointed at srvURL, and a
// StateImportFailed download (retry budget NOT exhausted) tracking torrentID.
func staleFilteredFixture(t *testing.T, srvURL, clientType, category, torrentID string) (*Scanner, *models.DownloadClient, *db.DownloadRepo, context.Context) {
	t.Helper()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()

	dlRepo := db.NewDownloadRepo(database)
	clientRepo := db.NewDownloadClientRepo(database)
	s := NewScanner(dlRepo, clientRepo,
		db.NewBookRepo(database), db.NewAuthorRepo(database), db.NewHistoryRepo(database),
		t.TempDir(), "", "", "", "")

	host, port := scannerTestHostPort(t, srvURL)
	client := &models.DownloadClient{
		Name: "stale-" + clientType, Type: clientType,
		Host: host, Port: port, Category: category, Enabled: true,
	}
	if err := clientRepo.Create(ctx, client); err != nil {
		t.Fatal(err)
	}

	tid := torrentID
	dl := &models.Download{
		GUID:             "guid-1461-" + clientType + "-" + category,
		Title:            "Still Seeding Elsewhere",
		Status:           models.StateImportFailed,
		Protocol:         "torrent",
		TorrentID:        &tid,
		DownloadClientID: &client.ID,
	}
	if err := dlRepo.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return s, client, dlRepo, ctx
}

// TestCheckTransmissionDownloads_FilteredListDoesNotBlock is the Transmission
// half of issue #1461: with a Category configured, GetTorrents filters by
// downloadDir/label, so a torrent moved to another directory disappears from
// the filtered listing while still seeding. The poller must NOT terminally
// block the StateImportFailed download on that filtered view.
func TestCheckTransmissionDownloads_FilteredListDoesNotBlock(t *testing.T) {
	// The daemon holds the torrent, but under a directory that does not match
	// the client's Category filter — the filtered listing comes back empty.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		torrents := []map[string]any{{
			"id":          42,
			"hashString":  "h42",
			"name":        "Still Seeding Elsewhere",
			"percentDone": 1.0,
			"status":      6, // seeding
			"downloadDir": "/moved/elsewhere",
		}}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    "success",
			"arguments": map[string]any{"torrents": torrents},
		})
	}))
	defer srv.Close()

	s, client, dlRepo, ctx := staleFilteredFixture(t, srv.URL, "transmission", "/data/books", "42")

	s.checkTransmissionDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-1461-transmission-/data/books")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == models.StateImportBlocked {
		t.Fatalf("issue #1461: download was terminally blocked from a category-filtered listing while its torrent is still seeding; status = %q", got.Status)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("status = %q, want %q (unchanged, awaiting retry)", got.Status, models.StateImportFailed)
	}
}

// TestCheckTransmissionDownloads_UnfilteredListStillBlocks guards the other
// direction: with NO Category filter the listing is a complete enumeration,
// so a StateImportFailed download whose torrent is genuinely gone must still
// be terminally blocked (issue #706 finding 4 must keep working).
func TestCheckTransmissionDownloads_UnfilteredListStillBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[]}}`))
	}))
	defer srv.Close()

	s, client, dlRepo, ctx := staleFilteredFixture(t, srv.URL, "transmission", "", "42")

	s.checkTransmissionDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-1461-transmission-")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Errorf("status = %q, want %q — with an unfiltered listing a vanished source must still be terminally blocked", got.Status, models.StateImportBlocked)
	}
}

// TestCheckQbittorrentDownloads_DegradedListDoesNotBlock is the qBittorrent
// half of issue #1461: when the unfiltered listing fails, seenSourceIDs
// degrades to the category-filtered view. A torrent qBittorrent holds under a
// different category is invisible in that view while still seeding, so the
// poller must NOT terminally block its StateImportFailed download that cycle.
func TestCheckQbittorrentDownloads_DegradedListDoesNotBlock(t *testing.T) {
	const storedHash = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("category") == "" {
				// The unfiltered listing fails — recovery degraded this cycle.
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Category-filtered listing succeeds but does not contain the
			// tracked torrent (it sits under a different category).
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s, client, dlRepo, ctx := staleFilteredFixture(t, srv.URL, "qbittorrent", "books", storedHash)

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-1461-qbittorrent-books")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == models.StateImportBlocked {
		t.Fatalf("issue #1461: download was terminally blocked from a degraded (category-only) listing; status = %q", got.Status)
	}
	if got.Status != models.StateImportFailed {
		t.Errorf("status = %q, want %q (unchanged, awaiting retry)", got.Status, models.StateImportFailed)
	}
}

// TestCheckQbittorrentDownloads_CompleteListStillBlocks guards the other
// direction for qBittorrent: when the unfiltered listing succeeds the
// enumeration is complete, so a torrent absent from BOTH the category view and
// the unfiltered view is definitively gone and the StateImportFailed download
// must still be terminally blocked.
func TestCheckQbittorrentDownloads_CompleteListStillBlocks(t *testing.T) {
	const storedHash = "aaaa1111bbbb2222cccc3333dddd4444eeee5555"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[]`)) // torrent gone everywhere
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s, client, dlRepo, ctx := staleFilteredFixture(t, srv.URL, "qbittorrent", "books", storedHash)

	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-1461-qbittorrent-books")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StateImportBlocked {
		t.Errorf("status = %q, want %q — with a complete enumeration a vanished source must still be terminally blocked", got.Status, models.StateImportBlocked)
	}
}
