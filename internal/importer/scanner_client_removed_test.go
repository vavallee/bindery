package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/releasefail"
)

// A torrent the client listed and later lost (a person cancelled it in the
// client, the client dropped it, it was wiped) is clientRemoved: recorded,
// failed, re-grabbed as before, and never counted towards a blocklist, however
// often it repeats and however well the indexer serves otherwise.
func TestNeverArrived_RemovedAfterBeingSeenIsNeverCounted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-cancelled")

	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		if _, err := f.downloads.MarkClientSeen(ctx, dl.ID); err != nil {
			t.Fatal(err)
		}
		neverArrived(ctx, f, dl)
		ageHistory(t, f, 7*time.Hour)
		// the next attempt is a fresh row state; clear the sighting as a
		// re-grab does, then see it again on the next loop turn
		if _, err := f.database.Exec(`UPDATE downloads SET client_seen_at = NULL WHERE id = ?`, dl.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a release the user kept cancelling was blocklisted (%d entries)", n)
	}
	got, err := f.downloads.GetByGUID(ctx, "guid-cancelled")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Status != models.StateFailed {
		t.Errorf("status = %q, want failed so the book is re-grabbed as before", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "removed from the download client") ||
		strings.Contains(got.ErrorMessage, "never reached") {
		t.Errorf("queue message %q does not say the client removed it", got.ErrorMessage)
	}
	events, err := f.scanner.history.ListByBook(ctx, *dl.BookID)
	if err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, ev := range events {
		if ev.EventType == models.HistoryEventDownloadFailed && strings.Contains(ev.Data, `"class":"clientRemoved"`) {
			removed++
		}
	}
	if removed != 3*releasefail.EscalateAfter {
		t.Errorf("%d clientRemoved events recorded, want %d", removed, 3*releasefail.EscalateAfter)
	}
}

// A torrent no poll ever found is never arrived and counts, with the message
// that says so.
func TestNeverArrived_NeverSeenCountsAndSaysSo(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-unseen")
	for i := 0; i < releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 1 {
		t.Fatalf("%d blocklist entries, want 1", n)
	}
	got, _ := f.downloads.GetByGUID(ctx, "guid-unseen")
	if got == nil || !strings.Contains(got.ErrorMessage, "never reached the download client") {
		t.Errorf("queue message %q does not say it never reached the client", got.ErrorMessage)
	}
}

// A row that predates migration 136 (live when it ran) reads as seen, so it
// can never count as never arrived. The migration test proves the backfill;
// this is the importer side of it.
func TestNeverArrived_RowFromBeforeTheMigrationNeverCounts(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-old")
	// What the migration's backfill does to a live row.
	if _, err := f.database.Exec(`UPDATE downloads SET client_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, dl.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		neverArrived(ctx, f, dl)
		ageHistory(t, f, 7*time.Hour)
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("an upgraded row was counted as never arrived (%d entries)", n)
	}
}

// The poll writes the sighting, once, and the later disappearance reads as a
// removal: the whole path through checkQbittorrentDownloads.
func TestCheckQbittorrentDownloads_SeenThenRemovedIsClientRemoved(t *testing.T) {
	var listed atomic.Bool
	listed.Store(true)
	s, dlRepo, blocklist, history, client, again := clientErrorFixture(t, "qbittorrent", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if !listed.Load() {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash": clientErrHash, "name": "Errored Torrent", "state": "downloading",
				"progress": 0.4, "amount_left": 100, "size": 200, "category": "",
				"save_path": "/tmp/x", "content_path": "/tmp/x/Errored Torrent",
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	dl, err := dlRepo.GetByGUID(ctx, "guid-client-error")
	if err != nil || dl == nil {
		t.Fatal(err)
	}
	if seen, _ := dlRepo.ClientSeen(ctx, dl.ID); seen {
		t.Fatal("the row reads as seen before any poll")
	}
	s.checkQbittorrentDownloads(ctx, client)
	if seen, _ := dlRepo.ClientSeen(ctx, dl.ID); !seen {
		t.Fatal("a poll that found the torrent did not record the sighting")
	}
	var before time.Time
	if err := again.database.QueryRow(`SELECT client_seen_at FROM downloads WHERE id = ?`, dl.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	s.checkQbittorrentDownloads(ctx, client)
	var after time.Time
	if err := again.database.QueryRow(`SELECT client_seen_at FROM downloads WHERE id = ?`, dl.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatalf("the sighting was rewritten on a later poll: %v then %v", before, after)
	}

	// The user removes it in the client; ten minutes have passed.
	listed.Store(false)
	if _, err := again.database.Exec(`UPDATE downloads SET added_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC(), dl.ID); err != nil {
		t.Fatal(err)
	}
	s.checkQbittorrentDownloads(ctx, client)

	got, err := dlRepo.GetByGUID(ctx, "guid-client-error")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Status != models.StateFailed || !strings.Contains(got.ErrorMessage, "removed from the download client") {
		t.Fatalf("status %q, message %q: want failed as removed from the client", got.Status, got.ErrorMessage)
	}
	entries, _ := blocklist.List(ctx)
	if len(entries) != 0 {
		t.Fatalf("blocklist = %+v, want empty", entries)
	}
	events, _ := history.ListByType(ctx, models.HistoryEventDownloadFailed)
	if len(events) != 1 || !strings.Contains(events[0].Data, `"class":"clientRemoved"`) {
		t.Fatalf("history = %+v, want one clientRemoved event", events)
	}
}
