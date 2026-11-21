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
// The failure is written to history with its class, so the count the next
// failure sees includes it. A failure that is the indexer's (429, 5xx, a
// "disabled till" ban) also makes the scheduler pass that indexer's releases
// over until it is due, so Bindery stops feeding an indexer that is banning
// itself. A release that has failed releasefail.EscalateAfter times in a row
// is blocklisted. A failure that is Bindery's own (client unreachable, a
// timeout) is recorded and neither counted nor blocklisted: it says nothing
// about the release, and the cooldown retries it.
//
// Nothing here copies the error text into history or the blocklist: it can
// carry a URL with an API key, or the client's address, which a non admin
// reads in the History tab.
func (s *Scheduler) recordSendFailure(ctx context.Context, dl *models.Download, sendErr error) {
	now := time.Now().UTC()
	f := releasefail.ClassifySend(sendErr.Error(), now)
	// Read the pause before this failure sets it: a failure that happens while
	// the indexer is already backing off is not evidence about the release.
	alreadyBackedOff := false
	if dl.IndexerID != nil {
		_, alreadyBackedOff = s.indexerBackedOff(*dl.IndexerID, now)
	}
	if until := releasefail.IndexerBackoffUntil(f, now); !until.IsZero() && dl.IndexerID != nil {
		s.backOffIndexer(*dl.IndexerID, until)
		slog.Warn("backing off an indexer after a failed grab",
			"indexer_id", *dl.IndexerID, "until", until, "detail", f.Detail)
	}
	verdict := releasefail.Assess(ctx, s.history, dl, f.Class, now, alreadyBackedOff)
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
