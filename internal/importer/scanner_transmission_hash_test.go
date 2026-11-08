package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// transmissionListHandler serves a torrent-get listing holding the given
// torrents, which is all checkTransmissionDownloads asks of a daemon.
func transmissionListHandler(t *testing.T, torrents []map[string]any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":    "success",
			"arguments": map[string]any{"torrents": torrents},
		})
	}
}

// category mirrors a real deployment: Transmission has no categories, so
// Bindery's Category doubles as a downloadDir filter. It also keeps
// failDownloadThatNeverArrived out of these tests — a filtered listing is not
// a complete source list, so absence from it is not treated as definitive.
func transmissionClientFixture(t *testing.T, s *Scanner, ctx context.Context, srv *httptest.Server, category string) *models.DownloadClient {
	t.Helper()
	host, port := scannerTestHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Name:     "transmission",
		Type:     "transmission",
		Host:     host,
		Port:     port,
		Category: category,
		Enabled:  true,
	}
	if err := s.clients.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

// TestCheckTransmissionDownloads_RecoversRenumberedTorrentID is the regression
// test for downloads stranded by a Transmission restart. The daemon renumbers
// every torrent when it restarts, so a download grabbed as id 17 comes back as
// id 1 and the poller used to lose it forever — it sat at "downloading" while
// the torrent seeded on, never imported and (with remove_on_import) never
// removed. The row must be recovered via addedDate and rewritten to the hash.
func TestCheckTransmissionDownloads_RecoversRenumberedTorrentID(t *testing.T) {
	grabbedAt := time.Now().Add(-48 * time.Hour).UTC()
	const hash = "0123456789abcdef0123456789abcdef01234567"

	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		// Same torrent, renumbered from 17 to 1 by a daemon restart.
		"id":          1,
		"hashString":  hash,
		"name":        "Stranded Release",
		"status":      0,
		"percentDone": 0.5,
		"downloadDir": "/downloads",
		"addedDate":   grabbedAt.Unix(),
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	staleID := "17"
	dl := &models.Download{
		GUID:             "guid-renumbered",
		DownloadClientID: &client.ID,
		Title:            "Stranded Release",
		NZBURL:           "magnet:?xt=urn:btih:0123",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &staleID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := s.downloads.SetGrabbedAt(ctx, dl.ID, grabbedAt); err != nil {
		t.Fatalf("set grabbed_at: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != hash {
		t.Fatalf("expected torrent id to be rewritten to the info hash %q, got %q", hash, derefTorrentID(got.TorrentID))
	}
}

// TestCheckTransmissionDownloads_IgnoresRecycledTorrentID guards the other
// direction: a stale numeric id that now belongs to an unrelated torrent must
// not be matched. Acting on that match would mark the wrong download imported
// and, with remove_on_import enabled, delete a torrent Bindery never grabbed.
func TestCheckTransmissionDownloads_IgnoresRecycledTorrentID(t *testing.T) {
	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		// Holds the id our download remembers, but was added a year later:
		// a different torrent that inherited the number.
		"id":          17,
		"hashString":  "ffffffffffffffffffffffffffffffffffffffff",
		"name":        "Somebody Else's Torrent",
		"status":      6,
		"percentDone": 1.0,
		"downloadDir": "/downloads",
		"addedDate":   time.Now().Unix(),
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	staleID := "17"
	dl := &models.Download{
		GUID:             "guid-recycled",
		DownloadClientID: &client.ID,
		Title:            "Our Release",
		NZBURL:           "magnet:?xt=urn:btih:dead",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &staleID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := s.downloads.SetGrabbedAt(ctx, dl.ID, time.Now().Add(-365*24*time.Hour).UTC()); err != nil {
		t.Fatalf("set grabbed_at: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != staleID {
		t.Fatalf("expected the stale id %q to be left alone, got %v", staleID, got.TorrentID)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Fatalf("expected status to remain downloading, got %q", got.Status)
	}
}

func TestReleaseNamesMatch(t *testing.T) {
	cases := []struct {
		name, torrent, title string
		want                 bool
	}{
		{"plus separated", "The+Lantern+Makers+Daughter+by+E.+Vance+EPUB", "The Lantern Makers Daughter by E. Vance EPUB", true},
		{"dot separated", "Some.Release.Name.EPUB", "Some Release Name EPUB", true},
		{"case insensitive", "SOME RELEASE", "some release", true},
		{"different release", "Some Other Book EPUB", "The Lantern Makers Daughter EPUB", false},
		{"empty torrent name", "", "The Lantern Makers Daughter", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseNamesMatch(tc.torrent, tc.title); got != tc.want {
				t.Fatalf("releaseNamesMatch(%q, %q) = %v, want %v", tc.torrent, tc.title, got, tc.want)
			}
		})
	}
}

// TestCheckTransmissionDownloads_LegacyBatchDoesNotCollapseOntoOneTorrent is a
// regression test for a live incident: a batch of downloads grabbed within
// minutes of each other all fell inside the addedDate window of the single
// torrent still present, so each one "recovered" onto it and had its id
// rewritten to that torrent's hash. A torrent belongs to one download — the
// first claim wins and the rest must be left alone rather than guessed at.
// Terminal downloads are not reconciled at all: they will never be imported or
// removed again, so rewriting their identifier can only ever be wrong.
func TestCheckTransmissionDownloads_LegacyBatchDoesNotCollapseOntoOneTorrent(t *testing.T) {
	grabbedAt := time.Now().Add(-72 * time.Hour).UTC()
	const hash = "89abcdef0123456789abcdef0123456789abcdef"

	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		"id":          1,
		"hashString":  hash,
		"name":        "The+Lantern+Makers+Daughter+by+E.+Vance+EPUB",
		"status":      0,
		"percentDone": 0.0,
		"downloadDir": "/downloads",
		"addedDate":   grabbedAt.Unix(),
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	// Three downloads grabbed inside the same window as the surviving torrent:
	// the one that is really it, a sibling still in flight, and one already
	// imported whose torrent is long gone.
	mk := func(guid, title, torrentID string, status models.DownloadState, offset time.Duration) *models.Download {
		id := torrentID
		dl := &models.Download{
			GUID:             guid,
			DownloadClientID: &client.ID,
			Title:            title,
			NZBURL:           "magnet:?xt=urn:btih:" + torrentID,
			Status:           status,
			Protocol:         "torrent",
			TorrentID:        &id,
		}
		if err := s.downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create download %s: %v", guid, err)
		}
		if err := s.downloads.SetGrabbedAt(ctx, dl.ID, grabbedAt.Add(offset)); err != nil {
			t.Fatalf("set grabbed_at: %v", err)
		}
		return dl
	}
	real := mk("guid-real", "The Lantern Makers Daughter by E. Vance EPUB", "17", models.DownloadStatusDownloading, 0)
	sibling := mk("guid-sibling", "A Second Unrelated Book EPUB", "18", models.DownloadStatusDownloading, 90*time.Second)
	done := mk("guid-imported", "A Third Unrelated Book EPUB", "20", models.StateImported, 30*time.Second)

	s.checkTransmissionDownloads(ctx, client)

	assertTorrentID := func(dl *models.Download, want, what string) {
		t.Helper()
		got, err := s.downloads.GetByGUID(ctx, dl.GUID)
		if err != nil {
			t.Fatalf("get by guid: %v", err)
		}
		if got.TorrentID == nil {
			t.Fatalf("%s: expected torrent id %q, got nil", what, want)
		}
		if *got.TorrentID != want {
			t.Fatalf("%s: expected torrent id %q, got %q", what, want, *got.TorrentID)
		}
	}
	// The name match resolves the ambiguous window in favour of the real one.
	assertTorrentID(real, hash, "the download that really is this torrent")
	assertTorrentID(sibling, "18", "a sibling grabbed in the same window")
	assertTorrentID(done, "20", "an already-imported download")
}

// TestCheckTransmissionDownloads_LegacyRowWithTwoNearbyTorrentsIsLeftAlone
// covers ambiguity on the download's side. One legacy download, and two
// torrents added inside its window: its own, and an unrelated one another tool
// dropped into a shared daemon in the same minute. Each torrent sees exactly
// one candidate download, so a torrent-side check alone pairs the row with
// whichever torrent the daemon lists first. Here that is the unrelated one,
// which would import the wrong payload and hand its hash to every later
// removal. With neither name matching the title the row must be left alone.
func TestCheckTransmissionDownloads_LegacyRowWithTwoNearbyTorrentsIsLeftAlone(t *testing.T) {
	grabbedAt := time.Now().Add(-24 * time.Hour).UTC()
	const (
		unrelated = "1111111111111111111111111111111111111111"
		ours      = "2222222222222222222222222222222222222222"
	)
	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{
		{
			"id": 1, "hashString": unrelated, "name": "Some.Show.S01E01.1080p",
			"status": 6, "percentDone": 1.0, "downloadDir": "/downloads",
			"addedDate": grabbedAt.Add(40 * time.Second).Unix(),
		},
		{
			"id": 2, "hashString": ours, "name": "vance_lantern_daughter",
			"status": 4, "percentDone": 0.3, "downloadDir": "/downloads",
			"addedDate": grabbedAt.Unix(),
		},
	}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	staleID := "17"
	dl := &models.Download{
		GUID:             "guid-two-nearby",
		DownloadClientID: &client.ID,
		Title:            "The Lantern Makers Daughter by E. Vance EPUB",
		NZBURL:           "magnet:?xt=urn:btih:2222",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &staleID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := s.downloads.SetGrabbedAt(ctx, dl.ID, grabbedAt); err != nil {
		t.Fatalf("set grabbed_at: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != staleID {
		t.Fatalf("expected the ambiguous legacy row to keep %q, got %q", staleID, derefTorrentID(got.TorrentID))
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Fatalf("expected status to remain downloading, got %q", got.Status)
	}
}

// TestCheckTransmissionDownloads_LegacyRowNameSettlesTwoNearbyTorrents is the
// companion: when the release name picks out one of the nearby torrents, the
// row is recovered onto that one regardless of listing order.
func TestCheckTransmissionDownloads_LegacyRowNameSettlesTwoNearbyTorrents(t *testing.T) {
	grabbedAt := time.Now().Add(-24 * time.Hour).UTC()
	const (
		unrelated = "3333333333333333333333333333333333333333"
		ours      = "4444444444444444444444444444444444444444"
	)
	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{
		{
			"id": 1, "hashString": unrelated, "name": "Some.Show.S01E01.1080p",
			"status": 4, "percentDone": 0.1, "downloadDir": "/downloads",
			"addedDate": grabbedAt.Add(40 * time.Second).Unix(),
		},
		{
			"id": 2, "hashString": ours, "name": "The.Lantern.Makers.Daughter.by.E.Vance.EPUB",
			"status": 4, "percentDone": 0.3, "downloadDir": "/downloads",
			"addedDate": grabbedAt.Unix(),
		},
	}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	staleID := "17"
	dl := &models.Download{
		GUID:             "guid-two-nearby-named",
		DownloadClientID: &client.ID,
		Title:            "The Lantern Makers Daughter by E Vance EPUB",
		NZBURL:           "magnet:?xt=urn:btih:4444",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &staleID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := s.downloads.SetGrabbedAt(ctx, dl.ID, grabbedAt); err != nil {
		t.Fatalf("set grabbed_at: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != ours {
		t.Fatalf("expected the row to be recovered onto %q, got %q", ours, derefTorrentID(got.TorrentID))
	}
}

func derefTorrentID(id *string) string {
	if id == nil {
		return "<nil>"
	}
	return *id
}

// TestCheckTransmissionDownloads_TerminalLegacyRowDoesNotBlockRecovery pins
// the terminal skip in the legacy reconciler. An imported legacy row and a
// live one were grabbed a minute apart, only the live one's torrent is still
// in the daemon, and neither title matches the torrent name. Counting the
// imported row makes the window ambiguous, so the live download would stay
// stranded; it must be recovered, and the imported row must keep its id.
func TestCheckTransmissionDownloads_TerminalLegacyRowDoesNotBlockRecovery(t *testing.T) {
	grabbedAt := time.Now().Add(-24 * time.Hour).UTC()
	const hash = "5555555555555555555555555555555555555555"
	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		"id": 1, "hashString": hash, "name": "vance_lantern_daughter",
		"status": 4, "percentDone": 0.3, "downloadDir": "/downloads",
		"addedDate": grabbedAt.Add(60 * time.Second).Unix(),
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/downloads")

	mk := func(guid, title, torrentID string, status models.DownloadState, at time.Time) *models.Download {
		id := torrentID
		dl := &models.Download{
			GUID:             guid,
			DownloadClientID: &client.ID,
			Title:            title,
			NZBURL:           "magnet:?xt=urn:btih:" + torrentID,
			Status:           status,
			Protocol:         "torrent",
			TorrentID:        &id,
		}
		if err := s.downloads.Create(ctx, dl); err != nil {
			t.Fatalf("create download %s: %v", guid, err)
		}
		if err := s.downloads.SetGrabbedAt(ctx, dl.ID, at); err != nil {
			t.Fatalf("set grabbed_at: %v", err)
		}
		return dl
	}
	done := mk("guid-terminal-done", "An Older Book EPUB", "20", models.StateImported, grabbedAt)
	live := mk("guid-terminal-live", "The Lantern Makers Daughter EPUB", "21", models.DownloadStatusDownloading, grabbedAt.Add(60*time.Second))

	s.checkTransmissionDownloads(ctx, client)

	gotLive, err := s.downloads.GetByGUID(ctx, live.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got := derefTorrentID(gotLive.TorrentID); got != hash {
		t.Fatalf("expected the live download to be recovered onto %q, got %q", hash, got)
	}
	gotDone, err := s.downloads.GetByGUID(ctx, done.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got := derefTorrentID(gotDone.TorrentID); got != "20" {
		t.Fatalf("expected the imported download to keep %q, got %q", "20", got)
	}
}

// TestCheckTransmissionDownloads_TorrentInBothCategoriesCountedOnce guards the
// dedupe in the single fetch (#3714). The torrent sits in the audiobook
// directory and carries the ebook label, so both categories match it. Listed
// twice, it would be two candidates for the legacy row below, and with names
// that do not settle it the row would be left stranded instead of recovered.
func TestCheckTransmissionDownloads_TorrentInBothCategoriesCountedOnce(t *testing.T) {
	grabbedAt := time.Now().Add(-48 * time.Hour).UTC()
	const hash = "89abcdef0123456789abcdef0123456789abcdef"

	srv := httptest.NewServer(transmissionListHandler(t, []map[string]any{{
		"id":          1,
		"hashString":  hash,
		"name":        "Completely Unrelated Name",
		"status":      0,
		"percentDone": 0.5,
		"downloadDir": "/tr/audio",
		"labels":      []string{"/tr"},
		"addedDate":   grabbedAt.Unix(),
	}}))
	defer srv.Close()

	s, _, _, ctx := scannerFixture(t, t.TempDir())
	client := transmissionClientFixture(t, s, ctx, srv, "/tr")
	client.CategoryAudiobook = "/tr/audio"

	staleID := "17"
	dl := &models.Download{
		GUID:             "guid-both-categories",
		DownloadClientID: &client.ID,
		Title:            "Stranded Release",
		NZBURL:           "magnet:?xt=urn:btih:89ab",
		Status:           models.DownloadStatusDownloading,
		Protocol:         "torrent",
		TorrentID:        &staleID,
	}
	if err := s.downloads.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if err := s.downloads.SetGrabbedAt(ctx, dl.ID, grabbedAt); err != nil {
		t.Fatalf("set grabbed_at: %v", err)
	}

	s.checkTransmissionDownloads(ctx, client)

	got, err := s.downloads.GetByGUID(ctx, dl.GUID)
	if err != nil {
		t.Fatalf("get by guid: %v", err)
	}
	if got.TorrentID == nil || *got.TorrentID != hash {
		t.Fatalf("torrent matched by both categories should be one candidate and recover the row; torrent id = %q, want %q", derefTorrentID(got.TorrentID), hash)
	}
}

// TestCheckTransmissionDownloads_OneFetchPerPoll pins the importer poll to a
// single torrent-get (#3714). It used to fetch the whole list once per
// category and, when nothing matched, once more unfiltered for the
// misconfiguration warning, although every fetch returns the same list.
func TestCheckTransmissionDownloads_OneFetchPerPoll(t *testing.T) {
	cases := []struct {
		name     string
		torrents []map[string]any
	}{
		{"matched", []map[string]any{
			{"id": 1, "hashString": "aaa1", "downloadDir": "/tr"},
			{"id": 2, "hashString": "aaa2", "downloadDir": "/tr/audio"},
		}},
		{"nothing matched", []map[string]any{
			{"id": 3, "hashString": "aaa3", "downloadDir": "/elsewhere"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			list := transmissionListHandler(t, tc.torrents)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				list(w, r)
			}))
			defer srv.Close()

			s, _, _, ctx := scannerFixture(t, t.TempDir())
			client := transmissionClientFixture(t, s, ctx, srv, "/tr")
			client.CategoryAudiobook = "/tr/audio"

			s.checkTransmissionDownloads(ctx, client)

			if calls != 1 {
				t.Fatalf("torrent-get calls = %d, want 1 per poll", calls)
			}
		})
	}
}
