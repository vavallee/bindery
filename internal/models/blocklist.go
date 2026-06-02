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
	// audit-only: IsBlocked / List / BlocklistedSpec do not filter on it, and
	// the blocklist remains global by design. See migration 049 for the
	// rationale.
	CreatedByUserID *int64 `json:"createdByUserId,omitempty"`
}
