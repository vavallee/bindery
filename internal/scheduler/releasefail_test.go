package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/indexer/newznab"
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

// ageHistory moves every history row into the past, which is what the time
// between two retries does. The rows are read and rewritten one by one so the
// stored time keeps the driver's format.
func ageHistory(t *testing.T, f *regrabFixture, d time.Duration) {
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

func blocklistEntries(t *testing.T, f *regrabFixture) []models.BlocklistEntry {
	t.Helper()
	entries, err := f.s.blocklist.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// serveOther records that the indexer sent a different release to the client,
// which is what an indexer that is otherwise serving looks like.
func serveOther(t *testing.T, f *regrabFixture, dl *models.Download) {
	t.Helper()
	data := `{"guid":"g-served","indexerId":` + strconv.FormatInt(*dl.IndexerID, 10) + `}`
	if err := f.s.history.Create(context.Background(), &models.HistoryEvent{
		EventType: models.HistoryEventGrabbed, SourceTitle: "Served Elsewhere", Data: data,
	}); err != nil {
		t.Fatal(err)
	}
}

// failSpaced records n failures a retry apart, the way the cooldown spaces
// them, with the indexer serving other releases in between.
func failSpaced(t *testing.T, f *regrabFixture, dl *models.Download, msg string, n int) {
	t.Helper()
	failSpacedServed(t, f, dl, msg, n, true)
}

func failSpacedServed(t *testing.T, f *regrabFixture, dl *models.Download, msg string, n int, served bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		f.s.recordSendFailure(context.Background(), dl, errors.New(msg))
		if served {
			serveOther(t, f, dl)
		}
		f.s.indexerBackoffMu.Lock()
		f.s.indexerBackoff = nil // the pause has lapsed by the next retry
		f.s.indexerBackoffMu.Unlock()
		ageHistory(t, f, 7*time.Hour)
	}
}

// A single indexer 429 is a blip: no blocklist row. The same failure
// EscalateAfter times in a row, a retry apart, is the release, and blocklists it.
func TestRecordSendFailure_RepeatedIndexerErrorEscalates(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()

	failSpaced(t, f, dl, indexer429, 1)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("a single 429 blocklisted the release (%d entries)", n)
	}
	failSpaced(t, f, dl, indexer429, releasefail.EscalateAfter-2)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("blocklisted before %d failures (%d entries)", releasefail.EscalateAfter, n)
	}
	failSpaced(t, f, dl, indexer429, 1)

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

// Failures close together are one bad moment, not three: however many land
// inside the spacing, the release is not blocklisted.
func TestRecordSendFailure_BackToBackFailuresDoNotEscalate(t *testing.T) {
	f, dl := failureFixture(t)
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.s.recordSendFailure(context.Background(), dl, errors.New(indexer429))
		f.s.indexerBackoffMu.Lock()
		f.s.indexerBackoff = nil
		f.s.indexerBackoffMu.Unlock()
	}
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("%d failures inside the spacing blocklisted the release (%d entries)", 3*releasefail.EscalateAfter, n)
	}
}

// A fault on Bindery's side of the wire says nothing about the release, however
// often it repeats.
func TestRecordSendFailure_LocalFaultNeverBlocklists(t *testing.T) {
	f, dl := failureFixture(t)
	failSpaced(t, f, dl, "add torrent: dial tcp 10.0.0.5:8080: connect: connection refused", 3*releasefail.EscalateAfter)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("a client that was unreachable blocklisted the release (%d entries)", n)
	}
	if _, backed := f.s.indexerBackedOff(*dl.IndexerID, time.Now()); backed {
		t.Error("a client fault backed the indexer off")
	}
}

// An import in between starts the count again.
func TestRecordSendFailure_ImportResetsTheCount(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	failSpaced(t, f, dl, indexer429, releasefail.EscalateAfter-1)
	if err := f.s.history.Create(ctx, &models.HistoryEvent{
		BookID: &f.book.ID, EventType: models.HistoryEventBookImported, SourceTitle: dl.Title,
		Data: `{"guid":"` + regrabGUID + `"}`,
	}); err != nil {
		t.Fatal(err)
	}
	ageHistory(t, f, 7*time.Hour)
	failSpaced(t, f, dl, indexer429, 1)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("the count survived an import: %d entries", n)
	}
}

// A send that went through resets the count too: the release is not what was
// failing.
func TestRecordSendFailure_SuccessfulSendResetsTheCount(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	failSpaced(t, f, dl, indexer429, releasefail.EscalateAfter-1)
	for i := 0; i < 2; i++ {
		if err := f.s.history.Create(ctx, &models.HistoryEvent{
			BookID: &f.book.ID, EventType: models.HistoryEventGrabbed, SourceTitle: dl.Title,
			Data: `{"guid":"` + regrabGUID + `"}`,
		}); err != nil {
			t.Fatal(err)
		}
		ageHistory(t, f, time.Hour)
	}
	failSpaced(t, f, dl, indexer429, 1)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("the count survived a successful send: %d entries", n)
	}
}

// An indexer wide outage is not the release's fault: when other releases from
// the same indexer fail the same way, this one's failures are not counted.
func TestRecordSendFailure_IndexerWideFaultIsNotCounted(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	other := &models.Download{
		GUID: "g-other", Title: "Other Book.epub", Status: models.StateGrabbed,
		Protocol: "torrent", BookID: &f.book.ID, OwnerUserID: f.bob, IndexerID: dl.IndexerID,
	}
	if err := f.downloads.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	f.s.recordSendFailure(ctx, other, errors.New(indexer429))
	f.s.indexerBackoffMu.Lock()
	f.s.indexerBackoff = nil
	f.s.indexerBackoffMu.Unlock()

	failSpaced(t, f, dl, indexer429, 2*releasefail.EscalateAfter)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("an indexer wide outage blocklisted a release (%d entries)", n)
	}
}

// An outage is not the release: retries of one release through an indexer that
// serves nothing in between never add up to a blocklist entry, however long it
// lasts.
func TestRecordSendFailure_OutageDoesNotEscalate(t *testing.T) {
	f, dl := failureFixture(t)
	failSpacedServed(t, f, dl, indexer429, 4*releasefail.EscalateAfter, false)
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("retries through an outage blocklisted the release (%d entries)", n)
	}
}

// A failure while the indexer is already backing off is not evidence about the
// release either; it is recorded as indexer wide.
func TestRecordSendFailure_FailureDuringBackoffIsNotCounted(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	f.s.recordSendFailure(ctx, dl, errors.New(indexer429)) // sets the pause
	f.s.recordSendFailure(ctx, dl, errors.New(indexer429)) // inside it
	events, err := f.s.history.ListByBook(ctx, f.book.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %d", err, len(events))
	}
	wide := 0
	for _, ev := range events {
		if strings.Contains(ev.Data, `"class":"indexerWide"`) {
			wide++
		}
	}
	if wide != 1 {
		t.Fatalf("%d events recorded as indexerWide, want 1 (the one inside the pause)", wide)
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

	// The grab path skips the release while the indexer backs off, and says why.
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
	releasefail.Blocklist(ctx, f.s.blocklist, dl, "downloadFailed: test")

	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	if n := f.adds.Load(); n != 0 {
		t.Fatalf("a blocklisted release past its cooldown was re-sent (%d adds)", n)
	}
}

// Blocklist is idempotent: a second call for a blocklisted release adds no row.
func TestBlocklist_Idempotent(t *testing.T) {
	f, dl := failureFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		releasefail.Blocklist(ctx, f.s.blocklist, dl, "downloadFailed: test")
	}
	if n := len(blocklistEntries(t, f)); n != 1 {
		t.Fatalf("%d entries, want 1", n)
	}
}

// torrentFixture routes the fixture's grab through the real caller path: a
// qBittorrent stub that accepts the login, a torznab release whose link the
// "indexer" answers with the HTTP 500 #4111 quotes.
func torrentFixture(t *testing.T) *regrabFixture {
	t.Helper()
	f := newRegrabFixture(t)
	f.s.history = db.NewHistoryRepo(f.database)
	ctx := context.Background()

	indexerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Download failed", http.StatusInternalServerError)
	}))
	t.Cleanup(indexerSrv.Close)
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("[]"))
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
	clients, err := f.s.clients.List(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients: %v", err)
	}
	c := clients[0]
	c.Type, c.Host, c.Port = "qbittorrent", u.Hostname(), port
	if err := f.s.clients.Update(ctx, &c); err != nil {
		t.Fatal(err)
	}
	indexers, _ := f.s.indexers.List(ctx)
	seeders := 20
	f.s.searcher = &fixedResultsSearcher{results: []newznab.SearchResult{{
		GUID: regrabGUID, Title: "Regrab Book.epub", NZBURL: indexerSrv.URL + "/dl.torrent",
		Protocol: "torrent", IndexerID: &indexers[0].ID, Seeders: &seeders,
	}}}
	return f
}

// retryAfterCooldown makes the failed row due for the scheduler again and
// lets the indexer pause lapse, as six hours do.
func retryAfterCooldown(t *testing.T, f *regrabFixture) {
	t.Helper()
	idx, _ := f.s.indexers.List(context.Background())
	serveOther(t, f, &models.Download{IndexerID: &idx[0].ID})
	row, err := f.downloads.GetByGUID(context.Background(), regrabGUID)
	if err != nil || row == nil {
		t.Fatalf("row: %v", err)
	}
	f.backdate(t, row.ID, "dead_at", time.Now().Add(-7*time.Hour))
	f.s.indexerBackoffMu.Lock()
	f.s.indexerBackoff = nil
	f.s.indexerBackoffMu.Unlock()
}

// The whole path: three sweeps a cooldown apart, each failing on the indexer's
// 500, blocklist the release; the search then leaves it alone.
func TestSearchAndGrab_RealPathSpacedFailuresBlocklist(t *testing.T) {
	f := torrentFixture(t)
	ctx := context.Background()
	for i := 1; i <= releasefail.EscalateAfter; i++ {
		f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
		want := 0
		if i == releasefail.EscalateAfter {
			want = 1
		}
		if n := len(blocklistEntries(t, f)); n != want {
			t.Fatalf("after failure %d: %d blocklist entries, want %d", i, n, want)
		}
		retryAfterCooldown(t, f)
		ageHistory(t, f, 7*time.Hour)
	}
	f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
	events, _ := f.s.history.ListByBook(ctx, f.book.ID)
	if len(events) != releasefail.EscalateAfter {
		t.Errorf("the blocklisted release was tried again: %d failure events", len(events))
	}
}

// The same path with the failures close together (a person pressing search):
// the retries reach the client but add up to nothing.
func TestSearchAndGrab_RealPathBackToBackFailuresDoNotBlocklist(t *testing.T) {
	f := torrentFixture(t)
	ctx := context.Background()
	for i := 0; i < 3*releasefail.EscalateAfter; i++ {
		f.s.searchAndGrabFormat(ctx, f.book, models.MediaTypeEbook, nil)
		retryAfterCooldown(t, f) // the row is due again, the history is not aged
	}
	events, _ := f.s.history.ListByBook(ctx, f.book.ID)
	if len(events) < 3*releasefail.EscalateAfter {
		t.Fatalf("only %d failures reached history; the path did not run", len(events))
	}
	if n := len(blocklistEntries(t, f)); n != 0 {
		t.Fatalf("back to back failures blocklisted the release (%d entries)", n)
	}
}
