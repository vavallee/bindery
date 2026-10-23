package scheduler

import (
	"errors"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/decision"
	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func (f *schedPinFixture) result(guid, protocol string) *newznab.SearchResult {
	return &newznab.SearchResult{GUID: guid, Title: "Pin Author Pinned Book epub", Protocol: protocol, IndexerID: f.idx.ID, IndexerName: f.idx.Name}
}

func TestPinnedClient_Routing(t *testing.T) {
	f := newSchedPinFixture(t)
	ctx := f.ctx

	t.Run("unpinned indexer yields no client", func(t *testing.T) {
		c, err := f.s.pinnedClient(ctx, f.result("g", "usenet"))
		if c != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", c, err)
		}
	})
	t.Run("result without an indexer id is never pinned", func(t *testing.T) {
		f.pin(t, f.privC.ID)
		res := f.result("g", "usenet")
		res.IndexerID = 0
		if c, err := f.s.pinnedClient(ctx, res); c != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", c, err)
		}
	})
	t.Run("no indexer repo means no pin", func(t *testing.T) {
		s := &Scheduler{clients: f.s.clients, history: f.s.history}
		if c, err := s.pinnedClient(ctx, f.result("g", "usenet")); c != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", c, err)
		}
	})
	t.Run("pinned indexer yields exactly its client, not the higher priority one", func(t *testing.T) {
		f.pin(t, f.privC.ID)
		c, err := f.s.pinnedClient(ctx, f.result("g", "usenet"))
		if err != nil || c == nil || c.ID != f.privC.ID {
			t.Fatalf("got %v, %v; want client %d", c, err, f.privC.ID)
		}
	})
	t.Run("release protocol must match the pinned client", func(t *testing.T) {
		c, err := f.s.pinnedClient(ctx, f.result("g", "torrent"))
		var perr *db.PinnedClientError
		if c != nil || !errors.As(err, &perr) || perr.Reason != db.PinClientWrongProtocol {
			t.Fatalf("got %v, %v; want a wrong protocol PinnedClientError", c, err)
		}
	})
	t.Run("indexer lookup failure is an error and not a silent fallback", func(t *testing.T) {
		broken, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		s := &Scheduler{indexers: db.NewIndexerRepo(broken), clients: f.s.clients, history: f.s.history}
		broken.Close()
		c, err := s.pinnedClient(ctx, f.result("g", "usenet"))
		if c != nil || err == nil {
			t.Fatalf("got %v, %v; want an error", c, err)
		}
		var perr *db.PinnedClientError
		if errors.As(err, &perr) {
			t.Fatalf("a database failure must not look like a pin refusal: %v", err)
		}
	})
}

func TestCheckPin_Outcomes(t *testing.T) {
	f := newSchedPinFixture(t)
	ctx := f.ctx

	t.Run("an indexer already skipped this sweep is skipped without a lookup or a record", func(t *testing.T) {
		skipped := map[int64]bool{f.idx.ID: true}
		c, skip, err := f.s.checkPin(ctx, f.book, f.result("g", "usenet"), skipped)
		if c != nil || !skip || err != nil {
			t.Fatalf("got %v, %v, %v", c, skip, err)
		}
		if n := len(f.pinnedFailures(t)); n != 0 {
			t.Fatalf("recorded %d failures for a quiet skip", n)
		}
	})

	t.Run("a database error stops the grab and does not mark the indexer skipped", func(t *testing.T) {
		broken, err := db.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		s := &Scheduler{indexers: db.NewIndexerRepo(broken), clients: f.s.clients, history: f.s.history}
		broken.Close()
		skipped := map[int64]bool{}
		c, skip, err := s.checkPin(ctx, f.book, f.result("g", "usenet"), skipped)
		if c != nil || skip || err == nil {
			t.Fatalf("got %v, %v, %v; want nil, false, error", c, skip, err)
		}
		if skipped[f.idx.ID] {
			t.Fatal("a transient database error must not blacklist the indexer for the sweep")
		}
	})

	t.Run("a refused pin skips, marks the indexer and records once", func(t *testing.T) {
		f.pin(t, f.privC.ID)
		f.privC.Enabled = false
		if err := f.clients.Update(ctx, f.privC); err != nil {
			t.Fatal(err)
		}
		skipped := map[int64]bool{}
		c, skip, err := f.s.checkPin(ctx, f.book, f.result("g1", "usenet"), skipped)
		if c != nil || !skip || err != nil {
			t.Fatalf("got %v, %v, %v; want nil, true, nil", c, skip, err)
		}
		if !skipped[f.idx.ID] {
			t.Fatal("indexer not marked skipped")
		}
		c, skip, err = f.s.checkPin(ctx, f.book, f.result("g2", "usenet"), skipped)
		if c != nil || !skip || err != nil {
			t.Fatalf("second release: got %v, %v, %v", c, skip, err)
		}
		if n := len(f.pinnedFailures(t)); n != 1 {
			t.Fatalf("failures recorded = %d, want 1 for two releases of one indexer", n)
		}
	})

	t.Run("a healthy pin returns the client and does not skip", func(t *testing.T) {
		f.privC.Enabled = true
		if err := f.clients.Update(ctx, f.privC); err != nil {
			t.Fatal(err)
		}
		c, skip, err := f.s.checkPin(ctx, f.book, f.result("g3", "usenet"), map[int64]bool{})
		if err != nil || skip || c == nil || c.ID != f.privC.ID {
			t.Fatalf("got %v, %v, %v", c, skip, err)
		}
	})
}

func TestRecordPinnedFailure_Dedupe(t *testing.T) {
	f := newSchedPinFixture(t)
	n := &recordingNotifier{}
	f.s.notif = n
	disabled := &db.PinnedClientError{IndexerName: "TrackerX", ClientID: 1, ClientName: "c", Reason: db.PinClientDisabled}
	missing := &db.PinnedClientError{IndexerName: "TrackerX", ClientID: 1, Reason: db.PinClientMissing}

	f.s.recordPinnedFailure(f.ctx, f.book.ID, f.result("g1", "usenet"), disabled)
	f.s.recordPinnedFailure(f.ctx, f.book.ID, f.result("g2", "usenet"), disabled)
	if got := len(f.pinnedFailures(t)); got != 1 {
		t.Fatalf("identical failure for the same indexer recorded %d times, want 1", got)
	}

	other := f.result("g3", "usenet")
	other.IndexerID = f.idx.ID + 50
	f.s.recordPinnedFailure(f.ctx, f.book.ID, other, disabled)
	if got := len(f.pinnedFailures(t)); got != 2 {
		t.Fatalf("a different indexer must record its own failure, events = %d", got)
	}

	f.s.recordPinnedFailure(f.ctx, f.book.ID, f.result("g4", "usenet"), missing)
	if got := len(f.pinnedFailures(t)); got != 3 {
		t.Fatalf("a changed reason is new information and must record, events = %d", got)
	}

	f.s.recordPinnedFailure(f.ctx, f.book.ID+99, f.result("g5", "usenet"), disabled)
	if got := len(f.pinnedFailures(t)); got != 3 {
		t.Fatalf("another book has no history of its own here, events listed for the first book = %d", got)
	}
	failed := 0
	for _, typ := range n.types {
		if typ == "downloadFailed" {
			failed++
		}
	}
	if failed != 4 {
		t.Fatalf("notifications = %d, want 4 (one per recorded failure)", failed)
	}
}

func TestPendingApproved_Gate(t *testing.T) {
	newFixture := func(t *testing.T) (*schedPinFixture, *db.PendingReleaseRepo) {
		f := newSchedPinFixture(t)
		f.s.searcher.(*perFormatSearcher).byFormat[models.MediaTypeEbook] = nil
		p := db.NewPendingReleaseRepo(f.database)
		f.s.pending = p
		return f, p
	}

	t.Run("no pending repo", func(t *testing.T) {
		f := newSchedPinFixture(t)
		res, c, err := f.s.pendingApproved(f.ctx, f.book, models.MediaTypeEbook, nil, nil)
		if res != nil || c != nil || err != nil {
			t.Fatalf("got %v, %v, %v", res, c, err)
		}
	})

	t.Run("a gate error stops the sweep and keeps the row", func(t *testing.T) {
		f, p := newFixture(t)
		f.holdPending(t, p, "g-held", f.idx)
		boom := errors.New("boom")
		dm := decision.New()
		res, c, err := f.s.pendingApproved(f.ctx, f.book, models.MediaTypeEbook, dm,
			func(*newznab.SearchResult) (*models.DownloadClient, bool, error) { return nil, false, boom })
		if res != nil || c != nil || !errors.Is(err, boom) {
			t.Fatalf("got %v, %v, %v; want the gate error", res, c, err)
		}
		rows, _ := p.ListByBookAndMediaType(f.ctx, f.book.ID, models.MediaTypeEbook)
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want the held release kept", len(rows))
		}
	})

	t.Run("an accepted row is returned with its client and deleted", func(t *testing.T) {
		f, p := newFixture(t)
		f.holdPending(t, p, "g-held", f.idx)
		dm := decision.New()
		res, c, err := f.s.pendingApproved(f.ctx, f.book, models.MediaTypeEbook, dm,
			func(*newznab.SearchResult) (*models.DownloadClient, bool, error) { return f.privC, false, nil })
		if err != nil || res == nil || res.GUID != "g-held" || c == nil || c.ID != f.privC.ID {
			t.Fatalf("got %v, %v, %v", res, c, err)
		}
		rows, _ := p.ListByBookAndMediaType(f.ctx, f.book.ID, models.MediaTypeEbook)
		if len(rows) != 0 {
			t.Fatalf("rows = %d, want the taken release deleted", len(rows))
		}
	})

	t.Run("a corrupt stored release is passed over, not fatal", func(t *testing.T) {
		f, p := newFixture(t)
		f.holdPending(t, p, "g-good", f.idx)
		if err := p.Upsert(f.ctx, &models.PendingRelease{
			BookID: f.book.ID, MediaType: models.MediaTypeEbook, Title: "bad", GUID: "g-bad", Protocol: "usenet", Reason: "delay", ReleaseJSON: "{not json",
		}); err != nil {
			t.Fatal(err)
		}
		dm := decision.New()
		res, _, err := f.s.pendingApproved(f.ctx, f.book, models.MediaTypeEbook, dm, nil)
		if err != nil || res == nil || res.GUID != "g-good" {
			t.Fatalf("got %v, %v; want the good row", res, err)
		}
	})

	t.Run("other media type rows are not considered", func(t *testing.T) {
		f, p := newFixture(t)
		f.holdPending(t, p, "g-held", f.idx)
		dm := decision.New()
		res, _, err := f.s.pendingApproved(f.ctx, f.book, models.MediaTypeAudiobook, dm, nil)
		if err != nil || res != nil {
			t.Fatalf("got %v, %v; want nothing for the other format", res, err)
		}
	})
}
