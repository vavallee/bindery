package models

import "time"

type BlocklistEntry struct {
	ID        int64     `json:"id"`
	BookID    *int64    `json:"bookId"`
	GUID      string    `json:"guid"`
	Title     string    `json:"title"`
	IndexerID *int64    `json:"indexerId"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
	// CreatedByUserID records which user promoted this row into the blocklist.
	// NULL for system-write paths (scheduler stall-detection, readarr import
	// migration) and for legacy rows that predate migration 049. The field is
	// audit-only: IsBlocked / List / BlocklistedSpec do not filter on it.
	// Which books an entry blocks is Scope's job, not the creator's.
	CreatedByUserID *int64 `json:"createdByUserId,omitempty"`
	// Scope is BlocklistScopeBook or BlocklistScopeRelease (migration 120,
	// #3563). Empty on a new entry means EffectiveScope decides.
	Scope string `json:"scope"`
}

// Blocklist entry scopes.
//
// A book scoped entry is a judgement about one book: its quality or metadata
// profile refused the release, the file did not match the book, or a user
// blocklisted a grab or an import of it from that book's History. It blocks
// the release for that book only, so one user's entry never decides what
// another user's book can grab.
//
// A release scoped entry says the release itself is bad for everyone: the
// download client failed it as broken or reported it stalled, a user
// blocklisted a failed or stalled download from History, or it came from a
// Readarr blocklist. It blocks the release for every book, the way migration
// 050 kept every entry global. An entry with no book is always release scoped,
// since there is no book to scope it to.
const (
	BlocklistScopeBook    = "book"
	BlocklistScopeRelease = "release"
)

// EffectiveScope is the scope the entry is stored and matched with: an
// explicit release scope, or no book at all, is release scoped, and anything
// else is book scoped. A stored book scoped entry whose book was deleted
// (book_id is ON DELETE SET NULL) keeps its book scope.
func (e BlocklistEntry) EffectiveScope() string {
	if e.Scope == BlocklistScopeRelease || (e.Scope == "" && e.BookID == nil) {
		return BlocklistScopeRelease
	}
	return BlocklistScopeBook
}

// BlocksBook reports whether the entry blocks its release for bookID. A book
// scoped entry whose book was deleted blocks nothing.
func (e BlocklistEntry) BlocksBook(bookID int64) bool {
	if e.EffectiveScope() == BlocklistScopeRelease {
		return true
	}
	return e.BookID != nil && *e.BookID == bookID
}
