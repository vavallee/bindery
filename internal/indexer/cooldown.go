package indexer

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

// Cooldown bounds. A rate-limit rejection carries no obligation to say when to
// come back, so an indexer that just says "request limit reached" gets the
// default. A parsed hint is clamped: an indexer claiming a multi-week lockout
// (or sending a malformed number that parses to something absurd) must not be
// able to bench itself indefinitely without the user ever seeing why, and a
// hint of zero must not produce a cooldown that has already expired.
const (
	defaultRateLimitCooldown = time.Hour
	minRateLimitCooldown     = time.Minute
	maxRateLimitCooldown     = 24 * time.Hour
)

// retryHintRe matches the "Retry in 485 minutes" clause indexers append to a
// Newznab 500 description. Case-insensitive, tolerant of the unit being
// singular or plural, and anchored on nothing — the clause appears mid-sentence
// after the human-readable reason.
var retryHintRe = regexp.MustCompile(`(?i)retry\s+in\s+(\d+)\s*(second|minute|hour|day)s?`)

// parseRetryHint extracts the indexer's own "come back in N units" hint from a
// rate-limit description. Returns false when the description carries no hint,
// which is common — the caller falls back to defaultRateLimitCooldown.
func parseRetryHint(desc string) (time.Duration, bool) {
	m := retryHintRe.FindStringSubmatch(desc)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		// Unreachable given the \d+ group, but a 20-digit number overflows
		// Atoi and must not be read as zero.
		return 0, false
	}
	var unit time.Duration
	switch strings.ToLower(m[2]) {
	case "second":
		unit = time.Second
	case "minute":
		unit = time.Minute
	case "hour":
		unit = time.Hour
	case "day":
		unit = 24 * time.Hour
	default:
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// cooldownEntry is one indexer's active lockout.
type cooldownEntry struct {
	until  time.Time
	reason string
	// recordedAt is when the lockout was registered, compared against the
	// indexer row's UpdatedAt so an edit cancels it. See cooldownActive.
	recordedAt time.Time
}

// indexerCooldowns tracks which indexers have told us to stop asking, and until
// when.
//
// A Newznab 500 ("Request limit reached. Retry in 485 minutes.") is an explicit
// instruction, and before #1934 Bindery discarded it: the classification in
// newznab.IsRateLimitError was consulted only to abort tier fall-through within
// a single search, so the next search — and every search for the next eight
// hours — sent another request the indexer had already refused. On indexers
// that count refused requests against the quota, that can stop the window ever
// clearing.
//
// State is in memory on the single shared *Searcher (cmd/bindery/main.go), so
// both the scheduler's auto-grab and the API's interactive search observe the
// same cooldowns. It is lost on restart, which costs one refused request per
// indexer per search afterwards — exactly the pre-#1934 behaviour, so a restart
// can only ever return to the old cost, never do worse. Persisting it waits on
// the indexer health-state work (#1935), which needs columns of its own.
//
// The zero value is ready to use.
type indexerCooldowns struct {
	mu      sync.Mutex
	entries map[int64]cooldownEntry
	// now is injectable so tests can advance time without sleeping. nil means
	// time.Now.
	now func() time.Time
}

func (c *indexerCooldowns) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// note records a cooldown for idx if err is a rate-limit rejection, and reports
// whether it did. Auth failures (1xx) are deliberately excluded: a suspended
// account or a bad API key never heals on a timer, and benching the indexer for
// hours would mean a user who fixes their key sits there wondering why nothing
// happens. Those need visibility and a notification instead (#1935).
//
// Indexers with no id (id 0) are not tracked — there is nothing stable to key
// on, and every such indexer would share one entry.
func (c *indexerCooldowns) note(idx models.Indexer, err error) bool {
	if idx.ID == 0 || !newznab.IsRateLimitError(err) {
		return false
	}

	d := defaultRateLimitCooldown
	if hinted, ok := parseRetryHint(err.Error()); ok {
		d = hinted
	}
	d = max(min(d, maxRateLimitCooldown), minRateLimitCooldown)

	now := c.clock()
	entry := cooldownEntry{
		until:      now.Add(d),
		reason:     err.Error(),
		recordedAt: now,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]cooldownEntry)
	}
	c.entries[idx.ID] = entry
	slog.Info("indexer rate-limited; holding off further searches",
		"indexer", idx.Name, "until", entry.until.Format(time.RFC3339), "error", err)
	return true
}

// active reports whether idx is currently in cooldown, and if so a
// human-readable reason naming the deadline.
//
// A cooldown is dropped when the indexer row has been edited since it was
// recorded (UpdatedAt newer than recordedAt). Changing an indexer is the user
// saying "try this again" — a new API key, a different account, a re-enable —
// and making them wait out a lockout that belonged to the old configuration
// would be the wrong answer. The loops already hold the full models.Indexer, so
// this needs no extra wiring back into the handlers.
func (c *indexerCooldowns) active(idx models.Indexer) (string, bool) {
	if idx.ID == 0 {
		return "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[idx.ID]
	if !ok {
		return "", false
	}
	if !idx.UpdatedAt.IsZero() && idx.UpdatedAt.After(entry.recordedAt) {
		delete(c.entries, idx.ID)
		return "", false
	}
	now := c.clock()
	if !now.Before(entry.until) {
		delete(c.entries, idx.ID)
		return "", false
	}
	remaining := entry.until.Sub(now).Round(time.Minute)
	if remaining < time.Minute {
		remaining = time.Minute
	}
	return fmt.Sprintf("rate limited, retrying in %s (%s)", remaining, entry.reason), true
}

// cooldownActive reports whether the searcher is currently holding off on idx.
func (s *Searcher) cooldownActive(idx models.Indexer) (string, bool) {
	return s.cooldowns.active(idx)
}

// noteIndexerError records a rate-limit cooldown for idx when err is one.
func (s *Searcher) noteIndexerError(idx models.Indexer, err error) {
	s.cooldowns.note(idx, err)
}
