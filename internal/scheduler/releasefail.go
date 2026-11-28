package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/releasefail"
)

// recordSendFailure acts on a grab the download client could not take (#4111).
//
// It acts on torrent grabs only. The failure is written to history with its
// class, so the count the next failure sees includes it. An indexer 429, 5xx
// or "disabled till" ban makes the scheduler pass that indexer's releases over
// until it is due, so Bindery stops feeding an indexer that is banning itself.
// A 429 or a ban is never counted towards a blocklist, because a quota and a
// dead link cannot be told apart. A 404 or 410, a 5xx without a ban notice and
// a torrent that never arrived can count (releasefail.Assess); a release that has
// failed releasefail.EscalateAfter times in a row is blocklisted. A failure
// that is Bindery's own (client unreachable, a timeout) is recorded and
// neither counted nor blocklisted.
//
// Nothing here copies the error text into history or the blocklist: it can
// carry a URL with an API key, or the client's address, which a non admin
// reads in the History tab.
func (s *Scheduler) recordSendFailure(ctx context.Context, dl *models.Download, sendErr error) {
	// Usenet is not touched: its failures are judged by the client's own
	// verdict at poll time (#3049), and an NZB fetch error here says nothing
	// that rule does not already cover.
	if dl.Protocol != "torrent" {
		return
	}
	now := time.Now().UTC()
	f := releasefail.ClassifySend(sendErr.Error(), now)
	if until := releasefail.IndexerBackoffUntil(f, now); !until.IsZero() && dl.IndexerID != nil {
		s.backOffIndexer(*dl.IndexerID, until)
		slog.Warn("backing off an indexer after a failed grab",
			"indexer_id", *dl.IndexerID, "until", until, "detail", f.Detail)
	}
	verdict := releasefail.Assess(ctx, s.history, dl, f.Class, now)
	// Several releases of one indexer answering 404 or 410 is the indexer's
	// problem (a misrouted proxy, a moved base path): pause it too.
	if f.Class == releasefail.ClassRelease && verdict.OtherReleasesFailing && dl.IndexerID != nil {
		s.backOffIndexer(*dl.IndexerID, now.Add(releasefail.DefaultIndexerBackoff))
		slog.Warn("backing off an indexer whose links are failing across releases",
			"indexer_id", *dl.IndexerID, "detail", f.Detail)
	}
	message := f.Detail
	if verdict.Block {
		message += ". Blocklisted after " + strconv.Itoa(verdict.Count) + " failures in a row"
	}
	if s.history != nil {
		fields := map[string]string{"guid": dl.GUID, "message": message, "class": string(verdict.Class)}
		if dl.IndexerID != nil {
			fields["indexerId"] = strconv.FormatInt(*dl.IndexerID, 10)
		}
		data, _ := json.Marshal(fields)
		_ = s.history.Create(ctx, &models.HistoryEvent{
			BookID:      dl.BookID,
			EventType:   models.HistoryEventDownloadFailed,
			SourceTitle: dl.Title,
			Data:        string(data),
			OwnerUserID: dl.OwnerUserID,
		})
	}
	if verdict.Block {
		releasefail.Blocklist(ctx, s.blocklist, dl, releasefail.Reason(f.Detail, verdict.Count))
	}
}

// backOffIndexer leaves an indexer alone until `until`. It never shortens a
// backoff that is already longer.
func (s *Scheduler) backOffIndexer(id int64, until time.Time) {
	s.indexerBackoffMu.Lock()
	defer s.indexerBackoffMu.Unlock()
	if s.indexerBackoff == nil {
		s.indexerBackoff = map[int64]time.Time{}
	}
	if cur, ok := s.indexerBackoff[id]; !ok || until.After(cur) {
		s.indexerBackoff[id] = until
	}
}

// indexerBackedOff reports whether the indexer is still being left alone, and
// until when. An expired entry is dropped.
func (s *Scheduler) indexerBackedOff(id int64, now time.Time) (time.Time, bool) {
	if id == 0 {
		return time.Time{}, false
	}
	s.indexerBackoffMu.Lock()
	defer s.indexerBackoffMu.Unlock()
	until, ok := s.indexerBackoff[id]
	if !ok {
		return time.Time{}, false
	}
	if !until.After(now) {
		delete(s.indexerBackoff, id)
		return time.Time{}, false
	}
	return until, true
}
