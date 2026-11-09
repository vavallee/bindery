// Package searchruns keeps a small, bounded, in memory record of book
// searches so a caller that started one can poll how it ended (#3133).
//
// A run is registered when a search is accepted, moves to running when the
// scheduler picks it up, and is finished when the search returns. The fields
// it ends with are the ones the "book search finished" log line carries, so the
// log and the endpoint say the same thing.
//
// Nothing here is persisted: a restart forgets every run and an unknown id
// reads as absent. The store is bounded by count and by age and prunes when a
// run is registered, so it needs no background goroutine.
package searchruns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Defaults for the production store.
const (
	DefaultMax = 500
	DefaultTTL = time.Hour
)

// State is where a run is in its life.
type State string

const (
	// StateQueued: accepted, not yet picked up. A bulk search paces its books,
	// so a run can wait here for a while.
	StateQueued State = "queued"
	// StateRunning: the search is asking indexers or deciding.
	StateRunning State = "running"
	// StateFinished: the search returned. Outcome says how it ended.
	StateFinished State = "finished"
)

// OutcomeNotSearched is the outcome of a run that finished without any search
// recording a result, for example because the book needed no format.
const OutcomeNotSearched = "not searched"

// Release is the release a search grabbed. It carries no URL or GUID: those can
// hold indexer credentials, and the run is for telling the caller what
// happened, not for re-fetching it.
type Release struct {
	Title     string `json:"title"`
	Indexer   string `json:"indexer,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// Result is what one format's search recorded. A book searched in two formats
// records twice into the same run.
type Result struct {
	Outcome      string
	Indexers     int
	RawResults   int
	AfterFilters int
	Approved     int
	ElapsedMs    int64
	Grabbed      *Release
}

// Run is the read model of one search, as the endpoint returns it.
type Run struct {
	ID           string     `json:"id"`
	State        State      `json:"state"`
	BookID       int64      `json:"bookId"`
	Origin       string     `json:"origin,omitempty"`
	Outcome      string     `json:"outcome,omitempty"`
	Indexers     int        `json:"indexers"`
	RawResults   int        `json:"rawResults"`
	AfterFilters int        `json:"afterFilters"`
	Approved     int        `json:"approved"`
	ElapsedMs    int64      `json:"elapsedMs"`
	Grabbed      *Release   `json:"grabbed,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

type entry struct {
	run     Run
	owner   int64
	updated time.Time
	hasRes  bool
}

// Store is safe for concurrent use. The zero value is not usable; use
// NewStore. A nil *Store is inert: Start returns "" and every other method
// does nothing, so callers need no nil checks.
type Store struct {
	mu   sync.Mutex
	runs map[string]*entry
	max  int
	ttl  time.Duration
	now  func() time.Time
}

// NewStore returns a store holding at most max runs, each for ttl after its
// last update.
func NewStore(max int, ttl time.Duration) *Store {
	if max < 1 {
		max = DefaultMax
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{runs: make(map[string]*entry), max: max, ttl: ttl, now: time.Now}
}

// Start registers a queued run for book bookID owned by owner (0 for none) and
// returns its id.
func (s *Store) Start(owner, bookID int64, origin string) string {
	if s == nil {
		return ""
	}
	id := newID()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	// Make room for the new run by dropping the oldest started ones.
	for len(s.runs) >= s.max {
		var oldestID string
		var oldest time.Time
		for k, e := range s.runs {
			if oldestID == "" || e.run.StartedAt.Before(oldest) {
				oldestID, oldest = k, e.run.StartedAt
			}
		}
		delete(s.runs, oldestID)
	}
	s.runs[id] = &entry{
		run:     Run{ID: id, State: StateQueued, BookID: bookID, Origin: origin, StartedAt: now},
		owner:   owner,
		updated: now,
	}
	return id
}

// Running marks a run as picked up.
func (s *Store) Running(id string) {
	s.update(id, func(e *entry, _ time.Time) {
		if e.run.State == StateQueued {
			e.run.State = StateRunning
		}
	})
}

// Record merges one format's result into the run. Counts add up, the elapsed
// time is the sum, a grab in any format is kept, and a grabbed outcome is
// never replaced by a later one.
func (s *Store) Record(id string, r Result) {
	s.update(id, func(e *entry, _ time.Time) {
		run := &e.run
		run.Indexers += r.Indexers
		run.RawResults += r.RawResults
		run.AfterFilters += r.AfterFilters
		run.Approved += r.Approved
		run.ElapsedMs += r.ElapsedMs
		if r.Grabbed != nil && run.Grabbed == nil {
			g := *r.Grabbed
			run.Grabbed = &g
		}
		if run.Outcome != "grabbed" {
			run.Outcome = r.Outcome
		}
		e.hasRes = true
	})
}

// Finish marks the run finished. A run that recorded nothing finishes with
// OutcomeNotSearched.
func (s *Store) Finish(id string) {
	s.update(id, func(e *entry, now time.Time) {
		if e.run.State == StateFinished {
			return
		}
		if !e.hasRes {
			e.run.Outcome = OutcomeNotSearched
		}
		e.run.State = StateFinished
		e.run.FinishedAt = &now
	})
}

// Get returns the run with id if scope may see it. scope is the caller's
// auth.ListScopeUserID: 0 sees every run, any other value sees only the runs
// that user started. An unknown id, an expired run and a run the caller may
// not see are indistinguishable, so the endpoint leaks nothing about others.
func (s *Store) Get(id string, scope int64) (Run, bool) {
	if s == nil || id == "" {
		return Run{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[id]
	if !ok || s.now().Sub(e.updated) > s.ttl {
		return Run{}, false
	}
	if scope != 0 && e.owner != scope {
		return Run{}, false
	}
	out := e.run
	if out.Grabbed != nil {
		g := *out.Grabbed
		out.Grabbed = &g
	}
	return out, true
}

// Len reports how many runs are held, expired ones included until the next
// write prunes them.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *Store) update(id string, fn func(*entry, time.Time)) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[id]
	if !ok {
		return
	}
	now := s.now()
	fn(e, now)
	e.updated = now
}

func (s *Store) pruneLocked(now time.Time) {
	for k, e := range s.runs {
		if now.Sub(e.updated) > s.ttl {
			delete(s.runs, k)
		}
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("searchruns: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

type ctxKey struct{}

// WithRunID returns ctx carrying a run id, so a handler can register a run,
// answer with its id, and let the search it launches fill it in. An empty id
// returns ctx unchanged.
func WithRunID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// RunIDFrom returns the run id ctx carries, or "".
func RunIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
