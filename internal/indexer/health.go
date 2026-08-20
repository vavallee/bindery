package indexer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// healthRefreshInterval bounds how often an unchanged health state is rewritten.
//
// Without it, a scheduled wanted scan searching hundreds of books would issue
// one UPDATE per indexer per book, all of them storing the value already there.
// A state change is always written immediately; this only throttles repeats.
const healthRefreshInterval = 5 * time.Minute

// notifierEventHealth mirrors notifier.EventHealth. Declared locally for the
// same reason downloader/health.go declares it: it keeps this package from
// importing the notifier for one string.
const notifierEventHealth = "health"

// IndexerHealthStore persists the outcome of a search against one indexer.
// Implemented by *db.IndexerRepo.
type IndexerHealthStore interface {
	RecordSearchFailure(ctx context.Context, id int64, code int, message string, at time.Time) error
	RecordSearchSuccess(ctx context.Context, id int64, at time.Time) error
}

// healthEventNotifier is the narrow shape of *notifier.Notifier this package
// needs, so health reporting can be tested without an HTTP fixture.
type healthEventNotifier interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// healthSnapshot is what was last written for an indexer, used to tell a state
// change from a repeat.
type healthSnapshot struct {
	failing bool
	code    int
	message string
	written time.Time
}

// indexerHealth records whether each indexer answered its last search, and
// notifies when one enters a failure only a human can clear (#1935).
//
// Persisting it is the point: before this, an indexer answering every search
// with "Account suspended" looked entirely normal in Settings, and the only
// trace was the interactive search panel, which a user has to already suspect
// something to go and read.
type indexerHealth struct {
	store IndexerHealthStore
	notif healthEventNotifier

	mu   sync.Mutex
	last map[int64]healthSnapshot
	now  func() time.Time
}

func (h *indexerHealth) clock() time.Time {
	if h != nil && h.now != nil {
		return h.now()
	}
	return time.Now()
}

// shouldWrite reports whether next differs from what was last written for this
// indexer, or whether the stored copy is stale enough to refresh.
func (h *indexerHealth) shouldWrite(id int64, next healthSnapshot, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, seen := h.last[id]
	changed := !seen ||
		prev.failing != next.failing ||
		prev.code != next.code ||
		prev.message != next.message
	if !changed && now.Sub(prev.written) < healthRefreshInterval {
		return false
	}
	next.written = now
	if h.last == nil {
		h.last = make(map[int64]healthSnapshot)
	}
	h.last[id] = next
	return true
}

// enteredAttention reports whether this write moves the indexer into a hard
// auth failure it was not already in. That edge is what gets notified: notifying
// on every failed search would mean hundreds of messages from one 12-hour
// wanted scan.
func (h *indexerHealth) enteredAttention(id int64, next healthSnapshot) bool {
	if !next.failing || !isAttentionCode(next.code) {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, seen := h.last[id]
	return !seen || !prev.failing || !isAttentionCode(prev.code)
}

// isAttentionCode reports whether a Newznab code needs a human. The 1xx range
// (bad credentials, suspended account, VPN forbidden) does; a 5xx rate limit
// clears on its own and #1934 already benches the indexer for it.
func isAttentionCode(code int) bool {
	return code >= 100 && code <= 199
}

// newznabCode extracts the Newznab error code from err, or 0 when the failure
// was not an indexer rejection (a connection error, an unparseable body).
func newznabCode(err error) int {
	var ie *newznab.IndexerError
	if errors.As(err, &ie) {
		return ie.Code
	}
	return 0
}

// recordFailure stores that idx refused or failed to answer a search.
func (h *indexerHealth) recordFailure(ctx context.Context, idx models.Indexer, err error) {
	if h == nil || h.store == nil || idx.ID == 0 || err == nil {
		return
	}
	now := h.clock()
	next := healthSnapshot{failing: true, code: newznabCode(err), message: err.Error()}

	notify := h.enteredAttention(idx.ID, next)
	if !h.shouldWrite(idx.ID, next, now) && !notify {
		return
	}
	// Detached context: the search that produced this may be cancelled or on
	// its way out, and losing the record would leave the indexer looking
	// healthy for exactly the reason it is not.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if werr := h.store.RecordSearchFailure(writeCtx, idx.ID, next.code, next.message, now); werr != nil {
		slog.Warn("failed to record indexer health", "indexer", idx.Name, "error", werr)
		return
	}
	if notify && h.notif != nil {
		h.notif.Send(context.WithoutCancel(ctx), notifierEventHealth, map[string]interface{}{
			"indexerId":   idx.ID,
			"indexerName": idx.Name,
			"status":      "error",
			"message":     next.message,
		})
	}
}

// recordSuccess clears any stored failure for idx.
func (h *indexerHealth) recordSuccess(ctx context.Context, idx models.Indexer) {
	if h == nil || h.store == nil || idx.ID == 0 {
		return
	}
	now := h.clock()
	if !h.shouldWrite(idx.ID, healthSnapshot{}, now) {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.store.RecordSearchSuccess(writeCtx, idx.ID, now); err != nil {
		slog.Warn("failed to clear indexer health", "indexer", idx.Name, "error", err)
	}
}

// WithHealth attaches persistent per-indexer health recording. Without it the
// searcher behaves exactly as before, which is what keeps every existing test
// and every non-wired caller unaffected.
func (s *Searcher) WithHealth(store IndexerHealthStore) *Searcher {
	s.health = &indexerHealth{store: store, last: make(map[int64]healthSnapshot)}
	return s
}

// WithHealthNotifier attaches the notifier that hard auth failures publish to.
// No-op unless WithHealth was called first.
func (s *Searcher) WithHealthNotifier(n healthEventNotifier) *Searcher {
	if s.health != nil {
		s.health.notif = n
	}
	return s
}

// noteIndexerSuccess clears stored health after an indexer answers.
func (s *Searcher) noteIndexerSuccess(ctx context.Context, idx models.Indexer) {
	s.health.recordSuccess(ctx, idx)
}
