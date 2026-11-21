package importer

import (
	"context"
	"strings"
	"testing"
	"time"

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
	return dl
}

// TestNeverArrived_BlocklistsAfterRepeatedFailures is #4111 path 2: a release
// that never reaches the client is retried after the cooldown, and used to be
// retried for ever. One or two failures stay unblocklisted (the usual cause is
// a client restart), the third blocklists the release for every book.
func TestNeverArrived_BlocklistsAfterRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-never")

	for i := 1; i < releasefail.EscalateAfter; i++ {
		f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)
		if n := len(f.entries(t, ctx)); n != 0 {
			t.Fatalf("after %d failure(s) the release is blocklisted (%d entries); only a repeat may do that", i, n)
		}
	}
	f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)

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

// TestNeverArrived_ImportResetsTheCount: a release that worked between two
// failures starts counting again.
func TestNeverArrived_ImportResetsTheCount(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-reset")

	for i := 0; i < releasefail.EscalateAfter-1; i++ {
		f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)
	}
	time.Sleep(10 * time.Millisecond)
	f.scanner.createHistoryEvent(ctx, models.HistoryEventBookImported, dl.Title, dl.BookID, 0, map[string]string{})
	time.Sleep(10 * time.Millisecond)
	f.scanner.failDownloadThatNeverArrived(ctx, dl, map[int64]bool{}, true)

	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("a release that imported in between was blocklisted on its first new failure (%d entries)", n)
	}
}

// TestClientErroredTorrent_CountedThenBlocklisted is #4111 path 3. The client's
// errored state is counted, not an immediate ban: qBittorrent reports it for a
// full disk or an unwritable save path, which is the host and not the release.
func TestClientErroredTorrent_CountedThenBlocklisted(t *testing.T) {
	ctx := context.Background()
	f := newContentFailureFixture(t)
	dl := torrentFailureDownload(t, ctx, f, "guid-errored")

	for i := 1; i <= releasefail.EscalateAfter; i++ {
		f.scanner.markDownloadFailedCounted(ctx, dl, "Torrent failed in qBittorrent",
			releasefail.ClassClientError, "qBittorrent reported the torrent as errored")
		want := 0
		if i == releasefail.EscalateAfter {
			want = 1
		}
		if n := len(f.entries(t, ctx)); n != want {
			t.Fatalf("after %d errored report(s): %d blocklist entries, want %d", i, n, want)
		}
	}
	got, err := f.downloads.GetByGUID(ctx, "guid-errored")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.ErrorMessage, "Blocklisted") {
		t.Errorf("download error %q does not say the release was blocklisted", got.ErrorMessage)
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
	}
	if n := len(f.entries(t, ctx)); n != 0 {
		t.Fatalf("an unclassified failure blocklisted the release (%d entries)", n)
	}
}
