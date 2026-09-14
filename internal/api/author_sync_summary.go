package api

import (
	"sync"

	"github.com/vavallee/bindery/internal/models"
)

// authorSyncSkippedSampleLimit caps how many rejected titles a summary carries.
// The point is to let the user recognise WHAT is being dropped, not to mirror
// the whole rejected tail into the author payload — a prolific author can have
// hundreds of foreign-language works.
const authorSyncSkippedSampleLimit = 5

// authorSyncSummaries remembers the outcome of the last catalogue sync per
// author so the author detail endpoint can report it (#1889).
//
// Deliberately in-process rather than a table: the counts are diagnostic
// output about a run, not library state, and the moment they matter is right
// after the refresh the user just triggered. That keeps the fix free of a
// schema change, at the cost of the summary disappearing on restart — which is
// the same lifetime the log ring buffer behind Settings → Logs has.
type authorSyncSummaries struct {
	mu       sync.Mutex
	byAuthor map[int64]models.AuthorSyncSummary
}

// record stores the summary for authorID, replacing any earlier one. Only the
// most recent sync is kept; an older run's counts describe a catalogue that no
// longer exists.
func (s *authorSyncSummaries) record(authorID int64, summary models.AuthorSyncSummary) {
	if authorID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAuthor == nil {
		s.byAuthor = make(map[int64]models.AuthorSyncSummary)
	}
	s.byAuthor[authorID] = summary
}

// get returns a copy of the last recorded summary for authorID, or nil when
// this process hasn't synced that author. A copy so a caller attaching it to a
// response can't mutate the stored record.
func (s *authorSyncSummaries) get(authorID int64) *models.AuthorSyncSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary, ok := s.byAuthor[authorID]
	if !ok {
		return nil
	}
	summary.AllowedLanguages = append([]string(nil), summary.AllowedLanguages...)
	summary.SkippedLanguageSample = append([]models.AuthorSyncSkippedBook(nil), summary.SkippedLanguageSample...)
	return &summary
}

// forget drops the recorded summary for authorID. Called when the author row
// goes away so a re-added author can't inherit the deleted one's counts.
func (s *authorSyncSummaries) forget(authorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byAuthor, authorID)
}

// authorSyncsRunning counts the catalogue syncs in flight per author. The
// manual Refresh uses it to refuse a second sync for an author whose first is
// still running, and the author Get handler reports it so the page can wait
// for the sync a click started (#2601). A count rather than a set because the
// add, bulk and scheduled paths can overlap a sync for the same author, and
// one of them finishing must not clear the other's mark.
type authorSyncsRunning struct {
	mu       sync.Mutex
	byAuthor map[int64]int
}

// tryStart counts a sync for authorID only when none is running, and reports
// whether it did.
func (s *authorSyncsRunning) tryStart(authorID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAuthor[authorID] > 0 {
		return false
	}
	s.startLocked(authorID)
	return true
}

// start counts a sync for authorID whether or not another is running.
func (s *authorSyncsRunning) start(authorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked(authorID)
}

func (s *authorSyncsRunning) startLocked(authorID int64) {
	if s.byAuthor == nil {
		s.byAuthor = make(map[int64]int)
	}
	s.byAuthor[authorID]++
}

// done ends one counted sync for authorID.
func (s *authorSyncsRunning) done(authorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAuthor[authorID] <= 1 {
		delete(s.byAuthor, authorID)
		return
	}
	s.byAuthor[authorID]--
}

// running reports whether any sync for authorID is in flight.
func (s *authorSyncsRunning) running(authorID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byAuthor[authorID] > 0
}
