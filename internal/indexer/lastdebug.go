package indexer

import "sync"

// DebugOriginInteractive is the SearchDebug.Origin of a search run from the
// book page's Search button (POST /book/{id}/search). The automatic paths
// report their SearchOrigin instead ("scheduled", "bulk" and so on).
const DebugOriginInteractive = "interactive"

// debugLogBackgroundCap bounds how many automatic search trails are kept. A
// wanted sweep over a large library records one per book and format, and only
// the newest few are ever read, so the oldest are dropped.
const debugLogBackgroundCap = 64

// DebugLog holds the most recent search audit trails behind
// GET /api/v1/search/last-debug.
//
// It used to live in the API package and was written by exactly one caller,
// the interactive SearchBook handler, so a scheduled or bulk search never
// reached it and the endpoint kept answering with an older interactive search
// as if it were the latest (#2154). It lives here now so the scheduler, which
// runs those searches, can write to it as well.
//
// Two kinds of entry are kept, because they have different readers:
//
//   - An interactive search belongs to the user who pressed the button and is
//     kept per user, one slot each, so one user cannot read another's search
//     (#1859).
//   - An automatic search belongs to the owner of the book it searched for,
//     and is visible to whoever may see that book.
//
// Every entry carries its origin and book id in the payload, so a reader can
// tell which search it is looking at rather than assuming it is the one it
// just started.
type DebugLog struct {
	mu          sync.Mutex
	seq         uint64
	interactive map[int64]debugEntry
	background  []debugEntry
}

type debugEntry struct {
	seq         uint64
	dbg         *SearchDebug
	ownerUserID int64
}

// NewDebugLog returns an empty log.
func NewDebugLog() *DebugLog {
	return &DebugLog{interactive: make(map[int64]debugEntry)}
}

// RecordInteractive stores d as userID's latest interactive search. The
// caller must not modify d afterwards.
func (l *DebugLog) RecordInteractive(userID int64, d *SearchDebug) {
	if l == nil || d == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.interactive == nil {
		l.interactive = make(map[int64]debugEntry)
	}
	l.seq++
	l.interactive[userID] = debugEntry{seq: l.seq, dbg: d}
}

// RecordBackground stores d as an automatic search for a book owned by
// ownerUserID (0 for an unowned book). The caller must not modify d
// afterwards.
func (l *DebugLog) RecordBackground(ownerUserID int64, d *SearchDebug) {
	if l == nil || d == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.background = append(l.background, debugEntry{seq: l.seq, dbg: d, ownerUserID: ownerUserID})
	if over := len(l.background) - debugLogBackgroundCap; over > 0 {
		l.background = append(l.background[:0:0], l.background[over:]...)
	}
}

// Latest returns the newest search readerID may see, or nil.
//
// A reader sees their own latest interactive search. With
// seeAllInteractive they see every user's, which is for a caller holding the
// install's API key: that key is admin equivalent, and it is what lets a
// script read the search a signed in user just ran in the browser (#2154).
// Without it one user never sees another's interactive search (#1859).
//
// An automatic search is visible when canSeeBook reports that the reader may
// see a book with that owner. A nil canSeeBook hides every automatic search.
func (l *DebugLog) Latest(readerID int64, seeAllInteractive bool, canSeeBook func(ownerUserID int64) bool) *SearchDebug {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var best debugEntry
	if seeAllInteractive {
		for _, e := range l.interactive {
			if e.seq > best.seq {
				best = e
			}
		}
	} else if e, ok := l.interactive[readerID]; ok {
		best = e
	}
	if canSeeBook != nil {
		for i := len(l.background) - 1; i >= 0; i-- {
			e := l.background[i]
			if e.seq <= best.seq {
				break
			}
			if canSeeBook(e.ownerUserID) {
				best = e
				break
			}
		}
	}
	return best.dbg
}
