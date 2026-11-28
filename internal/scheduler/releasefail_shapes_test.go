package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/releasefail"
)

// The shapes #4111 names (the third, a torrent that never reaches the
// client, is judged at poll time: TestNeverArrived_BlocklistsAfterRepeatedFailures
// in internal/importer), each driven through searchAndGrabFormat with two
// releases of the book on ONE indexer, which is the reporter's case: the
// first ranked release fails in the given way every time, the second is a
// usenet release the SABnzbd stub accepts. What matters to the user is not
// only that the failing release is blocklisted but that the search then moves
// on to the other one instead of retrying the dead release every cooldown.

const shapesAltGUID = "g-4111-alt"

// minimalTorrent is a bencoded single file torrent, enough for the
// qBittorrent adapter to accept the indexer's answer as torrent content.
const minimalTorrent = "d4:infod6:lengthi1e4:name1:a12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"

type shapesFixture struct {
	*regrabFixture
	indexerID int64
}

// shapesFixture wires a qBittorrent stub next to the fixture's SABnzbd stub.
// indexerStatus is what the indexer answers for the torrent's link (200 serves
// minimalTorrent); qbAdd is the body qBittorrent answers the add with.
func newShapesFixture(t *testing.T, indexerStatus int, qbAdd string) *shapesFixture {
	t.Helper()
	f := newRegrabFixture(t)
	f.s.history = db.NewHistoryRepo(f.database)
	ctx := context.Background()

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".nzb") {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><nzb></nzb>`))
			return
		}
		if indexerStatus != http.StatusOK {
			http.Error(w, "Download failed", indexerStatus)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write([]byte(minimalTorrent))
	}))
	t.Cleanup(indexerSrv.Close)
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/torrents/add":
			_, _ = w.Write([]byte(qbAdd))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	}))
	t.Cleanup(qb.Close)
	u, err := url.Parse(qb.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.clients.Create(ctx, &models.DownloadClient{
		Name: "qb", Type: "qbittorrent", Host: u.Hostname(), Port: port, Enabled: true,
		EnabledForBooks: true, EnabledForAudiobooks: true,
	}); err != nil {
		t.Fatal(err)
	}
	indexers, _ := f.s.indexers.List(ctx)
	idxID := indexers[0].ID
	seeders := 20
	f.s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{
		{
			GUID: regrabGUID, Title: "Regrab Book.epub", NZBURL: indexerSrv.URL + "/dl.torrent",
			Protocol: "torrent", IndexerID: &idxID, Seeders: &seeders,
		},
		{
			GUID: shapesAltGUID, Title: "Regrab Book retail.epub", NZBURL: indexerSrv.URL + "/alt.nzb",
			Protocol: "usenet", IndexerID: &idxID,
		},
	}}
	return &shapesFixture{regrabFixture: f, indexerID: idxID}
}

// sweepUntilBlocklisted runs a sweep per cooldown, the way the scheduler
// does, with the indexer serving other releases in between, and stops once
// the failing release is blocklisted or the alternative has been sent. It
// returns how many sweeps failed the first release.
func (f *shapesFixture) sweepUntilBlocklisted(t *testing.T, maxSweeps int) int {
	t.Helper()
	ctx := context.Background()
	serveOther(t, f.regrabFixture, f.indexerID)
	failures := 0
	for i := 0; i < maxSweeps; i++ {
		f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
		if f.adds.Load() > 0 {
			return failures
		}
		row, err := f.downloads.GetByGUID(ctx, regrabGUID)
		if err != nil || row == nil {
			t.Fatalf("sweep %d did not try the first release: %v", i+1, err)
		}
		if row.Status == models.StateFailed {
			failures++
		}
		f.backdate(t, row.ID, "dead_at", time.Now().Add(-7*time.Hour))
		serveOther(t, f.regrabFixture, f.indexerID)
		clearPause(f.regrabFixture)
		ageHistory(t, f.regrabFixture, 7*time.Hour)
	}
	return failures
}

// assertMovesOn checks the release is blocklisted and that the search sent
// the other release instead of retrying the blocklisted one.
func (f *shapesFixture) assertMovesOn(t *testing.T, wantReason string) {
	t.Helper()
	ctx := context.Background()
	entries := blocklistEntries(t, f.regrabFixture)
	if len(entries) != 1 || entries[0].GUID != regrabGUID {
		t.Fatalf("blocklist = %+v, want one entry for the failing release", entries)
	}
	if !strings.Contains(entries[0].Reason, wantReason) {
		t.Errorf("reason %q does not name %q", entries[0].Reason, wantReason)
	}
	if f.adds.Load() == 0 {
		f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	}
	alt, err := f.downloads.GetByGUID(ctx, shapesAltGUID)
	if err != nil || alt == nil {
		t.Fatalf("the search did not move on to the other release: %v", err)
	}
	if f.adds.Load() != 1 {
		t.Errorf("SABnzbd got %d requests, want the one add of the other release", f.adds.Load())
	}
}

// Shape 1a: the indexer answers the .torrent link with 404 or 410 (#4119).
func TestShape4111_IndexerNotFoundBlocklistsAndMovesOn(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := newShapesFixture(t, status, "Ok.")
			if n := f.sweepUntilBlocklisted(t, 2*releasefail.EscalateAfter); n != releasefail.EscalateAfter {
				t.Errorf("%d failed sweeps before the search moved on, want %d", n, releasefail.EscalateAfter)
			}
			f.assertMovesOn(t, "HTTP "+strconv.Itoa(status))
		})
	}
}

// Shape 1b: the indexer answers the .torrent link with a 5xx and no ban
// notice. Every failure pauses the indexer, which here also holds back the
// other release (same indexer), so the first one only backs off; three a
// retry apart blocklist the release and the search moves on.
func TestShape4111_Indexer5xxBlocklistsAndMovesOn(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := newShapesFixture(t, status, "Ok.")
			if n := f.sweepUntilBlocklisted(t, 2*releasefail.EscalateAfter); n != releasefail.EscalateAfter {
				t.Errorf("%d failed sweeps before the search moved on, want %d", n, releasefail.EscalateAfter)
			}
			f.assertMovesOn(t, "HTTP "+strconv.Itoa(status))
		})
	}
}

// Shape 1c: a 429 or a "disabled till" ban. Never counted (a quota and a
// dead link look the same); the indexer is paused instead, and the release
// is retried once the pause lapses.
func TestShape4111_IndexerRateLimitNeverBlocklists(t *testing.T) {
	f := newShapesFixture(t, http.StatusTooManyRequests, "Ok.")
	if n := f.sweepUntilBlocklisted(t, 3*releasefail.EscalateAfter); n != 3*releasefail.EscalateAfter {
		t.Errorf("%d failed sweeps, want every sweep to retry the release", n)
	}
	if n := len(blocklistEntries(t, f.regrabFixture)); n != 0 {
		t.Fatalf("a 429 blocklisted the release (%d entries)", n)
	}
}

// Shape 2: the download client rejects the add (qBittorrent "Fails."). Never
// counted: qBittorrent 4.x answers a torrent it already holds the same way,
// so this cannot be told from a release the user already has.
func TestShape4111_ClientRejectsAddNeverBlocklists(t *testing.T) {
	f := newShapesFixture(t, http.StatusOK, "Fails.")
	if n := f.sweepUntilBlocklisted(t, 3*releasefail.EscalateAfter); n != 3*releasefail.EscalateAfter {
		t.Errorf("%d failed sweeps, want every sweep to retry the release", n)
	}
	if n := len(blocklistEntries(t, f.regrabFixture)); n != 0 {
		t.Fatalf("a rejected add blocklisted the release (%d entries)", n)
	}
	ev, err := f.s.history.ListByBook(context.Background(), f.book.ID)
	if err != nil || len(ev) == 0 || !strings.Contains(ev[0].Data, `"class":"local"`) {
		t.Fatalf("history = %v, %v; want the rejection recorded as local", ev, err)
	}
}
