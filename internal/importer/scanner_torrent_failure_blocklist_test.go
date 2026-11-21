package importer

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/releasefail"
)

// torrentFailureDownload is a downloading torrent row whose id the client does
// not know, old enough that the never-arrived grace has passed.
func torrentFailureDownload(t *testing.T, ctx context.Context, f contentFailureFixture, guid string) *models.Download {
	t.Helper()
	hash := "abcdef0123456789abcdef0123456789abcdef01"
	bookID := f.addBook(t, ctx, guid)
	dl := &models.Download{
		GUID: guid, Title: "Dead Torrent " + guid, Status: models.StateDownloading,
		Protocol: "torrent", TorrentID: &hash, BookID: &bookID,
	}
	if err := f.downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	dl.AddedAt = time.Now().Add(-time.Hour)
	// A real indexer row, because the blocklist entry points at it.
	idx := &models.Indexer{Name: "idx-" + guid, Type: "torznab", URL: "http://idx.invalid", Enabled: true}
	if err := db.NewIndexerRepo(f.database).Create(ctx, idx); err != nil {
		t.Fatal(err)
	}
	dl.IndexerID = &idx.ID
	serveOther(f, dl)
	return dl
}

// serveOther records that the download's indexer sent a different release to
// a client, which is what an indexer that is otherwise serving looks like.
func serveOther(f contentFailureFixture, dl *models.Download) {
	f.scanner.createHistoryEvent(context.Background(), models.HistoryEventGrabbed, "Served Elsewhere", nil, 0,
		map[string]string{"guid": "g-served", "indexerId": strconv.FormatInt(*dl.IndexerID, 10)})
}

// ageHistory moves every history row into the past, as the time between two
// retries does.
func ageHistory(t *testing.T, f contentFailureFixture, d time.Duration) {
	t.Helper()
	rows, err := f.database.Query(`SELECT id, created_at FROM history`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id int64
		at time.Time
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.at); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	rows.Close()
	for _, r := range all {
		if _, err := f.database.Exec(`UPDATE history SET created_at = ? WHERE id = ?`, r.at.Add(-d), r.id); err != nil {
			t.Fatal(err)
		}
	}
}

func neverArrived(ctx context.Context, f contentFailureFixture, dl *models.Download) {
	f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)
	serveOther(f, dl)
}

// TestNeverArrived_BlocklistsAfterRepeatedFailures is #4111 path 2: a release
// that never reaches the client is retried after the cooldown, and used to be
// retried for ever. One or two failures stay unblocklisted (the usual cause is
// a client restart), the third, a retry later, blocklists the release for
// every book.
func TestNeverArrived_BlocklistsAfterRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-never")

	for i := 1; i < releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
		if n := len(f.entries(t, ctx)); n != 0 {
			t.Fatalf("after %d failure(s) the release is blocklisted (%d entries); only a repeat may do that", i, n)
		}
		ageHistory(t, f, 7*time.Hour)
	}
	neverArrived(ctx, f, dl)

	entries := f.entries(t, ctx)
	if len(entries) != 1 {
		t.Fatalf("blocklist has %d entries after %d failures, want 1", len(entries), releasefail.EscalateAfter)
	}
	e := entries[0]
	if e.GUID != "guid-never" || e.Scope != models.BlocklistScopeRelease {
		t.Errorf("entry = {guid %q, scope %q}, want release scope for guid-never", e.GUID, e.Scope)
	}
	if !strings.Contains(e.Reason, "never reached the download client") || !strings.Contains(e.Reason, "3 times") {
		t.Errorf("reason %q does not name the failure and the count", e.Reason)
	}
}

// Failures close together are one moment, not a pattern.
func TestNeverArrived_BackToBackDoesNotEscalate(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-burst")
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a burst of failures blocklisted the release (%d entries)", n)
	}
}

// TestNeverArrived_ImportResetsTheCount: a release that worked between two
// failures starts counting again.
func TestNeverArrived_ImportResetsTheCount(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-reset")

	for i := 0; i < releasefail.EscalateAfter-1; i++ {
		neverArrived(ctx, f, dl)
		ageHistory(t, f, 7*time.Hour)
	}
	f.scanner.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, 0, map[string]string{"guid": dl.GUID})
	ageHistory(t, f, 7*time.Hour)
	neverArrived(ctx, f, dl)

	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a release that imported in between was blocklisted on its first new failure (%d entries)", n)
	}
}

// When the indexer is failing other releases the same way, a release that
// never arrived is not evidence against itself.
func TestNeverArrived_IndexerWideFaultIsNotCounted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-wide")
	f.scanner.createHistoryEvent(ctx, models.HistoryEventDownloadFailed, "Other", nil, 0,
		map[string]string{"guid": "someone-else", "class": "indexer", "indexerId": strconv.FormatInt(*dl.IndexerID, 10)})
	for i := 0; i < 2*releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
		ageHistory(t, f, time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a release was blocklisted while its indexer was failing others (%d entries)", n)
	}
}

// TestClientErroredTorrent_NeverBlocklists: the client's errored state is
// recorded but never counted. qBittorrent reports it for a full disk or an
// unwritable save path, which is the host, and a full disk would blocklist
// every release in turn.
func TestClientErroredTorrent_NeverBlocklists(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-errored")

	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.scanner.markDownloadFailedClass(ctx, dl, "Torrent failed in qBittorrent", string(releasefail.ClassClientError))
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("an errored torrent blocklisted the release (%d entries)", n)
	}
	events, err := f.scanner.history.ListByBook(ctx, *dl.BookID)
	if err != nil || len(events) != 3*releasefail.EscalateAfter {
		t.Fatalf("history: %v, %d events; the failure must still be recorded", err, len(events))
	}
	if !strings.Contains(events[0].Data, `"class":"clientError"`) {
		t.Errorf("event data %s does not carry the class", events[0].Data)
	}
}

// TestUnclassifiedFailureIsNeverCounted: markDownloadFailed (every other
// caller) stores no class and never escalates.
func TestUnclassifiedFailureIsNeverCounted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-plain")
	for i := 0; i < 2*releasefail.EscalateAfter; i++ {
		f.scanner.markDownloadFailed(ctx, dl, "something else")
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("an unclassified failure blocklisted the release (%d entries)", n)
	}
}

// A download with no indexer id never counts.
func TestNeverArrived_NoIndexerNeverCounts(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-noidx")
	served := *dl.IndexerID
	dl.IndexerID = nil
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)
		f.scanner.createHistoryEvent(ctx, models.HistoryEventGrabbed, "Served Elsewhere", nil, 0,
			map[string]string{"guid": "g-served", "indexerId": strconv.FormatInt(served, 10)})
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a download with no indexer was blocklisted (%d entries)", n)
	}
}

// A lone release that never arrives while its indexer serves nothing else is
// not evidence either.
func TestNeverArrived_IndexerServingNothingIsNotCounted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-alone")
	ageHistory(t, f, 3*24*time.Hour) // the one grab it was served with is long past
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a release was blocklisted while its indexer served nothing (%d entries)", n)
	}
}

// A usenet download is failed plainly, as before: #3049's rules govern it.
func TestNeverArrived_UsenetIsNotCounted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-usenet")
	dl.Protocol = "usenet"
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a usenet download was blocklisted (%d entries)", n)
	}
}
