package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/releasefail"
)

const indexer429 = "fetch torrent content: fetch torrent: indexer returned HTTP 429: https://idx.example/dl?apikey=SECRET123"

// failureFixture is the regrab fixture with history wired and a download row
// for the release, which is what a grab leaves behind when the send fails.
func failureFixture(t *testing.T) (*regrabFixture, *models.Download) {
	t.Helper()
	f := newRegrabFixture(t)
	f.s.history = db.NewHistoryRepo(f.database)
	ctx := context.Background()
	indexers, err := f.s.indexers.List(ctx)
	if err != nil || len(indexers) == 0 {
		t.Fatalf("indexers: %v", err)
	}
	dl := &models.Download{
		GUID: regrabGUID, Title: "Regrab Book.epub", Status: models.StateGrabbed,
		Protocol: "torrent", BookID: &f.book.ID, OwnerUserID: f.bob, IndexerID: &indexers[0].ID,
	}
	if err := f.downloads.Create(ctx, dl); err != nil {
		t.Fatal(err)
	}
	return f, dl
}

func blocklistEntries(t *testing.T, f *regrabFixture) []models.BlocklistEntry {
	t.Helper()
	entries, err := f.s.blocklist.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// A single indexer 429 is a blip: no blocklist row. The same failure
// EscalateAfter times in a row is the release, and blocklists it.
func TestRecordSendFailure_RepeatedIndexerErrorEscalates(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()

	f.s.recordSendFailure(ctx, dl, errors.New(indexer429))
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("a single 429 blocklisted the release (%d entries)", n)
	}
	for i := 2; i < releasefail.EscalateAfter; i++ {
		f.s.recordSendFailure(ctx, dl, errors.New(indexer429))
	}
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("blocklisted before %d failures (%d entries)", releasefail.EscalateAfter, n)
	}
	f.s.recordSendFailure(ctx, dl, errors.New(indexer429))

	entries := blocklistEntries(t, f)
	if len(entries) != 1 {
		t.Fatalf("blocklist has %d entries after %d 429s, want 1", len(entries), releasefail.EscalateAfter)
	}
	e := entries[0]
	if e.GUID != regrabGUID || e.Scope != models.BlocklistScopeRelease {
		t.Errorf("entry = %+v, want release scope for %s", e, regrabGUID)
	}
	if !strings.Contains(e.Reason, "indexer HTTP 429") || !strings.Contains(e.Reason, "3 times") {
		t.Errorf("reason %q does not name the class and count", e.Reason)
	}
	// The blocklist the sweep builds must now reject the release.
	spec := decision.NewBlocklistedSpec(entries)
	if ok, _ := spec.IsSatisfiedBy(decision.Release{GUID: regrabGUID}, f.book); ok {
		t.Error("the sweep's blocklist spec still accepts the blocklisted release")
	}

	// No secret, URL or client address reaches history or the blocklist.
	events, err := f.s.history.ListByBook(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != releasefail.EscalateAfter {
		t.Fatalf("history has %d events, want one per failure (%d)", len(events), releasefail.EscalateAfter)
	}
	for _, ev := range events {
		if strings.Contains(ev.Data, "SECRET123") || strings.Contains(ev.Data, "idx.example") {
			t.Errorf("history event leaks the error text: %s", ev.Data)
		}
	}
	if strings.Contains(e.Reason, "SECRET123") {
		t.Errorf("blocklist reason leaks the error text: %s", e.Reason)
	}
}

// A fault on Bindery's side of the wire says nothing about the release, however
// often it repeats.
func TestRecordSendFailure_LocalFaultNeverBlocklists(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.s.recordSendFailure(ctx, dl, errors.New("add torrent: dial tcp 10.0.0.5:8080: connect: connection refused"))
	}
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("a client that was unreachable blocklisted the release (%d entries)", n)
	}
	if _, backed := f.s.indexerBackedOff(*dl.IndexerID, time.Now()); backed {
		t.Error("a client fault backed the indexer off")
	}
}

// A release that imported between failures starts counting from zero.
func TestRecordSendFailure_ImportResetsTheCount(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	for i := 0; i < releasefail.EscalateAfter-1; i++ {
		f.s.recordSendFailure(ctx, dl, errors.New(indexer429))
	}
	time.Sleep(10 * time.Millisecond)
	if err := f.s.history.Create(ctx, &models.HistoryEvent{
		BookID: &f.book.ID, EventType: models.HistoryEventBookImported, SourceTitle: dl.Title, Data: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	f.s.recordSendFailure(ctx, dl, errors.New(indexer429))
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("the count survived an import: %d entries", n)
	}
}

// The indexer is left alone until the time it gave, and the search passes its
// releases over (the fixture's only release is the indexer's) instead of
// sending them to the client.
func TestRecordSendFailure_IndexerBackoffHonoursTheTimestamp(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	until := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second)
	msg := fmt.Sprintf("fetch torrent: indexer returned HTTP 429: Indexer is disabled till %s due to recent failures.",
		until.Format("1/2/2006 15:04:05"))

	f.s.recordSendFailure(ctx, dl, errors.New(msg))

	got, backed := f.s.indexerBackedOff(*dl.IndexerID, time.Now().UTC())
	if !backed || !got.Equal(until) {
		t.Fatalf("backoff = %v (%v), want until the indexer's own %v", got, backed, until)
	}

	// The grab path skips the release while the indexer backs off.
	if err := f.downloads.Delete(ctx, dl.ID); err != nil {
		t.Fatal(err)
	}
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 0 {
		t.Fatalf("a release from a backing off indexer was sent to the client (%d adds)", n)
	}
	row, err := f.downloads.GetByGUID(ctx, regrabGUID)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Errorf("a download row was created for a release the search skipped: %+v", row)
	}

	if _, backed := f.s.indexerBackedOff(*dl.IndexerID, until.Add(time.Second)); backed {
		t.Error("the backoff outlived the indexer's timestamp")
	}

	// Once the backoff lapses the release is grabbed again.
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 1 {
		t.Fatalf("after the backoff the release was not grabbed (%d adds)", n)
	}
}

// The regrab path does not pick a blocklisted release again: the blocklist
// spec rejects it before the dead row's cooldown is consulted.
func TestSearchAndGrab_SkipsBlocklistedFailedRelease(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	if err := f.downloads.SetError(ctx, dl.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	f.backdate(t, dl.ID, "dead_at", time.Now().Add(-48*time.Hour))
	f.s.recordSendFailure(ctx, dl, errors.New(indexer429)) // not blocklisted yet
	releasefail.Blocklist(ctx, f.s.blocklist, dl, "downloadFailed: test")

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 0 {
		t.Fatalf("a blocklisted release past its cooldown was re-sent (%d adds)", n)
	}
}
